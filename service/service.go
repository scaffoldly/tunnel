package service

import (
	"context"
	"errors"
	"fmt"
	"path"
	"reflect"
	"slices"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/scaffoldly/tunnel/config"
	"github.com/scaffoldly/tunnel/consts"
)

// ControllerName is this package's import path, read from the type system for
// the same reason the other halves do it.
//
// Unlike ingress and gateway, nothing in a user's cluster names this string:
// there is no ServiceClass, and the provider comes from the annotation prefix
// or spec.loadBalancerClass. It exists to derive ReporterName, so events from
// this controller are attributable to the package that emitted them.
var ControllerName = reflect.TypeFor[Reconciler]().PkgPath()

// Name is the short label for this controller, used in logs.
var Name = path.Base(ControllerName)

// ReporterName is the identity this controller's events are attributed to.
// Not ControllerName: the API server rejects an import path there. See
// consts.Reporter.
var ReporterName = consts.Reporter(ControllerName)

// Reconciler turns a Service that asks for a tunnel into the child Ingress
// that gets it one.
type Reconciler struct {
	client.Client
	// Services reads the Service being reconciled. The manager's *uncached*
	// reader, deliberately: the watch below is metadata-only, and reading the
	// full Service through the cached client would start a second, structured
	// informer over every Service in the cluster — exactly the cost the
	// metadata watch exists to avoid, and controller-runtime warns that the
	// two caches then race.
	Services client.Reader
	Recorder events.EventRecorder
	// Probe reports how an origin speaks when the Service did not say. Best
	// effort and injectable: the production one opens a socket, which no unit
	// test should.
	Probe Prober
	// Providers is the vocabulary a trigger may name. Injected rather than
	// read from consts here so the resolution stays testable against a fixed
	// set, and so widening it later is a change at one call site.
	Providers []string
}

// New registers the Service controller with mgr.
func New(mgr ctrl.Manager, _ config.Config) error {
	r := &Reconciler{
		Client:    mgr.GetClient(),
		Services:  mgr.GetAPIReader(),
		Recorder:  mgr.GetEventRecorder(ReporterName),
		Probe:     Probe,
		Providers: consts.InstalledProviders,
	}

	if err := ctrl.NewControllerManagedBy(mgr).
		// Metadata only. There is no field selector for "has an annotation
		// whose name half is tunnel", so every Service in the cluster has to
		// be watched; caching their specs as well would put the whole Service
		// inventory in this process's heap, which is what the Ingress and
		// Gateway halves already refuse by reading Services through the
		// uncached reader. PartialObjectMetadata keeps the cache proportional
		// to the number of Services rather than their size.
		WatchesMetadata(&corev1.Service{}, &handler.EnqueueRequestForObject{}).
		// The child publishes the hostname, and it appears seconds after the
		// child is created. Without this the loadBalancerClass path would
		// never see it: nothing touches the Service at that moment.
		//
		// Not Owns(), which the builder only accepts alongside For().
		Watches(&networkingv1.Ingress{},
			handler.EnqueueRequestForOwner(mgr.GetScheme(), mgr.GetRESTMapper(), &corev1.Service{},
				handler.OnlyControllerOwner())).
		Named(consts.ControllerService).
		Complete(r); err != nil {
		return fmt.Errorf("setup service controller: %w", err)
	}

	mgr.GetLogger().Info("service controller registered", "controller", ControllerName)
	return nil
}

// Reconcile brings the child Ingresses of one Service into line with what the
// Service asks for, and — on the loadBalancerClass path only — copies the
// resulting hostname into the Service's status.
//
// Every Service in the cluster arrives here, because the metadata watch cannot
// tell which ones carry spec.loadBalancerClass. See the note on Services for
// why the read is uncached, and the handoff for the alternatives.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var svc corev1.Service
	if err := r.Services.Get(ctx, req.NamespacedName, &svc); err != nil {
		if apierrors.IsNotFound(err) {
			// Children carry an ownerReference to the Service, so the API
			// server's garbage collector removes them. Nothing to undo here,
			// and no finalizer: the tunnel lives in the controller process and
			// dies with the child object.
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// A Service on its way out keeps its children until GC takes them; racing
	// the collector would only produce conflicts.
	if svc.DeletionTimestamp != nil {
		return ctrl.Result{}, nil
	}

	wanted, err := providers(&svc, r.Providers)
	if err != nil {
		// Unsupported by construction — providers reads one object and reaches
		// nothing else, so there is no version of this that succeeds on retry.
		//
		// The children go with it. A Service whose triggers no longer resolve
		// is not asking for the tunnels it used to, and leaving a child
		// running would publish a hostname that nothing on the Service asks
		// for any more. The event says why.
		logger.Info("service not serviceable", "reason", err)
		if err := r.prune(ctx, &svc, nil); err != nil {
			return ctrl.Result{}, err
		}
		if _, err := r.publish(ctx, &svc, ""); err != nil {
			return ctrl.Result{}, err
		}
		r.Recorder.Eventf(&svc, nil, consts.EventTypeWarning, consts.ReasonUnsupported,
			consts.ActionProvision, consts.MsgUnsupportedFmt, err)
		return ctrl.Result{}, nil
	}

	hostnames, retry, err := r.reconcileChildren(ctx, &svc, wanted)
	if err != nil {
		return ctrl.Result{}, err
	}

	// The status write is the loadBalancerClass path's alone. An annotated
	// Service is left exactly as the user wrote it — no status, no
	// annotations — which is both the decision and the only thing the API
	// server would accept: a status.loadBalancer write on a Service that is
	// not type LoadBalancer is rejected outright.
	provider, ok := statusProvider(&svc, r.Providers)
	if !ok {
		return ctrl.Result{RequeueAfter: retry}, nil
	}
	changed, err := r.publish(ctx, &svc, hostnames[provider])
	if err != nil {
		return ctrl.Result{}, err
	}
	if changed && hostnames[provider] != "" {
		logger.Info("published tunnel hostname to service status",
			"provider", provider, "hostname", hostnames[provider])
	}
	return ctrl.Result{RequeueAfter: retry}, nil
}

// reconcileChildren ensures one child per wanted provider, removes the rest,
// and reports the hostname each child currently publishes.
//
// The second return is how long to wait before trying again, and is non-zero
// only when an origin could not be reached to determine how it speaks. Nothing
// else would bring us back: a backend becoming ready is not an event on the
// Service or on its child.
func (r *Reconciler) reconcileChildren(ctx context.Context, svc *corev1.Service, wanted []resolved) (map[string]string, time.Duration, error) {
	logger := log.FromContext(ctx)
	hostnames := make(map[string]string, len(wanted))
	keep := make(map[string]struct{}, len(wanted))
	var retry time.Duration

	for _, want := range wanted {
		if want.api != apiIngress {
			// Phase 3. Refused rather than quietly served through the Ingress
			// branch: a user who asked for Gateway semantics and silently got
			// Ingress ones has been lied to.
			r.Recorder.Eventf(svc, nil, consts.EventTypeWarning, consts.ReasonUnsupported,
				consts.ActionProvision, consts.MsgUnsupportedFmt,
				fmt.Errorf("%w: the %q API is not implemented yet; set %s/%s: %q to use the Ingress branch",
					consts.ErrUnsupported, want.api, want.provider, annotationTunnel, apiIngress))
			continue
		}

		// Only when the Service did not say. An explicit annotation or
		// appProtocol is a statement of intent, and probing past it would let
		// a momentarily-wrong backend override its own author.
		if !want.declared {
			address := originAddress(svc.Namespace, svc.Name, want.port.number)
			switch scheme, err := r.Probe(ctx, address); {
			case err != nil:
				logger.Info("could not determine origin protocol", "address", address, "error", err)
				r.Recorder.Eventf(svc, nil, consts.EventTypeWarning, consts.ReasonProtocol,
					consts.ActionProvision, consts.MsgProtocolUnknownFmt,
					address, err, want.protocol, want.provider, consts.ProtocolAnnotation)
				retry = consts.TunnelRetryInterval
			case scheme != want.protocol:
				logger.Info("detected origin protocol", "address", address, "protocol", scheme)
				r.Recorder.Eventf(svc, nil, consts.EventTypeNormal, consts.ReasonProtocol,
					consts.ActionProvision, consts.MsgProtocolProbedFmt,
					address, scheme, want.provider, consts.ProtocolAnnotation)
				want.protocol = scheme
			}
		}

		child, err := r.ensure(ctx, svc, want)
		if err != nil {
			if errors.Is(err, consts.ErrUnsupported) {
				r.Recorder.Eventf(svc, nil, consts.EventTypeWarning, consts.ReasonUnsupported,
					consts.ActionProvision, consts.MsgUnsupportedFmt, err)
				continue
			}
			return nil, 0, err
		}

		keep[child.Name] = struct{}{}
		hostnames[want.provider] = childHostname(child)

		if hostnames[want.provider] != "" {
			r.Recorder.Eventf(svc, nil, consts.EventTypeNormal, consts.ReasonTunnelReady,
				consts.ActionProvision, consts.MsgTunnelReadyFmt, hostnames[want.provider], want.provider)
		}
	}

	if err := r.prune(ctx, svc, keep); err != nil {
		return nil, 0, err
	}
	return hostnames, retry, nil
}

// statusProvider reports which provider's hostname belongs in this Service's
// status, if any.
//
// Only the loadBalancerClass path, and only for a class this controller serves.
// A Service that merely carries the annotation is never written to.
func statusProvider(svc *corev1.Service, known []string) (string, bool) {
	if svc.Spec.Type != corev1.ServiceTypeLoadBalancer || svc.Spec.LoadBalancerClass == nil {
		return "", false
	}
	class := *svc.Spec.LoadBalancerClass
	if !slices.Contains(known, class) {
		return "", false
	}
	return class, true
}

// childHostname reads the address a child Ingress currently publishes, which
// is where the Ingress half writes the tunnel's hostname.
func childHostname(ing *networkingv1.Ingress) string {
	for _, in := range ing.Status.LoadBalancer.Ingress {
		if in.Hostname != "" {
			return in.Hostname
		}
	}
	return ""
}
