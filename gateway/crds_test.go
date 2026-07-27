package gateway

import (
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func crd(version, channel string) *apiextensionsv1.CustomResourceDefinition {
	ann := map[string]string{}
	if version != "" {
		ann[annotationBundleVersion] = version
	}
	if channel != "" {
		ann[annotationChannel] = channel
	}
	return &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "gateways.gateway.networking.k8s.io", Annotations: ann},
	}
}

// TestDecide is upstream's ruleset for bundling Gateway API CRDs. The CRDs are
// shared cluster-wide, so a wrong "yes" here does not break this controller —
// it breaks whatever other implementation is reading the same definitions.
func TestDecide(t *testing.T) {
	tests := []struct {
		name     string
		existing *apiextensionsv1.CustomResourceDefinition
		ours     *apiextensionsv1.CustomResourceDefinition
		want     bool
	}{
		{
			name:     "older same channel upgrades",
			existing: crd("v1.5.0", "standard"),
			ours:     crd("v1.6.1", "standard"),
			want:     true,
		},
		{
			// Rule 1: never overwrite a newer version.
			name:     "newer is left alone",
			existing: crd("v1.7.0", "standard"),
			ours:     crd("v1.6.1", "standard"),
			want:     false,
		},
		{
			// Equal is not older. Rewriting churns resourceVersion and would
			// make every controller restart look like a CRD change.
			name:     "equal is left alone",
			existing: crd("v1.6.1", "standard"),
			ours:     crd("v1.6.1", "standard"),
			want:     false,
		},
		{
			// Rule 2: experimental carries fields standard does not, so
			// overwriting it would silently drop data from live objects.
			name:     "different channel is left alone even when older",
			existing: crd("v1.5.0", "experimental"),
			ours:     crd("v1.6.1", "standard"),
			want:     false,
		},
		{
			// Rule 1: unrecognized. Something installed these without saying
			// what they are.
			name:     "unparseable version is left alone",
			existing: crd("nightly", "standard"),
			ours:     crd("v1.6.1", "standard"),
			want:     false,
		},
		{
			name:     "missing version annotation is left alone",
			existing: crd("", "standard"),
			ours:     crd("v1.6.1", "standard"),
			want:     false,
		},
		{
			// Hand-applied CRDs carry no annotations at all. Channel mismatch
			// catches it first, but either way the answer is no.
			name:     "no annotations at all is left alone",
			existing: crd("", ""),
			ours:     crd("v1.6.1", "standard"),
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, reason := decide(tt.existing, tt.ours)
			if got != tt.want {
				t.Errorf("decide() = %v (%s), want %v", got, reason, tt.want)
			}
			if reason == "" {
				t.Error("decide() gave no reason; the log line is how an operator finds out why")
			}
		})
	}
}

// The bundle is embedded, so a truncated or missing file is a build-time
// problem rather than a runtime one — but the parse still has to yield the
// kinds the controller watches.
func TestParseCRDsFindsWatchedKinds(t *testing.T) {
	got, err := parseCRDs(crdYAML)
	if err != nil {
		t.Fatalf("parseCRDs: %v", err)
	}

	found := map[string]bool{}
	for _, c := range got {
		found[c.Name] = true
		if c.Annotations[annotationChannel] != "standard" {
			t.Errorf("%s: channel = %q, want standard", c.Name, c.Annotations[annotationChannel])
		}
	}

	// The two the manager actually watches. Missing either means the gateway
	// half never registers.
	for _, name := range []string{
		"gatewayclasses.gateway.networking.k8s.io",
		"gateways.gateway.networking.k8s.io",
	} {
		if !found[name] {
			t.Errorf("bundle does not contain %s", name)
		}
	}
}

// The embedded bundle and the compiled types must be the same release, or the
// controller installs a schema it cannot deserialize.
func TestBundleVersionMatchesGoMod(t *testing.T) {
	got, err := parseCRDs(crdYAML)
	if err != nil {
		t.Fatalf("parseCRDs: %v", err)
	}
	for _, c := range got {
		if v := c.Annotations[annotationBundleVersion]; v != bundledVersion {
			t.Errorf("%s: bundle-version = %q, want %q — run `make crds`", c.Name, v, bundledVersion)
		}
	}
}
