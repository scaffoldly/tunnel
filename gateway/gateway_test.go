package gateway

import (
	"context"
	"errors"
	"strings"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/scaffoldly/tunnel/consts"
)

// What the Gateway API spec fixes for the two conditions on a GatewayClass.
//
// Literals, not the constants the controller itself uses: an assertion written
// in terms of gatewayv1.GatewayClassReasonAccepted holds however the condition
// is built, and would have passed just as happily against the Waiting/False
// condition this package published while it already provisioned. The literals
// are cross-checked against upstream in TestConditionsMatchTheSpec, so an
// upstream rename surfaces there rather than as a weakened assertion here.
const (
	conditionAccepted = "Accepted"
	reasonAccepted    = "Accepted"

	conditionSupportedVersion = "SupportedVersion"
	reasonSupportedVersion    = "SupportedVersion"
	reasonUnsupportedVersion  = "UnsupportedVersion"
)

// Bundle versions the tests reason about, written out rather than derived from
// bundledVersion for the same reason as above. TestSupportedVersionsPin ties
// them to what the build actually supports.
const (
	versionSupported      = "v1.6.1" // what we bundle
	versionSamePatchOlder = "v1.6.0" // same minor, older patch: still supported
	versionOlderMinor     = "v1.4.1" // a real release, older minor
	versionNewerMinor     = "v1.7.0" // ahead of us, cannot be vouched for
)

func classRequest(name string) ctrl.Request {
	return ctrl.Request{NamespacedName: types.NamespacedName{Name: name}}
}

// gatewayCRD is a Gateway API CRD at the given bundle version. An empty
// version leaves the annotation off entirely, which is a case upstream calls
// out by name.
func gatewayCRD(name, version string) client.Object {
	crd := &apiextensionsv1.CustomResourceDefinition{ObjectMeta: metav1.ObjectMeta{Name: name}}
	if version != "" {
		crd.Annotations = map[string]string{annotationBundleVersion: version}
	}
	return crd
}

// gatewayCRDs is the set a cluster serving the Gateway API has, all at one
// version. Only the two kinds this controller watches — enough to be realistic
// without restating the bundle.
func gatewayCRDs(version string) []client.Object {
	return []client.Object{
		gatewayCRD("gatewayclasses.gateway.networking.k8s.io", version),
		gatewayCRD("gateways.gateway.networking.k8s.io", version),
	}
}

// classReconciler wires a ClassReconciler to a fake cluster holding both the
// classes and the CRDs. The CRD reader is the same client, standing in for the
// manager's uncached reader.
func classReconciler(objs ...client.Object) (*ClassReconciler, client.WithWatch) {
	c := newFakeClient(objs...)
	return &ClassReconciler{Client: c, CRDs: c}, c
}

// condition returns one of the class's conditions, failing if it is absent:
// Gateway API requires both of these on a class this controller accepts.
func condition(t *testing.T, c client.Client, name, conditionType string) *metav1.Condition {
	t.Helper()
	got := getClass(t, c, name)
	cond := meta.FindStatusCondition(got.Status.Conditions, conditionType)
	if cond == nil {
		t.Fatalf("gatewayclass %q publishes no %s condition; Gateway API requires one",
			name, conditionType)
	}
	return cond
}

func acceptedCondition(t *testing.T, c client.Client, name string) *metav1.Condition {
	t.Helper()
	return condition(t, c, name, conditionAccepted)
}

func supportedVersionCondition(t *testing.T, c client.Client, name string) *metav1.Condition {
	t.Helper()
	return condition(t, c, name, conditionSupportedVersion)
}

// TestConditionsMatchTheSpec pins the strings the assertions below are written
// in terms of against the Gateway API's own constants.
func TestConditionsMatchTheSpec(t *testing.T) {
	for _, tt := range []struct {
		what     string
		upstream string
		asserted string
	}{
		{"Accepted condition type", string(gatewayv1.GatewayClassConditionStatusAccepted), conditionAccepted},
		{"reason for Accepted=True", string(gatewayv1.GatewayClassReasonAccepted), reasonAccepted},
		{"SupportedVersion condition type", string(gatewayv1.GatewayClassConditionStatusSupportedVersion), conditionSupportedVersion},
		{"reason for SupportedVersion=True", string(gatewayv1.GatewayClassReasonSupportedVersion), reasonSupportedVersion},
		{"reason for SupportedVersion=False", string(gatewayv1.GatewayClassReasonUnsupportedVersion), reasonUnsupportedVersion},
	} {
		if tt.upstream != tt.asserted {
			t.Errorf("upstream %s = %q, tests assert %q", tt.what, tt.upstream, tt.asserted)
		}
	}
}

// TestSupportedVersionsPin is the one place a gateway-api bump has to be
// noticed. The version literals above and the e2e assert in
// tests/e2e/gateway/00-assert.yaml describe v1.6; when go.mod moves to a new
// minor, this fails and both need updating along with `make crds`.
func TestSupportedVersionsPin(t *testing.T) {
	if want := "v1.6"; supportedVersions != want {
		t.Fatalf("supportedVersions = %q, want %q — gateway-api moved to a new minor: "+
			"update the version literals in this file and the SupportedVersion assert in "+
			"tests/e2e/gateway/00-assert.yaml", supportedVersions, want)
	}
	if want := "v1.6.1"; bundledVersion != want {
		t.Errorf("bundledVersion = %q, want %q — the module moved; run `make crds`", bundledVersion, want)
	}
}

// TestClassReconcileAcceptsOurClass is the regression guard for a condition
// that lied for a whole release: the class reported Accepted=False, reason
// Waiting, "tunnel provisioning is not implemented yet", while Gateways naming
// that very class minted tunnels and served traffic. Nothing read it, so
// nothing caught it.
//
// A conformant consumer is entitled to refuse a class its controller has not
// accepted, so False here is worse than publishing nothing at all.
func TestClassReconcileAcceptsOurClass(t *testing.T) {
	class := gatewayClass(consts.ProviderTunnelPizza, ControllerName)
	// Not 0: observedGeneration matching by accident proves nothing.
	class.Generation = 7

	r, c := classReconciler(append(gatewayCRDs(versionSupported), class)...)

	if _, err := r.Reconcile(context.Background(), classRequest(consts.ProviderTunnelPizza)); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	cond := acceptedCondition(t, c, consts.ProviderTunnelPizza)

	if cond.Status != metav1.ConditionTrue {
		t.Errorf("Accepted status = %q (reason %q, message %q), want %q — Gateways on this class do provision",
			cond.Status, cond.Reason, cond.Message, metav1.ConditionTrue)
	}
	if cond.Reason != reasonAccepted {
		t.Errorf("Accepted reason = %q, want %q", cond.Reason, reasonAccepted)
	}
	// Without it, a consumer cannot tell this condition from one written
	// before the last edit to the class.
	if cond.ObservedGeneration != 7 {
		t.Errorf("Accepted observedGeneration = %d, want %d", cond.ObservedGeneration, 7)
	}
	if cond.LastTransitionTime.IsZero() {
		t.Error("Accepted lastTransitionTime is zero; the API server requires it")
	}

	// The message is user-facing — `kubectl describe gatewayclass` — so it may
	// not go on disclaiming a capability the controller has.
	if cond.Message == "" {
		t.Error("Accepted message is empty")
	}
	for _, bad := range []string{"not implemented", "unimplemented", "stub", "scaffold"} {
		if strings.Contains(strings.ToLower(cond.Message), bad) {
			t.Errorf("Accepted message %q still says %q", cond.Message, bad)
		}
	}
	// It names where the tunnels come from, and the class's name is the
	// provider — so a class installed for Cloudflare must not advertise ours.
	if !strings.Contains(cond.Message, consts.ProviderTunnelPizza) {
		t.Errorf("Accepted message %q does not name the provider %q",
			cond.Message, consts.ProviderTunnelPizza)
	}
}

// Each installed class is accepted on its own terms; the message follows the
// class's name, which is the provider it mints from.
func TestClassReconcileAcceptsEveryInstalledProvider(t *testing.T) {
	objs := gatewayCRDs(versionSupported)
	for _, provider := range consts.InstalledProviders {
		objs = append(objs, gatewayClass(provider, ControllerName))
	}
	r, c := classReconciler(objs...)

	for _, provider := range consts.InstalledProviders {
		if _, err := r.Reconcile(context.Background(), classRequest(provider)); err != nil {
			t.Fatalf("%s: Reconcile() error = %v", provider, err)
		}
		cond := acceptedCondition(t, c, provider)
		if cond.Status != metav1.ConditionTrue {
			t.Errorf("%s: Accepted status = %q, want %q", provider, cond.Status, metav1.ConditionTrue)
		}
		if !strings.Contains(cond.Message, provider) {
			t.Errorf("%s: Accepted message %q names another provider", provider, cond.Message)
		}
	}
}

// A class naming someone else's controller is theirs to accept. Writing a
// condition to it would fight the controller that actually implements it.
func TestClassReconcileLeavesForeignClassAlone(t *testing.T) {
	const foreign gatewayv1.GatewayController = "example.com/other-controller"
	r, c := classReconciler(append(gatewayCRDs(versionSupported),
		gatewayClass(consts.ProviderTunnelPizza, foreign))...)

	if _, err := r.Reconcile(context.Background(), classRequest(consts.ProviderTunnelPizza)); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	if got := getClass(t, c, consts.ProviderTunnelPizza); len(got.Status.Conditions) != 0 {
		t.Errorf("wrote %v to another controller's class", got.Status.Conditions)
	}
}

// A deleted class is not an error: the event outlives the object.
func TestClassReconcileIgnoresMissingClass(t *testing.T) {
	r, _ := classReconciler(gatewayCRDs(versionSupported)...)
	if _, err := r.Reconcile(context.Background(), classRequest(consts.ProviderTunnelPizza)); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
}

// An unchanged condition must not be rewritten: a status write is a watch
// event, which is another reconcile, which is another write.
func TestClassReconcileWritesOnlyWhenTheConditionChanges(t *testing.T) {
	class := gatewayClass(consts.ProviderTunnelPizza, ControllerName)
	class.Generation = 1

	var writes int
	c := fake.NewClientBuilder().
		WithScheme(scheme()).
		WithObjects(append(gatewayCRDs(versionSupported), class)...).
		WithStatusSubresource(&gatewayv1.GatewayClass{}).
		WithInterceptorFuncs(interceptor.Funcs{
			SubResourceUpdate: func(ctx context.Context, cl client.Client, sub string,
				obj client.Object, opts ...client.SubResourceUpdateOption) error {
				writes++
				return cl.Status().Update(ctx, obj, opts...)
			},
		}).
		Build()
	// Both conditions are computed every reconcile, so this also covers the
	// case where one changes and the other does not: one write, not two.
	r := &ClassReconciler{Client: c, CRDs: c}

	for range 3 {
		if _, err := r.Reconcile(context.Background(), classRequest(consts.ProviderTunnelPizza)); err != nil {
			t.Fatalf("Reconcile() error = %v", err)
		}
	}
	if writes != 1 {
		t.Errorf("wrote status %d times for one unchanged class, want 1", writes)
	}

	// A new generation is a real change, and the condition has to follow it or
	// it cannot be told apart from a stale one.
	stored := getClass(t, c, consts.ProviderTunnelPizza)
	stored.Generation = 2
	if err := c.Update(context.Background(), stored); err != nil {
		t.Fatalf("update gatewayclass: %v", err)
	}
	if _, err := r.Reconcile(context.Background(), classRequest(consts.ProviderTunnelPizza)); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if writes != 2 {
		t.Errorf("wrote status %d times after a generation bump, want 2", writes)
	}
	if got := acceptedCondition(t, c, consts.ProviderTunnelPizza).ObservedGeneration; got != 2 {
		t.Errorf("Accepted observedGeneration = %d after the bump, want 2", got)
	}
}

// TestClassReconcilePublishesSupportedVersion covers the condition Gateway API
// requires alongside Accepted: "this condition MUST be set by a controller when
// it marks a GatewayClass Accepted".
//
// The table is the policy in checkVersions, stated in versions rather than in
// the code's own terms. Two of these cases are the ones that actually happen —
// CRDs we installed ourselves, and CRDs somebody else installed at another
// version — and two are the ones that get mishandled: an unannotated CRD, and
// a CRD newer than the build.
func TestClassReconcilePublishesSupportedVersion(t *testing.T) {
	tests := []struct {
		name string
		crds []client.Object
		// want is the condition's status; wantIn is a substring the message
		// must carry, so a user can see what was detected.
		want   metav1.ConditionStatus
		wantIn string
	}{
		{
			name:   "the version we bundle",
			crds:   gatewayCRDs(versionSupported),
			want:   metav1.ConditionTrue,
			wantIn: versionSupported,
		},
		{
			// Upstream's versioning policy forbids schema changes in a patch
			// release, so pinning the patch would report a cluster unsupported
			// over a difference that cannot reach us.
			name:   "same minor, older patch",
			crds:   gatewayCRDs(versionSamePatchOlder),
			want:   metav1.ConditionTrue,
			wantIn: versionSamePatchOlder,
		},
		{
			name:   "an older minor",
			crds:   gatewayCRDs(versionOlderMinor),
			want:   metav1.ConditionFalse,
			wantIn: versionOlderMinor,
		},
		{
			// Newer is not better here: a release we were not built against
			// is one we cannot vouch for, in either direction.
			name:   "a newer minor",
			crds:   gatewayCRDs(versionNewerMinor),
			want:   metav1.ConditionFalse,
			wantIn: versionNewerMinor,
		},
		{
			// Upstream is explicit that a missing annotation is False, not an
			// exemption: an implementation cannot vouch for CRDs it cannot
			// identify. The message is where the distinction lives, so it has
			// to name the CRD.
			name:   "no bundle-version annotation at all",
			crds:   gatewayCRDs(""),
			want:   metav1.ConditionFalse,
			wantIn: "gateways.gateway.networking.k8s.io",
		},
		{
			// The mixed cluster: ours installed cleanly, something older left
			// an unannotated CRD behind. Both belong in the message.
			name: "one annotated, one not",
			crds: []client.Object{
				gatewayCRD("gateways.gateway.networking.k8s.io", versionSupported),
				gatewayCRD("tcproutes.gateway.networking.k8s.io", ""),
			},
			want:   metav1.ConditionFalse,
			wantIn: "tcproutes.gateway.networking.k8s.io",
		},
		{
			// Every Gateway API CRD counts, not just the kinds we watch.
			name: "a stale CRD for a kind this controller never reads",
			crds: []client.Object{
				gatewayCRD("gatewayclasses.gateway.networking.k8s.io", versionSupported),
				gatewayCRD("gateways.gateway.networking.k8s.io", versionSupported),
				gatewayCRD("tcproutes.gateway.networking.k8s.io", versionOlderMinor),
			},
			want:   metav1.ConditionFalse,
			wantIn: versionOlderMinor,
		},
		{
			// Not every cluster's CRDs come from a release. "Unrecognized" is
			// the same answer as "older": we cannot vouch for it either way.
			name:   "a version string that is not a version",
			crds:   gatewayCRDs("main-branch"),
			want:   metav1.ConditionFalse,
			wantIn: "main-branch",
		},
		{
			// Somebody else's CRDs are not ours to have an opinion about.
			name: "another group's CRDs are ignored",
			crds: []client.Object{
				gatewayCRD("gateways.gateway.networking.k8s.io", versionSupported),
				gatewayCRD("certificates.cert-manager.io", ""),
			},
			want:   metav1.ConditionTrue,
			wantIn: versionSupported,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			class := gatewayClass(consts.ProviderTunnelPizza, ControllerName)
			class.Generation = 3
			r, c := classReconciler(append(tt.crds, class)...)

			if _, err := r.Reconcile(context.Background(), classRequest(consts.ProviderTunnelPizza)); err != nil {
				t.Fatalf("Reconcile() error = %v", err)
			}

			cond := supportedVersionCondition(t, c, consts.ProviderTunnelPizza)
			if cond.Status != tt.want {
				t.Errorf("SupportedVersion status = %q (reason %q, message %q), want %q",
					cond.Status, cond.Reason, cond.Message, tt.want)
			}

			// The reason is fixed by the spec in both directions.
			wantReason := reasonUnsupportedVersion
			if tt.want == metav1.ConditionTrue {
				wantReason = reasonSupportedVersion
			}
			if cond.Reason != wantReason {
				t.Errorf("SupportedVersion reason = %q, want %q", cond.Reason, wantReason)
			}
			if cond.ObservedGeneration != 3 {
				t.Errorf("SupportedVersion observedGeneration = %d, want 3", cond.ObservedGeneration)
			}

			// Upstream asks the message to name what was detected and what is
			// supported, since the status alone cannot say which disagreed.
			if !strings.Contains(cond.Message, tt.wantIn) {
				t.Errorf("SupportedVersion message %q does not mention %q", cond.Message, tt.wantIn)
			}
			if !strings.Contains(cond.Message, "v1.6") {
				t.Errorf("SupportedVersion message %q does not say which versions are supported", cond.Message)
			}

			// Whatever the versions say, the class stays accepted: this is the
			// best-effort half of the choice upstream offers, and refusing a
			// class whose Gateways provision is the bug this package had.
			if got := acceptedCondition(t, c, consts.ProviderTunnelPizza); got.Status != metav1.ConditionTrue {
				t.Errorf("Accepted status = %q, want %q — an unrecognized CRD version must not "+
					"withdraw a class that provisions", got.Status, metav1.ConditionTrue)
			}
		})
	}
}

// A cluster we cannot read is not a cluster we can vouch for. Publishing True
// here would be the Accepted=False bug pointed the other way: a claim about
// something never checked.
func TestClassReconcileReportsUnreadableCRDs(t *testing.T) {
	boom := errors.New("customresourcedefinitions is forbidden")
	class := gatewayClass(consts.ProviderTunnelPizza, ControllerName)

	c := fake.NewClientBuilder().
		WithScheme(scheme()).
		WithObjects(append(gatewayCRDs(versionSupported), class)...).
		WithStatusSubresource(&gatewayv1.GatewayClass{}).
		WithInterceptorFuncs(interceptor.Funcs{
			List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList,
				opts ...client.ListOption) error {
				if _, ok := list.(*metav1.PartialObjectMetadataList); ok {
					return boom
				}
				return cl.List(ctx, list, opts...)
			},
		}).
		Build()
	r := &ClassReconciler{Client: c, CRDs: c}

	// The error comes back so the reconcile is retried with backoff, and the
	// condition corrects itself when the read succeeds.
	if _, err := r.Reconcile(context.Background(), classRequest(consts.ProviderTunnelPizza)); !errors.Is(err, boom) {
		t.Fatalf("Reconcile() error = %v, want it to wrap %v", err, boom)
	}

	cond := supportedVersionCondition(t, c, consts.ProviderTunnelPizza)
	if cond.Status != metav1.ConditionFalse {
		t.Errorf("SupportedVersion status = %q, want %q when the CRDs could not be read",
			cond.Status, metav1.ConditionFalse)
	}
	if !strings.Contains(cond.Message, "forbidden") {
		t.Errorf("SupportedVersion message %q does not say why the check failed", cond.Message)
	}

	// And the class is still accepted: we could not read the CRDs, but the
	// Gateways on this class are being served regardless.
	if got := acceptedCondition(t, c, consts.ProviderTunnelPizza); got.Status != metav1.ConditionTrue {
		t.Errorf("Accepted status = %q, want %q", got.Status, metav1.ConditionTrue)
	}
}
