package gateway

import (
	"testing"

	"k8s.io/apimachinery/pkg/util/validation"
)

// TestReporterNameIsAcceptedByTheAPIServer runs the exact check the API server
// applies to an Event's reportingController.
//
// Same bug as package ingress had: passing ControllerName straight to
// GetEventRecorder produced "github.com/scaffoldly/tunnel/gateway", which has
// too many slashes to be a qualified name, so every event was rejected with
// "will not retry" and nothing appeared under `kubectl describe gateway`. A
// fake client never validates it, so only a real cluster — or this test —
// catches it.
func TestReporterNameIsAcceptedByTheAPIServer(t *testing.T) {
	if errs := validation.IsQualifiedName(ReporterName); len(errs) > 0 {
		t.Fatalf("ReporterName %q is not a qualified name: %v", ReporterName, errs)
	}
	if want := "tunnel.scaffoldly.github.com/gateway"; ReporterName != want {
		t.Errorf("ReporterName = %q, want %q", ReporterName, want)
	}
	// The controller identity itself is unchanged: it is still the import path
	// the GatewayClass names, and the reporter is a separate derivation.
	if errs := validation.IsQualifiedName(string(ControllerName)); len(errs) == 0 {
		t.Error("ControllerName is now a valid qualified name; the reporter derivation may be redundant")
	}
}

// TestControllerNameIsThePackagePath guards the contract nothing else checks:
// every GatewayClass this controller created names this string in
// spec.controllerName, and a package move would
// change it silently on every cluster that already installed us.
func TestControllerNameIsThePackagePath(t *testing.T) {
	if want := "github.com/scaffoldly/tunnel/gateway"; string(ControllerName) != want {
		t.Fatalf("ControllerName = %q, want %q", ControllerName, want)
	}
	if Name != "gateway" {
		t.Errorf("Name = %q, want %q", Name, "gateway")
	}
}
