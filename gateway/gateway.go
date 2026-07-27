// Package gateway implements the Gateway API half of the tunnel controller.
//
// ingress-nginx reached end of life in March 2026 and the ecosystem is moving
// here, so this is the primary surface rather than an afterthought — package
// ingress remains supported alongside it, not instead of it.
//
// The package path is the contract: a GatewayClass claims this controller by
// setting spec.controllerName to ControllerName, which is this package's
// import path.
package gateway

import (
	"context"
	"errors"
	"fmt"
	"path"
	"reflect"
	"time"

	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/scaffoldly/tunnel/config"
	"github.com/scaffoldly/tunnel/consts"
	"github.com/scaffoldly/tunnel/tunnels"
)

// ControllerName is the value a GatewayClass must carry in spec.controllerName
// to be handled here: this package's import path, read from the type system
// rather than written down, so moving or renaming the package updates the
// contract instead of silently breaking it.
var ControllerName = gatewayv1.GatewayController(reflect.TypeFor[Reconciler]().PkgPath())

// Name is the short label for this controller, used in logs and probes.
var Name = path.Base(string(ControllerName))

// ReporterName is the identity this controller's events are attributed to.
//
// Not ControllerName: the API server rejects an import path there, silently
// dropping every event. See consts.Reporter.
var ReporterName = consts.Reporter(string(ControllerName))

// New registers the Gateway API controllers with mgr.
//
// Registering nothing is a valid outcome: Gateway API CRDs are not installed
// on every cluster, and a manager that watches a kind the API server does not
// serve fails to start outright. An Ingress-only cluster gets a log line
// instead of a crash loop.
func New(mgr ctrl.Manager, cfg config.Config) error {
	// Before the probe, not after: the probe is what decides whether anything
	// registers, and a Runnable would not run until the manager had already
	// started without these watches.
	//
	// Its own client for the same reason the class installer has one — the
	// manager's reads go through a cache that has not synced at setup time.
	if cfg.InstallGatewayAPI {
		c, err := client.New(mgr.GetConfig(), client.Options{Scheme: scheme()})
		if err != nil {
			return fmt.Errorf("build crd client: %w", err)
		}
		if err := installCRDs(context.Background(), c); err != nil {
			return fmt.Errorf("install gateway api crds: %w", err)
		}
		if err := awaitEstablished(context.Background(), c); err != nil {
			return fmt.Errorf("await gateway api crds: %w", err)
		}
	}

	ok, err := installed(mgr)
	if err != nil {
		return fmt.Errorf("detect gateway api: %w", err)
	}
	if !ok {
		mgr.GetLogger().Info("gateway api crds not found; gateway controllers disabled")
		return nil
	}

	if err := (&ClassReconciler{Client: mgr.GetClient()}).setup(mgr); err != nil {
		return fmt.Errorf("setup gatewayclass controller: %w", err)
	}

	store := tunnels.NewStore(mgr.GetLogger().WithName(consts.ControllerGateway), tunnels.Dial, consts.TunnelRetryInterval)
	if err := mgr.Add(store); err != nil {
		return fmt.Errorf("add tunnel store: %w", err)
	}

	r := &Reconciler{
		Client:   mgr.GetClient(),
		Services: mgr.GetAPIReader(),
		Recorder: mgr.GetEventRecorder(ReporterName),
		Tunnels:  store,
	}
	if err := r.setup(mgr, store); err != nil {
		return fmt.Errorf("setup gateway controller: %w", err)
	}

	if cfg.InstallGatewayClasses {
		if err := mgr.Add(&installer{cfg: cfg}); err != nil {
			return fmt.Errorf("add gatewayclass installer: %w", err)
		}
	}

	mgr.GetLogger().Info("gateway controllers registered", "controller", ControllerName)
	return nil
}

// installed reports whether the cluster serves the Gateway API group.
func installed(mgr ctrl.Manager) (bool, error) {
	_, err := mgr.GetRESTMapper().RESTMapping(
		schema.GroupKind{Group: gatewayv1.GroupName, Kind: "Gateway"},
		gatewayv1.GroupVersion.Version,
	)
	if err != nil {
		if meta.IsNoMatchError(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// ClassReconciler owns GatewayClass status.
//
// Gateway API requires the implementing controller to publish an Accepted
// condition; a class nobody accepts leaves every Gateway referencing it in
// limbo with no explanation, and a conformant consumer may refuse to use it.
// Gateways naming one of our classes are provisioned, so this accepts.
type ClassReconciler struct {
	client.Client
}

func (r *ClassReconciler) setup(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&gatewayv1.GatewayClass{}).
		Named(consts.ControllerGatewayClass).
		Complete(r)
}

func (r *ClassReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var class gatewayv1.GatewayClass
	if err := r.Get(ctx, req.NamespacedName, &class); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if class.Spec.ControllerName != ControllerName {
		return ctrl.Result{}, nil
	}

	// The spec fixes one reason for Accepted=True, so it is read from the
	// Gateway API's own constants rather than written down here.
	//
	// observedGeneration is not optional: without it a consumer cannot tell
	// this condition from one left over before the last edit to the class.
	// The class's name is the provider, so the message can say where the
	// tunnels it accepts will come from.
	accepted := metav1.Condition{
		Type:               string(gatewayv1.GatewayClassConditionStatusAccepted),
		Status:             metav1.ConditionTrue,
		Reason:             string(gatewayv1.GatewayClassReasonAccepted),
		Message:            fmt.Sprintf(consts.MsgClassAcceptedFmt, class.Name),
		ObservedGeneration: class.Generation,
	}

	if !upsert(&class.Status.Conditions, accepted) {
		return ctrl.Result{}, nil
	}
	if err := r.Status().Update(ctx, &class); err != nil {
		return ctrl.Result{}, fmt.Errorf("update gatewayclass status: %w", err)
	}
	log.FromContext(ctx).Info("gatewayclass accepted", "gatewayclass", class.Name)
	return ctrl.Result{}, nil
}

// Reconciler wires Gateways claimed by one of our GatewayClasses to a tunnel.
type Reconciler struct {
	client.Client
	// Services reads backend Services. Separate from Client because it is the
	// manager's uncached reader — see (*Reconciler).port.
	Services client.Reader
	Recorder events.EventRecorder
	// Tunnels owns the live tunnels; Reconcile only declares what it wants.
	Tunnels *tunnels.Store
}

func (r *Reconciler) setup(mgr ctrl.Manager, store *tunnels.Store) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&gatewayv1.Gateway{}).
		// Routes carry the backends, so a Gateway's origin changes when its
		// routes do without the Gateway itself being touched.
		Watches(&gatewayv1.HTTPRoute{}, handler.EnqueueRequestsFromMapFunc(routeParents)).
		// A tunnel becomes ready seconds after it is asked for and can drop
		// long after that; neither is a change to any object the API server
		// would report.
		WatchesRawSource(source.Channel(store.Source(), &handler.EnqueueRequestForObject{})).
		Named(consts.ControllerGateway).
		Complete(r)
}

// routeParents maps an HTTPRoute to the Gateways it attaches to, so a route
// added, edited, or deleted re-reconciles whatever it points at.
func routeParents(_ context.Context, obj client.Object) []reconcile.Request {
	route, ok := obj.(*gatewayv1.HTTPRoute)
	if !ok {
		return nil
	}
	var out []reconcile.Request
	for _, ref := range route.Spec.ParentRefs {
		if ref.Kind != nil && *ref.Kind != "Gateway" {
			continue
		}
		ns := route.Namespace
		if ref.Namespace != nil {
			ns = string(*ref.Namespace)
		}
		out = append(out, reconcile.Request{
			NamespacedName: types.NamespacedName{Namespace: ns, Name: string(ref.Name)},
		})
	}
	return out
}

func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var gw gatewayv1.Gateway
	if err := r.Get(ctx, req.NamespacedName, &gw); err != nil {
		if apierrors.IsNotFound(err) {
			// Deleted between the event and this read. The tunnel lives in
			// this process, so closing it is the whole teardown.
			r.Tunnels.Forget(req.NamespacedName)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	class, ours, err := r.class(ctx, &gw)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !ours {
		// Someone else's Gateway, or a dangling class. It may have been ours a
		// moment ago, though, and a reclassed Gateway has to give its tunnel
		// back and take our stale address with it.
		if r.Tunnels.Forget(req.NamespacedName) {
			if _, err := r.publish(ctx, &gw, ""); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	origin, err := r.origin(ctx, &gw)
	if err != nil {
		r.Tunnels.Forget(req.NamespacedName)
		if _, clearErr := r.publish(ctx, &gw, ""); clearErr != nil {
			return ctrl.Result{}, clearErr
		}
		if errors.Is(err, errUnsupported) {
			// Nothing to retry: this is the spec, not the weather. A Gateway
			// with no routes yet lands here, which is why it is reported
			// rather than treated as an error.
			logger.Info("gateway not serviceable", "reason", err)
			r.Recorder.Eventf(&gw, nil, consts.EventTypeWarning, consts.ReasonUnsupported,
				consts.ActionProvision, consts.MsgUnsupportedFmt, err)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	provider := class.Name
	status := r.Tunnels.Ensure(req.NamespacedName, class, origin)
	switch status.State {
	case tunnels.Ready:
		changed, err := r.publish(ctx, &gw, status.Hostname)
		if err != nil {
			return ctrl.Result{}, err
		}
		if changed {
			logger.Info("tunnel ready", "provider", provider, "origin", origin.String(),
				"hostname", status.Hostname)
			r.Recorder.Eventf(&gw, nil, consts.EventTypeNormal, consts.ReasonTunnelReady,
				consts.ActionProvision, consts.MsgTunnelReadyFmt, status.Hostname, provider)
		}
		return ctrl.Result{}, nil

	case tunnels.Failed:
		if _, err := r.publish(ctx, &gw, ""); err != nil {
			return ctrl.Result{}, err
		}
		logger.Info("tunnel failed", "provider", provider, "error", status.Err,
			"retryAt", status.RetryAt)
		r.Recorder.Eventf(&gw, nil, consts.EventTypeWarning, consts.ReasonTunnelFailed,
			consts.ActionProvision, consts.MsgTunnelFailedFmt, status.Err)
		return ctrl.Result{RequeueAfter: time.Until(status.RetryAt)}, nil

	default:
		logger.Info("tunnel pending", "provider", provider, "origin", origin.String())
		return ctrl.Result{}, nil
	}
}

// publish writes the tunnel hostname to the Gateway's status, and reports
// whether it had to. An empty hostname clears it.
//
// status.addresses is the Gateway API's equivalent of the Ingress's
// status.loadBalancer — where the implementing controller states the address
// it serves on. Type Hostname because a tunnel has no routable IP.
func (r *Reconciler) publish(ctx context.Context, gw *gatewayv1.Gateway, hostname string) (bool, error) {
	var want []gatewayv1.GatewayStatusAddress
	if hostname != "" {
		want = []gatewayv1.GatewayStatusAddress{{
			Type:  ptr.To(gatewayv1.HostnameAddressType),
			Value: hostname,
		}}
	}

	if apiequality.Semantic.DeepEqual(gw.Status.Addresses, want) {
		return false, nil
	}

	gw.Status.Addresses = want
	if err := r.Status().Update(ctx, gw); err != nil {
		return false, fmt.Errorf("update gateway status: %w", err)
	}
	return true, nil
}

// class resolves the GatewayClass this Gateway asks for, and reports whether
// it is ours. Same rule as the Ingress half: a class is named for the host it
// mints from, so choosing a class is the whole choice.
//
// This is what gets handed to libtunnel:
//
//	libtunnel.Cloudflare().WithProvider(provider)
func (r *Reconciler) class(ctx context.Context, gw *gatewayv1.Gateway) (*gatewayv1.GatewayClass, bool, error) {
	name := string(gw.Spec.GatewayClassName)
	if name == "" {
		return nil, false, nil
	}

	var class gatewayv1.GatewayClass
	if err := r.Get(ctx, client.ObjectKey{Name: name}, &class); err != nil {
		if apierrors.IsNotFound(err) {
			// Dangling class reference. The Gateway is inert until the class
			// exists, and it is not ours to complain about.
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("get gatewayclass %q: %w", name, err)
	}

	if class.Spec.ControllerName != ControllerName {
		return nil, false, nil
	}

	return &class, true, nil
}

// upsert reports whether it changed conditions, so an unchanged status does not
// trigger a write and another reconcile.
func upsert(conditions *[]metav1.Condition, next metav1.Condition) bool {
	for i, existing := range *conditions {
		if existing.Type != next.Type {
			continue
		}
		if existing.Status == next.Status &&
			existing.Reason == next.Reason &&
			existing.Message == next.Message &&
			existing.ObservedGeneration == next.ObservedGeneration {
			return false
		}
		next.LastTransitionTime = existing.LastTransitionTime
		if existing.Status != next.Status {
			next.LastTransitionTime = metav1.Now()
		}
		(*conditions)[i] = next
		return true
	}
	next.LastTransitionTime = metav1.Now()
	*conditions = append(*conditions, next)
	return true
}
