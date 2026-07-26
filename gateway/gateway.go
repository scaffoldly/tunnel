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
	"fmt"
	"path"
	"reflect"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/scaffoldly/tunnel/config"
	"github.com/scaffoldly/tunnel/consts"
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

	r := &Reconciler{
		Client:   mgr.GetClient(),
		Recorder: mgr.GetEventRecorder(ReporterName),
	}
	if err := r.setup(mgr); err != nil {
		return fmt.Errorf("setup gateway controller: %w", err)
	}

	if cfg.Install {
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
// limbo with no explanation. Until provisioning exists this reports
// Accepted=False rather than claiming a capability we do not have.
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

	log.FromContext(ctx).Info("unimplemented", "gatewayclass", class.Name)

	// Waiting is the Gateway API's reason for a class not yet usable, which is
	// exactly the situation: the controller is present but cannot provision.
	meta := metav1.Condition{
		Type:               string(gatewayv1.GatewayClassConditionStatusAccepted),
		Status:             metav1.ConditionFalse,
		Reason:             string(gatewayv1.GatewayClassReasonWaiting),
		Message:            consts.MsgUnimplemented,
		ObservedGeneration: class.Generation,
	}

	if !upsert(&class.Status.Conditions, meta) {
		return ctrl.Result{}, nil
	}
	if err := r.Status().Update(ctx, &class); err != nil {
		return ctrl.Result{}, fmt.Errorf("update gatewayclass status: %w", err)
	}
	return ctrl.Result{}, nil
}

// Reconciler wires Gateways claimed by one of our GatewayClasses to a tunnel.
type Reconciler struct {
	client.Client
	Recorder events.EventRecorder
}

func (r *Reconciler) setup(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&gatewayv1.Gateway{}).
		Named(consts.ControllerGateway).
		Complete(r)
}

func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var gw gatewayv1.Gateway
	if err := r.Get(ctx, req.NamespacedName, &gw); err != nil {
		// Deleted between the event and this read. Nothing to undo yet; once
		// tunnels are real this is where they get torn down.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	provider, ours, err := r.provider(ctx, &gw)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !ours {
		return ctrl.Result{}, nil
	}

	log.FromContext(ctx).Info("unimplemented", "provider", provider, "gateway", req.NamespacedName)

	r.Recorder.Eventf(&gw, nil, consts.EventTypeWarning, consts.ReasonUnimplemented, consts.ActionProvision,
		consts.MsgUnimplementedFmt, provider)

	// status.addresses stays empty on purpose. Publishing an address we cannot
	// actually serve would be worse than publishing nothing.
	return ctrl.Result{}, nil
}

// provider reports whether this Gateway is ours and, if so, which host to mint
// tunnels from. Same rule as the Ingress half: a GatewayClass is named for the
// host it mints from, so choosing a class is the whole choice.
//
// This is what gets handed to libtunnel:
//
//	libtunnel.Cloudflare().WithProvider(provider)
func (r *Reconciler) provider(ctx context.Context, gw *gatewayv1.Gateway) (string, bool, error) {
	name := string(gw.Spec.GatewayClassName)
	if name == "" {
		return "", false, nil
	}

	var class gatewayv1.GatewayClass
	if err := r.Get(ctx, client.ObjectKey{Name: name}, &class); err != nil {
		if apierrors.IsNotFound(err) {
			// Dangling class reference. The Gateway is inert until the class
			// exists, and it is not ours to complain about.
			return "", false, nil
		}
		return "", false, fmt.Errorf("get gatewayclass %q: %w", name, err)
	}

	if class.Spec.ControllerName != ControllerName {
		return "", false, nil
	}

	return class.Name, true, nil
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
