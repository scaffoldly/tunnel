package gateway

import (
	"context"
	_ "embed"
	"fmt"
	"strings"
	"time"

	"golang.org/x/mod/semver"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// Annotations every Gateway API CRD carries, and the two things the upstream
// rules are expressed in terms of.
const (
	annotationBundleVersion = "gateway.networking.k8s.io/bundle-version"
	annotationChannel       = "gateway.networking.k8s.io/channel"
)

// crdYAML is the standard channel bundle, vendored at the version in go.mod.
// Regenerate with `make crds` when that version moves; the two disagreeing
// would mean installing a schema the controller cannot deserialize.
//
//go:embed crds/standard-install.yaml
var crdYAML []byte

// bundledVersion is the release the vendored bundle must carry. A test pins it
// against the file, so `make crds` after a go.mod bump is not something anyone
// has to remember.
const bundledVersion = "v1.6.1"

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
