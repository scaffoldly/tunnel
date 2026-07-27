// Package ingress implements the Ingress half of the tunnel controller.
//
// The package path is the contract: an IngressClass claims this controller by
// setting spec.controller to ControllerName, which is this package's import
// path. The Gateway API half lives in package gateway alongside it.
//
// What it does, per claimed Ingress: resolve the provider host from the
// annotation cascade, resolve the backend Service to a local origin URL, ask
// libtunnel for a tunnel from that provider to that origin, and publish the
// public hostname to status.loadBalancer.ingress[].hostname once the tunnel
// is reachable end to end.
//
// The tunnel is held in this process — libtunnel runs the cloudflared engine
// in-process — so requests arrive here and are proxied to the Service. That
// makes the tunnel's lifetime the controller's lifetime: nothing is left in
// the cluster to clean up on delete (hence no finalizer), and a restart mints
// a new tunnel with a new hostname.
package ingress

import (
	"context"
	"errors"
	"fmt"
	"path"
	"reflect"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"github.com/scaffoldly/tunnel/config"
	"github.com/scaffoldly/tunnel/consts"
)

// ControllerName is the value an IngressClass must carry in spec.controller to
// be handled here: this package's import path, read from the type system
// rather than written down, so moving or renaming the package updates the
// contract instead of silently breaking it.
var ControllerName = reflect.TypeFor[Reconciler]().PkgPath()

// Name is the short label for this controller, used in logs and probes.
var Name = path.Base(ControllerName)

// ReporterName is the identity this controller's events are attributed to.
//
// Not ControllerName: the API server rejects an import path there, silently
// dropping every event. See consts.Reporter.
var ReporterName = consts.Reporter(ControllerName)

// Reconciler wires Ingresses claimed by one of our IngressClasses to a tunnel.
type Reconciler struct {
	client.Client
	// Services reads backend Services. Separate from Client because it is the
	// manager's uncached reader — see (*Reconciler).port.
	Services client.Reader
	Recorder events.EventRecorder
	// Tunnels owns the live tunnels; Reconcile only declares what it wants.
	Tunnels *store
}

// New registers the Ingress controller with mgr.
//
// Ingress is served by every cluster, so unlike the Gateway API half there is
// no capability to probe for first.
func New(mgr ctrl.Manager, cfg config.Config) error {
	tunnels := newStore(mgr.GetLogger().WithName(consts.ControllerIngress), dial, consts.TunnelRetryInterval)
	if err := mgr.Add(tunnels); err != nil {
		return fmt.Errorf("add tunnel store: %w", err)
	}

	r := &Reconciler{
		Client:   mgr.GetClient(),
		Services: mgr.GetAPIReader(),
		Recorder: mgr.GetEventRecorder(ReporterName),
		Tunnels:  tunnels,
	}

	if err := ctrl.NewControllerManagedBy(mgr).
		For(&networkingv1.Ingress{}).
		// A tunnel becomes ready seconds after it is asked for, and can drop
		// long after that. Both are changes the Ingress's status has to
		// follow, and neither is a change to any object the API server would
		// tell us about — so the store wakes us directly instead of the
		// controller polling every pending Ingress on a timer.
		WatchesRawSource(source.Channel(tunnels.source(), &handler.EnqueueRequestForObject{})).
		Named(consts.ControllerIngress).
		Complete(r); err != nil {
		return fmt.Errorf("setup ingress controller: %w", err)
	}

	if cfg.InstallIngressClasses {
		if err := mgr.Add(&installer{cfg: cfg}); err != nil {
			return fmt.Errorf("add ingressclass installer: %w", err)
		}
	}

	mgr.GetLogger().Info("ingress controller registered", "controller", ControllerName)
	return nil
}

func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var ing networkingv1.Ingress
	if err := r.Get(ctx, req.NamespacedName, &ing); err != nil {
		if apierrors.IsNotFound(err) {
			// Deleted between the event and this read. The tunnel lives in
			// this process, so closing it is the whole teardown — nothing
			// survives in the cluster to need a finalizer.
			r.Tunnels.forget(req.NamespacedName)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	class, ours, err := r.class(ctx, &ing)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !ours {
		// Another controller's Ingress, or no class at all. We are not a
		// default class, so an unclassed Ingress is never ours. It may have
		// been ours a moment ago, though — a reclassed Ingress has to give its
		// tunnel back, and take our stale hostname off its status with it.
		//
		// Only if we were actually serving it: an Ingress that was never ours
		// owns its own status, and writing to it would fight whichever
		// controller does.
		if r.Tunnels.forget(req.NamespacedName) {
			if _, err := r.publish(ctx, &ing, ""); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	origin, err := r.origin(ctx, &ing)
	if err != nil {
		r.Tunnels.forget(req.NamespacedName)
		if _, clearErr := r.publish(ctx, &ing, ""); clearErr != nil {
			return ctrl.Result{}, clearErr
		}
		if errors.Is(err, errUnsupported) {
			// Nothing to retry: this is the spec, not the weather. Editing the
			// Ingress brings us straight back here.
			logger.Info("ingress not serviceable", "reason", err)
			r.Recorder.Eventf(&ing, nil, consts.EventTypeWarning, consts.ReasonUnsupported,
				consts.ActionProvision, consts.MsgUnsupportedFmt, err)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	provider := class.Name
	status := r.Tunnels.ensure(req.NamespacedName, class, origin)
	switch status.state {
	case tunnelReady:
		changed, err := r.publish(ctx, &ing, status.hostname)
		if err != nil {
			return ctrl.Result{}, err
		}
		if changed {
			logger.Info("tunnel ready", "provider", provider, "origin", origin.String(),
				"hostname", status.hostname)
			// Ingress has no conditions field, so beyond status.loadBalancer an
			// Event is the only place this can surface. `kubectl describe
			// ingress` shows it.
			r.Recorder.Eventf(&ing, nil, consts.EventTypeNormal, consts.ReasonTunnelReady,
				consts.ActionProvision, consts.MsgTunnelReadyFmt, status.hostname, provider)
		}
		return ctrl.Result{}, nil

	case tunnelFailed:
		// Stop advertising a hostname that no longer serves, and wait out the
		// cooldown before minting a replacement.
		if _, err := r.publish(ctx, &ing, ""); err != nil {
			return ctrl.Result{}, err
		}
		logger.Info("tunnel failed", "provider", provider, "error", status.err,
			"retryAt", status.retryAt)
		r.Recorder.Eventf(&ing, nil, consts.EventTypeWarning, consts.ReasonTunnelFailed,
			consts.ActionProvision, consts.MsgTunnelFailedFmt, status.err)
		return ctrl.Result{RequeueAfter: time.Until(status.retryAt)}, nil

	default:
		// Minting or connecting. No requeue: the store wakes us when the
		// hostname is real. Publishing one before it resolves would advertise
		// an address that does not answer.
		logger.Info("tunnel pending", "provider", provider, "origin", origin.String())
		return ctrl.Result{}, nil
	}
}

// publish writes the tunnel hostname to the Ingress's status, and reports
// whether it had to. An empty hostname clears it.
//
// status.loadBalancer.ingress[].hostname is where an ingress controller states
// the address it serves the Ingress on — it is what `kubectl get ingress`
// prints under ADDRESS — so it is the one place a tunnel URL belongs.
func (r *Reconciler) publish(ctx context.Context, ing *networkingv1.Ingress, hostname string) (bool, error) {
	var want []networkingv1.IngressLoadBalancerIngress
	if hostname != "" {
		want = []networkingv1.IngressLoadBalancerIngress{{
			Hostname: hostname,
			// No IP: a tunnel has no address to route to.
			//
			// Both ports, because the edge answers on both. Cloudflare quick
			// tunnels serve plaintext on 80 and we match them deliberately, so
			// a hostname behaves the same whichever provider minted it. A zone
			// that redirects 80 to 443 still answers on 80 to issue it.
			Ports: []networkingv1.IngressPortStatus{
				{
					Port:     80,
					Protocol: corev1.ProtocolTCP,
				},
				{
					Port:     443,
					Protocol: corev1.ProtocolTCP,
				},
			},
		}}
	}

	// DeepEqual rather than comparing the fields we happen to care about: a
	// hand-written check silently stops noticing whatever the struct grows
	// next, and skipping the write leaves a stale status behind with no error
	// to say so.
	if apiequality.Semantic.DeepEqual(ing.Status.LoadBalancer.Ingress, want) {
		return false, nil
	}

	ing.Status.LoadBalancer.Ingress = want
	if err := r.Status().Update(ctx, ing); err != nil {
		return false, fmt.Errorf("update ingress status: %w", err)
	}
	return true, nil
}

// class resolves the IngressClass this Ingress asks for, and reports whether
// it is ours.
//
// The class is the whole configuration: its name is the provider host the
// tunnel is minted from, so `kubectl get ingressclass` reads as the list of
// providers this cluster can reach. Nothing else selects one — an Ingress
// picks a class, and that is the choice.
//
// This is what ends up handed to libtunnel:
//
//	libtunnel.Cloudflare().WithProvider(class.Name)
func (r *Reconciler) class(ctx context.Context, ing *networkingv1.Ingress) (*networkingv1.IngressClass, bool, error) {
	name := ing.Spec.IngressClassName
	if name == nil || *name == "" {
		return nil, false, nil
	}

	var class networkingv1.IngressClass
	if err := r.Get(ctx, client.ObjectKey{Name: *name}, &class); err != nil {
		if apierrors.IsNotFound(err) {
			// Dangling class reference. The Ingress is inert until the class
			// exists, and it is not ours to complain about.
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("get ingressclass %q: %w", *name, err)
	}

	if class.Spec.Controller != ControllerName {
		return nil, false, nil
	}

	return &class, true, nil
}
