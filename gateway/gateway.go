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
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/tools/events"
	"k8s.io/utils/ptr"
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
		Recorder: mgr.GetEventRecorder(string(ControllerName)),
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

// installer creates the GatewayClass naming this controller once the manager
// starts. Only reached when the Gateway API CRDs exist, since New returns
// early otherwise.
//
// A Runnable rather than setup-time work because it needs a live connection,
// and it builds its own client rather than using the manager's: the manager's
// reads go through a cache that has not synced yet at this point.
type installer struct {
	cfg config.Config
}

func (i *installer) Start(ctx context.Context) error {
	logger := log.FromContext(ctx).WithValues("gatewayclass", consts.DefaultProvider)

	c, err := client.New(ctrl.GetConfigOrDie(), client.Options{Scheme: scheme()})
	if err != nil {
		return fmt.Errorf("build client: %w", err)
	}

	class := &gatewayv1.GatewayClass{
		ObjectMeta: metav1.ObjectMeta{Name: consts.DefaultProvider},
		Spec: gatewayv1.GatewayClassSpec{
			ControllerName: ControllerName,
			Description:    ptr.To(consts.GatewayClassDescription),
		},
	}

	err = c.Create(ctx, class)
	switch {
	case err == nil:
		logger.Info("created gatewayclass")
		return nil
	case apierrors.IsAlreadyExists(err):
		// spec.controllerName is immutable, so a class owned by someone else
		// is a real conflict rather than a no-op. Report it instead of
		// pretending the install succeeded.
		var existing gatewayv1.GatewayClass
		if err := c.Get(ctx, client.ObjectKey{Name: consts.DefaultProvider}, &existing); err != nil {
			return fmt.Errorf("get existing gatewayclass: %w", err)
		}
		if existing.Spec.ControllerName != ControllerName {
			logger.Info("gatewayclass exists but names a different controller; leaving it alone",
				"controller", existing.Spec.ControllerName)
			return nil
		}
		logger.Info("gatewayclass already present")
		return nil
	default:
		return fmt.Errorf("create gatewayclass: %w", err)
	}
}

// scheme carries the Gateway API types the installer's own client needs; the
// manager's scheme is not reachable from a Runnable.
func scheme() *runtime.Scheme {
	s := runtime.NewScheme()
	utilruntime.Must(gatewayv1.Install(s))
	return s
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
// tunnels from. Same cascade as the Ingress half: the Gateway's own
// annotation, then its class's, then DefaultProvider.
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

	if p := gw.Annotations[consts.AnnotationProvider]; p != "" {
		return p, true, nil
	}
	if p := class.Annotations[consts.AnnotationProvider]; p != "" {
		return p, true, nil
	}
	return consts.DefaultProvider, true, nil
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
