// Package readyz registers the manager's readiness probe.
//
// Readiness answers "should this replica be doing work?" — a failure removes
// the pod from service without restarting it. Ping only reports that the
// process is up, which is the same thing liveness already says.
//
// The honest check is whether the caches have synced: a manager whose informers
// are still filling will reconcile against an incomplete view of the cluster.
// controller-runtime exposes no non-blocking way to ask, so that is left for
// when it matters — with one replica and no traffic routed here, an early
// ready is harmless.
package readyz

import (
	"fmt"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"

	"github.com/scaffoldly/tunnel/consts"
)

// Name is the probe's path segment, served at /readyz.
const Name = consts.Readyz

// New registers the readiness probe with mgr.
func New(mgr ctrl.Manager) error {
	if err := mgr.AddReadyzCheck(Name, healthz.Ping); err != nil {
		return fmt.Errorf("add %s check: %w", Name, err)
	}
	return nil
}
