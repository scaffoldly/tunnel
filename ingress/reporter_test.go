package ingress

import (
	"testing"

	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/scaffoldly/tunnel/consts"
)

// TestReporterNameIsAcceptedByTheAPIServer runs the exact check the API server
// applies to an Event's reportingController.
//
// This is the one that bit: passing ControllerName straight to
// GetEventRecorder produced "github.com/scaffoldly/tunnel/ingress", which has
// too many slashes to be a qualified name, so every event was rejected with
// "will not retry" and nothing appeared under `kubectl describe ingress`. A
// fake client never validates it, so only a real cluster — or this test —
// catches it.
func TestReporterNameIsAcceptedByTheAPIServer(t *testing.T) {
	if errs := validation.IsQualifiedName(ReporterName); len(errs) > 0 {
		t.Fatalf("ReporterName %q is not a qualified name: %v", ReporterName, errs)
	}
	if want := "tunnel.scaffoldly.github.com/ingress"; ReporterName != want {
		t.Errorf("ReporterName = %q, want %q", ReporterName, want)
	}
	// The controller identity itself is unchanged: it is still the import path
	// the IngressClass names, and the reporter is a separate derivation.
	if errs := validation.IsQualifiedName(ControllerName); len(errs) == 0 {
		t.Error("ControllerName is now a valid qualified name; the reporter derivation may be redundant")
	}
}

func TestReporter(t *testing.T) {
	tests := []struct{ in, want string }{
		{"github.com/scaffoldly/tunnel/ingress", "tunnel.scaffoldly.github.com/ingress"},
		{"github.com/scaffoldly/tunnel/gateway", "tunnel.scaffoldly.github.com/gateway"},
		{"example.com/thing", "example.com/thing"},
		// Nothing to derive a domain from; pass it through rather than emit a
		// leading slash.
		{"ingress", "ingress"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := consts.Reporter(tt.in); got != tt.want {
			t.Errorf("Reporter(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
