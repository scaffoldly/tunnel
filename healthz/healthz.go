// Package healthz registers the manager's liveness probe.
//
// Liveness answers "is this process wedged?" — a failure gets the pod
// restarted. Ping is the right check for that and deliberately shallow: a
// liveness probe that fails because the API server is briefly unreachable
// restarts every controller in the cluster at exactly the wrong moment.
// Readiness is where dependency health belongs.
package healthz

import (
	"fmt"

	ctrl "sigs.k8s.io/controller-runtime"
	runtimehealthz "sigs.k8s.io/controller-runtime/pkg/healthz"

	"github.com/scaffoldly/tunnel/consts"
)

// Name is the probe's path segment, served at /healthz.
const Name = consts.Healthz

// New registers the liveness probe with mgr.
func New(mgr ctrl.Manager) error {
	if err := mgr.AddHealthzCheck(Name, runtimehealthz.Ping); err != nil {
		return fmt.Errorf("add %s check: %w", Name, err)
	}
	return nil
}
