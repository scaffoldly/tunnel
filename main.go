// Command tunnel runs the controllers that give a cluster public reachability
// through a tunnel provider.
//
// Both halves provision. An Ingress claimed by one of our IngressClasses gets
// a tunnel from its provider to its backend Service, and the public hostname
// is published to status.loadBalancer.ingress[].hostname — what `kubectl get
// ingress` prints under ADDRESS. A Gateway claimed by one of our GatewayClasses
// gets one to whatever its HTTPRoutes name, published to status.addresses, and
// the class reports Accepted. Traffic reaches the origin through this process,
// which holds the edge connection.
package main

import (
	"flag"
	"os"
	"path"
	"runtime/debug"
	"strings"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/scaffoldly/tunnel/config"
	"github.com/scaffoldly/tunnel/consts"
	"github.com/scaffoldly/tunnel/gateway"
	"github.com/scaffoldly/tunnel/healthz"
	"github.com/scaffoldly/tunnel/ingress"
	"github.com/scaffoldly/tunnel/metrics"
	"github.com/scaffoldly/tunnel/pod"
	"github.com/scaffoldly/tunnel/readyz"
	"github.com/scaffoldly/tunnel/service"
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(gatewayv1.Install(scheme))
}

func main() {
	var metricsAddr, probeAddr string
	var leaderElect bool
	var cfg config.Config

	flag.StringVar(&metricsAddr, consts.FlagMetricsAddr, consts.DefaultMetricsAddr, "address the metric endpoint binds to")
	flag.StringVar(&probeAddr, consts.FlagProbeAddr, consts.DefaultProbeAddr, "address the probe endpoint binds to")
	// Two replicas both minting tunnels for one Ingress would leak a tunnel per
	// reconcile, so this must be on before replicas > 1.
	flag.BoolVar(&leaderElect, consts.FlagLeaderElect, false, "enable leader election for controller manager")
	flag.BoolVar(&cfg.InstallIngressClasses, consts.FlagInstallIngressClasses, true,
		"create default IngressClasses on startup")
	flag.BoolVar(&cfg.InstallGatewayClasses, consts.FlagInstallGatewayClasses, true,
		"create default GatewayClasses on startup")
	flag.BoolVar(&cfg.InstallGatewayAPI, consts.FlagInstallGatewayAPI, true,
		"install the Gateway API CRDs when the cluster has none")

	opts := zap.Options{Development: true}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	log := ctrl.Log.WithName("setup")

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metrics.New(metricsAddr),
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         leaderElect,
		LeaderElectionID:       leaderElectionID(),
	})
	if err != nil {
		log.Error(err, "unable to start manager")
		os.Exit(1)
	}

	// Best effort: a component that fails to register is logged and skipped
	// rather than taking down the ones that did. Each New runs its own
	// preflight and decides what it can offer this cluster. A slice rather
	// than a map, so registration and its logging stay in a fixed order.
	for _, c := range []struct {
		name     string
		register func(ctrl.Manager) error
	}{
		{healthz.Name, healthz.New},
		{readyz.Name, readyz.New},
		{ingress.Name, func(m ctrl.Manager) error { return ingress.New(m, cfg) }},
		{gateway.Name, func(m ctrl.Manager) error { return gateway.New(m, cfg) }},
		{service.Name, func(m ctrl.Manager) error { return service.New(m, cfg) }},
		{pod.Name, func(m ctrl.Manager) error { return pod.New(m, cfg) }},
	} {
		if err := c.register(mgr); err != nil {
			log.Error(err, "registration failed", "component", c.name)
		}
	}

	log.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		log.Error(err, "problem running manager")
		os.Exit(1)
	}
}

// leaderElectionID names the Lease the manager contends for. Derived from the
// module path so a fork cannot silently contend for upstream's lease in the
// same cluster. Slashes are not legal in a resource name, hence the swap.
//
// Build info is absent in some test binaries, so fall back to the controller's
// own package path, whose parent is the module.
func leaderElectionID() string {
	module := path.Dir(ingress.ControllerName)
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Path != "" {
		module = info.Main.Path
	}
	return strings.ReplaceAll(module, "/", "-")
}
