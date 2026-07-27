package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/scaffoldly/tunnel/consts"
)

// maxNameLength is the longest name the API server accepts: object names are
// DNS subdomains.
const maxNameLength = 253

// hashLength is how much of the digest goes on the end of a truncated name.
// Eight hex characters is 32 bits — enough that two names colliding after
// truncation is not a thing that happens, and short enough to leave the
// readable part readable.
const hashLength = 8

// children builds every object one resolved provider needs.
//
// One for the Ingress branch, two for the Gateway branch — and the Gateway
// branch needs both or neither: a Gateway names no backend, so a Gateway with
// no route attached has nothing to point a tunnel at, which the Gateway half
// reports as unsupported. The HTTPRoute is what makes the Gateway serve, so
// they are one unit here even though they are two objects in the cluster.
//
// The first object returned is the one that will carry the public address.
func children(svc *corev1.Service, want resolved) []client.Object {
	name := childName(svc.Name, want.provider)

	switch want.api {
	case apiGateway:
		return []client.Object{
			gatewayChild(svc, want, name),
			routeChild(svc, want, name),
		}
	default:
		return []client.Object{ingressChild(svc, want, name)}
	}
}

// ensure creates or updates one child and returns it as it now exists.
func (r *Reconciler) ensure(ctx context.Context, svc *corev1.Service, want resolved, desired client.Object) (client.Object, error) {
	logger := log.FromContext(ctx)
	kind := kindOf(desired)

	existing := emptyLike(desired)
	err := r.Get(ctx, client.ObjectKeyFromObject(desired), existing)
	switch {
	case apierrors.IsNotFound(err):
		if err := r.Create(ctx, desired); err != nil {
			return nil, fmt.Errorf("create %s %s: %w", kind, client.ObjectKeyFromObject(desired), err)
		}
		logger.Info("created child", "kind", kind, "name", desired.GetName(), "provider", want.provider)
		r.Recorder.Eventf(svc, nil, consts.EventTypeNormal, consts.ReasonProvisioning,
			consts.ActionProvision, consts.MsgProvisioningFmt, kind, desired.GetName(), want.provider)
		return desired, nil
	case err != nil:
		return nil, fmt.Errorf("get %s %s: %w", kind, client.ObjectKeyFromObject(desired), err)
	}

	// Ownership is the whole of the authorisation to write here. The RBAC
	// grants delete on these kinds cluster-wide because it must, so the scoping
	// has to happen in code: anything this controller did not create is
	// somebody else's object, whatever its name says.
	if !metav1.IsControlledBy(existing, svc) {
		return nil, fmt.Errorf("%w: %s", consts.ErrUnsupported,
			fmt.Sprintf(consts.MsgChildConflictFmt, kind, existing.GetName()))
	}

	if apiequality.Semantic.DeepEqual(specOf(existing), specOf(desired)) &&
		existing.GetLabels()[consts.LabelManagedBy] == desired.GetLabels()[consts.LabelManagedBy] &&
		matches(existing.GetAnnotations(), desired.GetAnnotations()) {
		return existing, nil
	}

	updated := existing.DeepCopyObject().(client.Object)
	copySpec(updated, desired)

	labels := updated.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	labels[consts.LabelManagedBy] = consts.ManagedBy
	updated.SetLabels(labels)

	// Merged, not replaced: an annotation somebody else put on the child is
	// not ours to drop.
	annotations := updated.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	for k, v := range desired.GetAnnotations() {
		annotations[k] = v
	}
	updated.SetAnnotations(annotations)

	if err := r.Update(ctx, updated); err != nil {
		return nil, fmt.Errorf("update %s %s: %w", kind, client.ObjectKeyFromObject(updated), err)
	}
	logger.Info("updated child", "kind", kind, "name", updated.GetName(), "provider", want.provider)
	return updated, nil
}

// prune deletes the children of svc that keep does not name.
//
// Owner-reference GC covers the Service being deleted. It does not cover the
// trigger being removed while the Service stays, which is the ordinary case: a
// user deletes the annotation and expects the tunnel to stop. Nor does it cover
// a Service switching from one API to the other, which would otherwise leave
// the previous branch's children behind, still serving, with the Service no
// longer asking for them. Nothing else in the system notices either, so both
// are done here.
func (r *Reconciler) prune(ctx context.Context, svc *corev1.Service, keep map[childKey]struct{}) error {
	logger := log.FromContext(ctx)

	// Every kind, every time, regardless of what the Service currently asks
	// for: the whole point is to collect children of a branch it has stopped
	// asking for.
	lists := []client.ObjectList{&networkingv1.IngressList{}}
	if r.GatewayAPI {
		// Only where the API server serves them. Listing a kind it does not
		// know is an error, and on an Ingress-only cluster there is nothing of
		// these kinds to collect anyway.
		lists = append(lists, &gatewayv1.GatewayList{}, &gatewayv1.HTTPRouteList{})
	}

	for _, list := range lists {
		if err := r.List(ctx, list, client.InNamespace(svc.Namespace)); err != nil {
			return fmt.Errorf("list children in %s: %w", svc.Namespace, err)
		}

		for _, obj := range itemsOf(list) {
			// IsControlledBy compares the controller reference's UID, so a
			// Service deleted and recreated under the same name does not
			// inherit the old one's children.
			if !metav1.IsControlledBy(obj, svc) {
				continue
			}
			if _, ok := keep[keyOf(obj)]; ok {
				continue
			}
			if err := r.Delete(ctx, obj); err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("delete %s %s: %w", kindOf(obj), client.ObjectKeyFromObject(obj), err)
			}
			logger.Info("deleted child no longer asked for", "kind", kindOf(obj), "name", obj.GetName())
		}
	}
	return nil
}

// ingressChild is the Ingress branch: one object, a default backend, no rules.
//
// A default backend rather than a rule because the tunnel fronts one origin, so
// there is no path to match on, and spec.defaultBackend is what the Ingress
// half's origin resolution already reduces to.
func ingressChild(svc *corev1.Service, want resolved, name string) *networkingv1.Ingress {
	className := want.provider
	return &networkingv1.Ingress{
		ObjectMeta: objectMeta(svc, want, name),
		Spec: networkingv1.IngressSpec{
			IngressClassName: &className,
			DefaultBackend: &networkingv1.IngressBackend{
				Service: &networkingv1.IngressServiceBackend{
					Name: svc.Name,
					// By number. A name would be resolved back to this same
					// number by the Ingress half, and the number is what port
					// selection already decided.
					Port: networkingv1.ServiceBackendPort{Number: want.port.number},
				},
			},
		},
	}
}

// gatewayChild is the Gateway branch's front half: the object the class is
// named on and the address is published to.
//
// One HTTP listener on 80. The listener describes the *public* side, which the
// tunnel edge terminates — it is not how the origin is dialed, and nothing here
// binds a local port. It mirrors what a hand-written Gateway for this
// controller looks like, which is the shape tests/e2e/gateway already proves.
//
// allowedRoutes from Same, because the only route that will ever attach is the
// one below it, in this Service's namespace. The Gateway half refuses
// cross-namespace backendRefs outright rather than honouring one without a
// ReferenceGrant, so a wider setting would advertise something it will not do.
func gatewayChild(svc *corev1.Service, want resolved, name string) *gatewayv1.Gateway {
	from := gatewayv1.NamespacesFromSame
	return &gatewayv1.Gateway{
		ObjectMeta: objectMeta(svc, want, name),
		Spec: gatewayv1.GatewaySpec{
			GatewayClassName: gatewayv1.ObjectName(want.provider),
			Listeners: []gatewayv1.Listener{{
				Name:     "http",
				Protocol: gatewayv1.HTTPProtocolType,
				Port:     gatewayv1.PortNumber(80),
				AllowedRoutes: &gatewayv1.AllowedRoutes{
					Namespaces: &gatewayv1.RouteNamespaces{From: &from},
				},
			}},
		},
	}
}

// routeChild is the Gateway branch's other half, and the one that does the
// work: a Gateway names no backend, so the origin is whatever HTTPRoutes name
// it as a parent. Without this the Gateway has nothing to point a tunnel at.
//
// parentRefs carries no namespace on purpose. The Gateway API defaults it to
// the route's own, which is where the Gateway is, and spelling it out would
// only be a second place for the two to disagree.
func routeChild(svc *corev1.Service, want resolved, name string) *gatewayv1.HTTPRoute {
	port := gatewayv1.PortNumber(want.port.number)
	return &gatewayv1.HTTPRoute{
		ObjectMeta: objectMeta(svc, want, name),
		Spec: gatewayv1.HTTPRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{{
					Name: gatewayv1.ObjectName(name),
				}},
			},
			Rules: []gatewayv1.HTTPRouteRule{{
				BackendRefs: []gatewayv1.HTTPBackendRef{{
					BackendRef: gatewayv1.BackendRef{
						BackendObjectReference: gatewayv1.BackendObjectReference{
							Name: gatewayv1.ObjectName(svc.Name),
							Port: &port,
						},
					},
				}},
			}},
		},
	}
}

// objectMeta is the metadata every child carries, whatever its kind.
func objectMeta(svc *corev1.Service, want resolved, name string) metav1.ObjectMeta {
	return metav1.ObjectMeta{
		Name:      name,
		Namespace: svc.Namespace,
		Labels:    map[string]string{consts.LabelManagedBy: consts.ManagedBy},
		// The child restates how its origin is dialed, rather than the serving
		// reconciler reaching back to the Service for it. That keeps the
		// generated object self-describing — the whole argument for generating
		// a real object instead of hiding the tunnel — and it means a
		// hand-written object can say the same thing the same way.
		Annotations: map[string]string{
			want.provider + "/" + consts.ProtocolAnnotation: want.protocol,
		},
		// Namespaced owner, namespaced dependent, same namespace: legal, unlike
		// the cluster-scoped classes the installer creates, which have to be
		// owned by a Namespace.
		OwnerReferences: []metav1.OwnerReference{
			*metav1.NewControllerRef(svc, corev1.SchemeGroupVersion.WithKind("Service")),
		},
	}
}

// childName is the name of the children for one Service and provider.
//
// Deterministic, because it is the only handle on them: an unstable name
// orphans the previous child on every reconcile, and prune would then delete
// and recreate a tunnel per loop. Dots become dashes so the provider reads as
// one piece of the name rather than as extra DNS labels.
//
// The Gateway and the HTTPRoute share it, and share it with the Ingress branch
// too. Different kinds do not collide, and a Service switching API then
// replaces its children instead of leaving a differently-named orphan.
//
// Service names are DNS labels and cannot contain dots, so the result has none
// either and the only limit that can be reached is the 253-character one.
func childName(service, provider string) string {
	name := service + "-" + strings.ReplaceAll(provider, ".", "-")
	if len(name) <= maxNameLength {
		return name
	}

	// Truncate and re-uniquify. The digest covers the untruncated inputs, so
	// two providers that agree on their first many characters still land on
	// different names.
	sum := sha256.Sum256([]byte(service + "/" + provider))
	suffix := "-" + hex.EncodeToString(sum[:])[:hashLength]
	return name[:maxNameLength-len(suffix)] + suffix
}

// matches reports whether want is a subset of got. A child may carry
// annotations this controller did not write — kubectl's last-applied, a mesh's
// injection marker — and rewriting it on every pass because of them would churn
// the object and re-mint its tunnel.
func matches(got, want map[string]string) bool {
	for k, v := range want {
		if got[k] != v {
			return false
		}
	}
	return true
}
