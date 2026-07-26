// Package ingress implements the Ingress half of the tunnel controller.
//
// The package path is the contract: an IngressClass claims this controller by
// setting spec.controller to ControllerName, which is this package's import
// path. The Gateway API half lives in package gateway alongside it.
package ingress

import (
	"context"
	"fmt"
	"path"
	"reflect"

	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

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

// Reconciler wires Ingresses claimed by one of our IngressClasses to a tunnel.
type Reconciler struct {
	client.Client
	Recorder events.EventRecorder
}

// New registers the Ingress controller with mgr.
//
// Ingress is served by every cluster, so unlike the Gateway API half there is
// no capability to probe for first.
func New(mgr ctrl.Manager, cfg config.Config) error {
	r := &Reconciler{
		Client:   mgr.GetClient(),
		Recorder: mgr.GetEventRecorder(ControllerName),
	}

	if err := ctrl.NewControllerManagedBy(mgr).
		For(&networkingv1.Ingress{}).
		Named(consts.ControllerIngress).
		Complete(r); err != nil {
		return fmt.Errorf("setup ingress controller: %w", err)
	}

	if cfg.Install {
		if err := mgr.Add(&installer{cfg: cfg}); err != nil {
			return fmt.Errorf("add ingressclass installer: %w", err)
		}
	}

	mgr.GetLogger().Info("ingress controller registered", "controller", ControllerName)
	return nil
}

// installer creates the IngressClass naming this controller once the manager
// starts, so the shipped manifest carries only a Deployment.
//
// A Runnable rather than setup-time work because it needs a live connection,
// and it builds its own client rather than using the manager's: the manager's
// reads go through a cache that has not synced yet at this point.
type installer struct {
	cfg config.Config
}

func (i *installer) Start(ctx context.Context) error {
	logger := log.FromContext(ctx).WithValues("ingressclass", consts.DefaultProvider)

	c, err := client.New(ctrl.GetConfigOrDie(), client.Options{})
	if err != nil {
		return fmt.Errorf("build client: %w", err)
	}

	class := &networkingv1.IngressClass{
		ObjectMeta: metav1.ObjectMeta{Name: consts.DefaultProvider},
		Spec:       networkingv1.IngressClassSpec{Controller: ControllerName},
	}

	err = c.Create(ctx, class)
	switch {
	case err == nil:
		logger.Info("created ingressclass")
		return nil
	case apierrors.IsAlreadyExists(err):
		// Someone else owns this name. spec.controller is immutable, so if it
		// points elsewhere this is a genuine conflict rather than a no-op —
		// say so instead of pretending the install succeeded.
		var existing networkingv1.IngressClass
		if err := c.Get(ctx, client.ObjectKey{Name: consts.DefaultProvider}, &existing); err != nil {
			return fmt.Errorf("get existing ingressclass: %w", err)
		}
		if existing.Spec.Controller != ControllerName {
			logger.Info("ingressclass exists but names a different controller; leaving it alone",
				"controller", existing.Spec.Controller)
			return nil
		}
		logger.Info("ingressclass already present")
		return nil
	default:
		return fmt.Errorf("create ingressclass: %w", err)
	}
}

func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var ing networkingv1.Ingress
	if err := r.Get(ctx, req.NamespacedName, &ing); err != nil {
		// Deleted between the event and this read. Nothing to undo yet; once
		// tunnels are real this is where they get torn down.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	provider, ours, err := r.provider(ctx, &ing)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !ours {
		// Another controller's Ingress, or no class at all. We are not a
		// default class, so an unclassed Ingress is never ours.
		return ctrl.Result{}, nil
	}

	logger.Info("unimplemented", "provider", provider, "ingress", req.NamespacedName)

	// Ingress has no conditions field, so an Event is the only place status
	// can surface. `kubectl describe ingress` shows it.
	r.Recorder.Eventf(&ing, nil, consts.EventTypeWarning, consts.ReasonUnimplemented, consts.ActionProvision,
		consts.MsgUnimplementedFmt, provider)

	// status.loadBalancer stays empty on purpose. Publishing a hostname we
	// cannot actually serve would be worse than publishing nothing.
	return ctrl.Result{}, nil
}

// provider reports whether this Ingress is ours and, if so, which host to mint
// tunnels from.
//
// Resolution runs most-specific first: the Ingress's own annotation, then its
// class's, then DefaultProvider. That lets one cluster default to a
// provider while sending individual workloads somewhere else, without needing
// a class per provider.
//
// This is what gets handed to libtunnel:
//
//	libtunnel.Cloudflare().WithProvider(provider)
func (r *Reconciler) provider(ctx context.Context, ing *networkingv1.Ingress) (string, bool, error) {
	name := ing.Spec.IngressClassName
	if name == nil || *name == "" {
		return "", false, nil
	}

	var class networkingv1.IngressClass
	if err := r.Get(ctx, client.ObjectKey{Name: *name}, &class); err != nil {
		if apierrors.IsNotFound(err) {
			// Dangling class reference. The Ingress is inert until the class
			// exists, and it is not ours to complain about.
			return "", false, nil
		}
		return "", false, fmt.Errorf("get ingressclass %q: %w", *name, err)
	}

	if class.Spec.Controller != ControllerName {
		return "", false, nil
	}

	if p := ing.Annotations[consts.AnnotationProvider]; p != "" {
		return p, true, nil
	}
	if p := class.Annotations[consts.AnnotationProvider]; p != "" {
		return p, true, nil
	}
	return consts.DefaultProvider, true, nil
}
