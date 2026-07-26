// Package metrics configures the manager's Prometheus endpoint.
//
// Unlike the probes and controllers, metrics cannot be registered after the
// fact — the manager builds its metrics server during construction — so New
// returns options to hand to ctrl.Options rather than taking a manager.
//
// The endpoint is served in the clear and unauthenticated: anything able to
// reach the pod can scrape it. That is the controller-runtime default and it
// is fine for cluster-internal scraping, but it exposes namespace and resource
// names via metric labels. Serving it authenticated means SecureServing plus a
// FilterProvider, which is worth doing before this is exposed beyond the
// cluster.
package metrics

import (
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/scaffoldly/tunnel/consts"
)

// Name is the short label for this component, used in logs.
const Name = consts.Metrics

// New builds the metrics server options bound to addr. "0" disables serving.
func New(addr string) metricsserver.Options {
	return metricsserver.Options{BindAddress: addr}
}
