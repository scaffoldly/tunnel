// Package pod gives a bare Pod a public hostname from one annotation.
//
// It is the shortest path this controller offers:
//
//	kubectl run nginx --image=nginx
//	kubectl annotate pod nginx tunnel.pizza/tunnel=true
//
// # WHAT IT GENERATES, AND WHY IT DOES NOT MINT
//
// A Pod cannot be an Ingress backend — an Ingress names a Service — so this
// generates the Service that stands in front of the Pod and copies the tunnel
// annotations onto it. The Service controller then does everything it already
// does: resolve the provider, probe the origin, write a child Ingress or a
// Gateway pair, and publish the hostname there. Nothing here mints a tunnel,
// resolves an origin, or touches the tunnel store.
//
// That makes three generated objects for one annotation, which is more than
// this feature would like. The alternative — pointing a tunnel straight at the
// Pod IP and generating nothing — was rejected on two counts: it needs a second
// copy of the minting and publishing loop, and a Pod has no status field an
// address could be written to, so the hostname would live only in events.
//
// # THE SERVICE IS SELECTOR-LESS
//
// `kubectl run` labels a Pod `run: <name>`, and `kubectl run --expose` builds
// its Service around exactly that label. That is imprecise in a way that
// matters here: a Pod carrying `app: web` from a Deployment, annotated
// directly, would produce a Service selecting the entire Deployment and a
// tunnel fronting workloads nobody annotated. So the generated Service has no
// selector at all and an EndpointSlice names the one Pod IP — the only form
// that cannot capture a second Pod.
//
// # PODS ARE MORTAL
//
// A Service outlives its endpoints; a Pod does not. Eviction, a node drain or
// `kubectl delete pod` takes the Pod, and owner-reference GC takes the Service,
// the EndpointSlice, the Ingress and the tunnel with it. Nothing recreates
// them, and the hostname is not stable across a recreate — it is not stable
// across a controller restart either. This path is for a demo, a dev loop and a
// one-off share. Anything that should survive its Pod belongs on a Service.
package pod

import (
	"context"
	"errors"
	"fmt"
	"path"
	"reflect"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	"github.com/scaffoldly/tunnel/config"
	"github.com/scaffoldly/tunnel/consts"
	"github.com/scaffoldly/tunnel/service"
)

// ControllerName is this package's import path, read from the type system for
// the same reason the other halves do it. Nothing in a user's cluster names it;
// it exists to derive ReporterName.
var ControllerName = reflect.TypeFor[Reconciler]().PkgPath()

// Name is the short label for this controller, used in logs.
var Name = path.Base(ControllerName)

// ReporterName is the identity this controller's events are attributed to.
var ReporterName = consts.Reporter(ControllerName)

// Reconciler turns an annotated Pod into the Service that fronts it.
type Reconciler struct {
	client.Client
	// Pods reads the Pod being reconciled. The manager's *uncached* reader:
	// the watch below is metadata-only, and reading the full Pod through the
	// cached client would start a second, structured informer over every Pod
	// in the cluster — by far the most numerous object there is.
	Pods     client.Reader
	Recorder events.EventRecorder
	// Providers is the vocabulary a trigger may name.
	Providers []string
}

// New registers the Pod controller with mgr.
func New(mgr ctrl.Manager, _ config.Config) error {
	r := &Reconciler{
		Client:    mgr.GetClient(),
		Pods:      mgr.GetAPIReader(),
		Recorder:  mgr.GetEventRecorder(ReporterName),
		Providers: consts.InstalledProviders,
	}

	if err := ctrl.NewControllerManagedBy(mgr).
		// Metadata only, and filtered. Unlike Services, the only trigger here
		// is an annotation — there is no spec.loadBalancerClass equivalent to
		// go looking for — so the predicate can answer from metadata alone and
		// the overwhelming majority of Pods in a cluster are never enqueued and
		// never read. Only their metadata is cached, which is the price of
		// annotation discovery and is why this is not a full informer.
		WatchesMetadata(&corev1.Pod{}, &handler.EnqueueRequestForObject{},
			builder.WithPredicates(triggered())).
		Named(consts.ControllerPod).
		Complete(r); err != nil {
		return fmt.Errorf("setup pod controller: %w", err)
	}

	mgr.GetLogger().Info("pod controller registered", "controller", ControllerName)
	return nil
}

// triggered matches Pods that carry a tunnel annotation now or carried one a
// moment ago.
//
// The second half is the whole point and the easy thing to get wrong: filtering
// on the new object alone means removing the annotation produces no event, so
// the generated Service is never collected and the tunnel runs until the Pod
// dies. Deleting the trigger has to reach the reconciler that undoes it.
func triggered() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc:  func(e event.CreateEvent) bool { return asks(e.Object) },
		DeleteFunc:  func(e event.DeleteEvent) bool { return asks(e.Object) },
		GenericFunc: func(e event.GenericEvent) bool { return asks(e.Object) },
		UpdateFunc: func(e event.UpdateEvent) bool {
			return asks(e.ObjectOld) || asks(e.ObjectNew)
		},
	}
}

// asks reports whether an object carries the activation label at all, whatever
// it says. Deliberately not "asks for a tunnel": a value of "none" or a typo
// still has to reach the reconciler, which is what reports it or cleans up
// after it.
func asks(obj client.Object) bool {
	_, ok := obj.GetLabels()[consts.TunnelLabel]
	return ok
}

// Reconcile brings the Service fronting one Pod into line with what the Pod
// asks for.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var pod corev1.Pod
	if err := r.Pods.Get(ctx, req.NamespacedName, &pod); err != nil {
		if apierrors.IsNotFound(err) {
			// The Service and EndpointSlice carry an ownerReference to the Pod,
			// so the garbage collector removes them, and the Ingress goes with
			// the Service. Nothing to undo, and no finalizer.
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if pod.DeletionTimestamp != nil {
		return ctrl.Result{}, nil
	}

	wanted, err := service.Requested(pod.Labels)
	if err != nil {
		// Unsupported by construction: this reads one object's annotations and
		// reaches nothing else, so no retry changes the answer.
		logger.Info("pod not serviceable", "reason", err)
		if err := r.prune(ctx, &pod, false); err != nil {
			return ctrl.Result{}, err
		}
		r.Recorder.Eventf(&pod, nil, consts.EventTypeWarning, consts.ReasonUnsupported,
			consts.ActionProvision, consts.MsgUnsupportedFmt, err)
		return ctrl.Result{}, nil
	}

	if len(wanted) == 0 {
		// Either the annotation was removed or it says none. Both mean the
		// generated objects should go; owner-reference GC does not cover it,
		// because the Pod is still here.
		return ctrl.Result{}, r.prune(ctx, &pod, false)
	}

	// A Pod with no address yet has nothing to point an EndpointSlice at. It
	// gets one the moment it is scheduled, and that is an update on the Pod, so
	// there is nothing to requeue for.
	if pod.Status.PodIP == "" {
		logger.Info("pod has no address yet", "pod", pod.Name)
		return ctrl.Result{}, nil
	}

	port, guessed := frontedPort(&pod)
	if err := r.ensure(ctx, &pod, port); err != nil {
		if errors.Is(err, consts.ErrUnsupported) {
			r.Recorder.Eventf(&pod, nil, consts.EventTypeWarning, consts.ReasonUnsupported,
				consts.ActionProvision, consts.MsgUnsupportedFmt, err)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// The guess is stated where the person who made it can see it. A Pod that
	// declares no port is the ordinary case — `kubectl run` declares none — and
	// 80 is right for most images and silently wrong for the rest. Someone
	// whose app listens on 3000 should be able to find out why their tunnel
	// serves errors without reading this source.
	if guessed {
		r.Recorder.Eventf(&pod, nil, consts.EventTypeNormal, consts.ReasonProvisioning,
			consts.ActionProvision, consts.MsgPortAssumedFmt, port, childName(pod.Name))
	}
	return ctrl.Result{}, nil
}
