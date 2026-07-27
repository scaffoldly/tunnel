package gateway

import (
	"context"
	_ "embed"
	"fmt"
	"slices"
	"strings"
	"time"

	"golang.org/x/mod/semver"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayconsts "sigs.k8s.io/gateway-api/pkg/consts"
)

// Annotations every Gateway API CRD carries, and the two things the upstream
// rules are expressed in terms of.
//
// Taken from the gateway-api module's own consts rather than spelled out, for
// the same reason ControllerName is read by reflection: a string that must
// equal someone else's string should be that string.
const (
	annotationBundleVersion = gatewayconsts.BundleVersionAnnotation
	annotationChannel       = gatewayconsts.ChannelAnnotation
)

// crdYAML is the standard channel bundle, copied from the gateway-api module
// itself rather than fetched from a release URL — go:embed cannot reach into a
// dependency, so it has to be vendored, but it can be vendored from the exact
// version the compiler resolved.
//
// Regenerate with `go generate ./gateway/...` when go.mod moves; a test fails
// if the two disagree, because installing a schema the controller cannot
// deserialize is worse than not installing one.
//
//go:generate make -C .. crds
//go:embed crds/zz_generated.standard-install.yaml
var crdYAML []byte

// bundledVersion is the Gateway API release this controller is built for:
// the version the module the compiler resolved says it is, not a string
// maintained here. TestBundleVersionMatchesGoMod pins the embedded YAML to it,
// so `make crds` after a go.mod bump is not something anyone has to remember —
// and now a bump cannot leave this constant behind either.
//
// Two things read it: installCRDs, which will not overwrite something newer,
// and checkVersions, which decides the GatewayClass SupportedVersion condition.
const bundledVersion = gatewayconsts.BundleVersion

// supportedVersions is the range checkVersions accepts, and the range the
// SupportedVersion condition advertises. Major and minor only.
//
// Upstream's versioning policy is that a patch release may not change the
// schema, so v1.6.0 and v1.6.1 are the same API to anyone reading it — pinning
// the patch would report a cluster unsupported over a difference that cannot
// affect us. Minor is where fields appear and validation tightens, so it is
// the boundary worth checking. This is also what the reference implementation
// does: nginx-gateway-fabric compares major and minor against upstream's
// consts.BundleVersion and ignores the patch.
var supportedVersions = semver.MajorMinor(bundledVersion)

// installCRDs makes a cluster serve the Gateway API when it does not already.
//
// The CRDs are a shared, cluster-scoped resource: several implementations can
// run in one cluster and they all read the same definitions, so upstream sets
// three rules for any implementation that bundles them. Never overwrite CRDs
// at an unrecognized or newer version, never overwrite a different release
// channel, and never remove them. decide() below is those three rules and
// nothing else.
//
// Gated on --install, alongside the classes: a cluster that manages its own
// Gateway API — Istio, Cilium, an admin with Argo — turns the flag off and
// this never runs.
func installCRDs(ctx context.Context, c client.Client) error {
	logger := log.FromContext(ctx)

	ours, err := parseCRDs(crdYAML)
	if err != nil {
		return fmt.Errorf("parse bundled crds: %w", err)
	}

	for _, crd := range ours {
		var existing apiextensionsv1.CustomResourceDefinition
		err := c.Get(ctx, client.ObjectKey{Name: crd.Name}, &existing)

		switch {
		case apierrors.IsNotFound(err):
			if err := c.Create(ctx, crd); err != nil {
				return fmt.Errorf("create crd %s: %w", crd.Name, err)
			}
			logger.Info("created gateway api crd", "crd", crd.Name,
				"version", crd.Annotations[annotationBundleVersion])

		case err != nil:
			return fmt.Errorf("get crd %s: %w", crd.Name, err)

		default:
			apply, reason := decide(&existing, crd)
			if !apply {
				logger.Info("leaving gateway api crd alone", "crd", crd.Name, "reason", reason,
					"existing", existing.Annotations[annotationBundleVersion],
					"bundled", crd.Annotations[annotationBundleVersion])
				continue
			}
			// ResourceVersion carries over or the update is rejected as a
			// conflict. Everything else is ours.
			crd.ResourceVersion = existing.ResourceVersion
			if err := c.Update(ctx, crd); err != nil {
				return fmt.Errorf("update crd %s: %w", crd.Name, err)
			}
			logger.Info("upgraded gateway api crd", "crd", crd.Name, "reason", reason,
				"from", existing.Annotations[annotationBundleVersion],
				"to", crd.Annotations[annotationBundleVersion])
		}
	}

	return nil
}

// decide reports whether the bundled CRD may overwrite the one already in the
// cluster, and why. It is the upstream ruleset, and it is deliberately biased
// toward leaving things alone: the cost of a wrong "no" is that the Gateway
// half stays disabled, and the cost of a wrong "yes" is breaking whatever
// other implementation shares these definitions.
func decide(existing, ours *apiextensionsv1.CustomResourceDefinition) (bool, string) {
	haveChannel := existing.Annotations[annotationChannel]
	wantChannel := ours.Annotations[annotationChannel]
	if haveChannel != wantChannel {
		// Rule 2. Experimental carries fields standard does not, so
		// downgrading a channel silently drops data from live objects.
		return false, fmt.Sprintf("different release channel (%q, bundled is %q)", haveChannel, wantChannel)
	}

	have := existing.Annotations[annotationBundleVersion]
	if !semver.IsValid(have) {
		// Rule 1, the unrecognized half. Something else installed these and
		// did not say what they are; assume it knows better.
		return false, fmt.Sprintf("unrecognized version %q", have)
	}

	want := ours.Annotations[annotationBundleVersion]
	if !semver.IsValid(want) {
		// Our own bundle is malformed. Refuse rather than guess.
		return false, fmt.Sprintf("bundled version %q is not valid semver", want)
	}

	if semver.Compare(have, want) >= 0 {
		// Rule 1, the newer half. Equal counts: rewriting an identical CRD
		// churns resourceVersion for nothing.
		return false, fmt.Sprintf("cluster has %s, not older than bundled %s", have, want)
	}

	return true, fmt.Sprintf("cluster has %s, bundled is newer at %s", have, want)
}

// versionReport is what the cluster's Gateway API CRDs say about themselves,
// reduced to the two things the SupportedVersion condition needs.
type versionReport struct {
	// supported is the condition's status. False is not a refusal to serve —
	// see checkVersions.
	supported bool
	// detail is the middle of the condition's message: what was found, in the
	// cluster's own terms. Upstream asks that the message name both the
	// detected versions and the supported ones.
	detail string
}

// checkVersions decides the GatewayClass SupportedVersion condition by reading
// the bundle-version annotation off every Gateway API CRD the cluster serves.
//
// # The policy, and why it is this one
//
// Upstream defines the input precisely and leaves the judgement to us:
//
//	The version of a Gateway API CRD is defined by the
//	gateway.networking.k8s.io/bundle-version annotation on the CRD. If
//	implementations detect any Gateway API CRDs that either do not have this
//	annotation set, or have it set to a version that is not recognized or
//	supported by the implementation, this condition MUST be set to false.
//
// So: an unannotated CRD is False, not an exemption. That is worth stating
// because the opposite reads as the kinder choice — a missing annotation is
// missing metadata, not a wrong version — but the whole point of the condition
// is that an implementation cannot vouch for CRDs it cannot identify. The
// distinction survives in the message, which is where upstream puts it, rather
// than in the status.
//
// Recognized means the same major and minor as the release we are built for;
// see supportedVersions for why the patch is ignored.
//
// # False does not stop provisioning
//
// Upstream offers two behaviours for unrecognized CRDs and we take the first
// explicitly: "best effort" support, Accepted=True with SupportedVersion=False.
// The alternative — refusing the class with Accepted=False — would mean a
// GatewayClass whose Gateways demonstrably provision and serve reports itself
// unusable, which is the exact bug this package shipped for a release and the
// exact way a conformant consumer gets talked out of a working class. The
// reference implementation takes the harder line and it has cost its users:
// nginx-gateway-fabric refuses the class outright, and one unannotated CRD
// left behind in a cluster is enough to do it (nginx/nginx-gateway-fabric#4762).
//
// It is also proportionate to what this controller actually reads: a
// GatewayClass's name and controllerName, a Gateway's gatewayClassName, and an
// HTTPRoute's parentRefs and backendRefs. Those fields are unchanged since
// v1.0. A version skew that breaks them is possible; one that breaks them
// silently, in a way an outright refusal would have caught, is not worth the
// cost of refusing every skew.
//
// # Channel is not part of this
//
// A cluster running the experimental channel at our version is running a
// superset of our schema, and every field above is in both channels.
// installCRDs cares about the channel because writing across channels drops
// data; reading does not. Reporting an experimental install unsupported would
// under-report, which is the failure mode this condition is most prone to.
func checkVersions(ctx context.Context, r client.Reader) (versionReport, error) {
	// Metadata only. The Gateway API bundle is about 700KB of schema across a
	// dozen CRDs, all of it irrelevant here, and this runs on every
	// GatewayClass reconcile — the annotation is in the metadata, so ask for
	// the metadata.
	var list metav1.PartialObjectMetadataList
	list.SetGroupVersionKind(apiextensionsv1.SchemeGroupVersion.WithKind("CustomResourceDefinitionList"))
	if err := r.List(ctx, &list); err != nil {
		return versionReport{}, fmt.Errorf("list customresourcedefinitions: %w", err)
	}

	var (
		versions    []string
		unannotated []string
	)
	for _, crd := range list.Items {
		// The API server enforces that a CRD's name is <plural>.<group>, so
		// the suffix is an exact test for the group and not a guess at one.
		// Every Gateway API CRD counts, including kinds this controller never
		// reads: upstream says "any Gateway API CRDs", and a v0.x TCPRoute
		// left behind in a cluster is exactly the kind of thing worth naming
		// in a message.
		if !strings.HasSuffix(crd.Name, "."+gatewayv1.GroupName) {
			continue
		}
		if v := crd.Annotations[annotationBundleVersion]; v != "" {
			versions = append(versions, v)
			continue
		}
		unannotated = append(unannotated, crd.Name)
	}

	return report(versions, unannotated), nil
}

// report turns what was found into the condition's status and message. Split
// out from the read so the policy can be tested without a cluster.
func report(versions, unannotated []string) versionReport {
	slices.Sort(unannotated)
	distinct := slices.Compact(slices.Sorted(slices.Values(versions)))

	switch {
	case len(distinct) == 0 && len(unannotated) == 0:
		// Not reachable through New, which registers nothing unless the API
		// server serves Gateway API kinds. Reachable if every CRD is deleted
		// while we run, and "no CRDs" is not evidence of a supported version.
		return versionReport{supported: false, detail: "are not installed"}

	case len(unannotated) > 0:
		detail := fmt.Sprintf("are missing the %s annotation on %s", annotationBundleVersion,
			strings.Join(unannotated, ", "))
		if len(distinct) > 0 {
			detail = fmt.Sprintf("are at %s, and %s", strings.Join(distinct, ", "), detail)
		}
		return versionReport{supported: false, detail: detail}
	}

	detail := fmt.Sprintf("are at %s", strings.Join(distinct, ", "))
	for _, v := range distinct {
		// MajorMinor returns "" for anything it cannot parse, which never
		// equals supportedVersions — so a version string that is not semver at
		// all lands on unsupported without a separate branch to keep honest.
		if semver.MajorMinor(v) != supportedVersions {
			return versionReport{supported: false, detail: detail}
		}
	}
	return versionReport{supported: true, detail: detail}
}

// parseCRDs reads the multi-document bundle. Anything that is not a CRD is
// ignored rather than applied — the bundle is trusted, but the blast radius of
// a stray object in a file this large is not worth taking on faith.
func parseCRDs(b []byte) ([]*apiextensionsv1.CustomResourceDefinition, error) {
	var out []*apiextensionsv1.CustomResourceDefinition

	decoder := yaml.NewYAMLToJSONDecoder(strings.NewReader(string(b)))
	for {
		var crd apiextensionsv1.CustomResourceDefinition
		err := decoder.Decode(&crd)
		if err != nil {
			if err.Error() == "EOF" {
				break
			}
			return nil, err
		}
		if crd.Kind != "CustomResourceDefinition" || crd.Name == "" {
			continue
		}
		out = append(out, &crd)
	}

	if len(out) == 0 {
		return nil, fmt.Errorf("no CustomResourceDefinitions in the bundle")
	}
	return out, nil
}

// awaitEstablished blocks until the API server serves the kinds this
// controller watches.
//
// A CRD exists the moment it is created but is not servable until its
// Established condition flips, and controller-runtime fails to start a manager
// watching a kind the API server does not yet serve. Without this the very
// first install on a fresh cluster would race and crash-loop once.
//
// Only the kinds actually watched are waited on. The bundle carries route
// types this controller never reads, and blocking on those would be waiting
// for something nobody needs.
func awaitEstablished(ctx context.Context, c client.Client) error {
	want := []string{
		"gatewayclasses." + gatewayv1.GroupName,
		"gateways." + gatewayv1.GroupName,
	}

	for _, name := range want {
		if err := wait.PollUntilContextTimeout(ctx, 250*time.Millisecond, 60*time.Second, true,
			func(ctx context.Context) (bool, error) {
				var crd apiextensionsv1.CustomResourceDefinition
				if err := c.Get(ctx, client.ObjectKey{Name: name}, &crd); err != nil {
					if apierrors.IsNotFound(err) {
						return false, nil
					}
					return false, err
				}
				for _, cond := range crd.Status.Conditions {
					if cond.Type == apiextensionsv1.Established {
						return cond.Status == apiextensionsv1.ConditionTrue, nil
					}
				}
				return false, nil
			}); err != nil {
			return fmt.Errorf("crd %s never became established: %w", name, err)
		}
	}
	return nil
}
