package pod

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/scaffoldly/tunnel/consts"
)

// defaultPort is what a Pod that declares no container port is assumed to
// serve.
//
// `kubectl run nginx --image=nginx` declares none — the field is only populated
// with --port — so refusing here would fail the shortest path this controller
// offers on its first line. It is a guess, and unlike every other guess in this
// codebase it is allowed to be one because the alternative is no feature. What
// makes it defensible is that it is reported: see MsgPortAssumedFmt.
const defaultPort int32 = 80

// portNames are the container port names worth preferring, in order, when a Pod
// declares several. Same rule as the Service half.
var portNames = []string{"http", "https"}

// frontedPort picks the port the generated Service targets, and reports whether
// it had to be assumed.
//
// A declared port always wins. 80 is the fallback for a Pod that declares
// nothing, not an override of one that does: a Pod declaring only
// containerPort 8080 gets 8080.
func frontedPort(pod *corev1.Pod) (int32, bool) {
	var declared []corev1.ContainerPort
	for _, c := range pod.Spec.Containers {
		for _, p := range c.Ports {
			// TCP only, for the same reason as the Service half: a tunnel
			// carries HTTP over TCP, so a UDP port is not a worse candidate, it
			// is not a candidate.
			if p.Protocol == "" || p.Protocol == corev1.ProtocolTCP {
				declared = append(declared, p)
			}
		}
	}

	switch len(declared) {
	case 0:
		return defaultPort, true
	case 1:
		return declared[0].ContainerPort, false
	}

	for _, want := range portNames {
		for _, p := range declared {
			if p.Name == want {
				return p.ContainerPort, false
			}
		}
	}

	// Several ports and no conventional name. `kubectl run --port` produces an
	// *unnamed* port, so a Pod built that way can never be disambiguated by
	// name — which makes refusing here a dead end rather than a correction,
	// since container ports cannot be edited on a running Pod. The first
	// declared port is taken instead, and it is reported as assumed so the
	// choice is visible.
	return declared[0].ContainerPort, true
}

// childName is the name of the Service and EndpointSlice generated for a Pod.
//
// Suffixed rather than sharing the Pod's name: a Service named after the Pod
// would collide with a Service the user already has for the same workload,
// which is exactly the object `kubectl run --expose` creates.
func childName(pod string) string {
	name := pod + "-" + consts.ManagedBy
	if len(name) <= maxNameLength {
		return name
	}
	return name[:maxNameLength]
}

// maxNameLength is the longest name the API server accepts. Pod names are DNS
// subdomains too, so this is only reachable by a Pod already near the limit.
const maxNameLength = 253

// ensure creates or updates the Service and EndpointSlice fronting one Pod.
func (r *Reconciler) ensure(ctx context.Context, pod *corev1.Pod, port int32) error {
	if err := r.ensureObject(ctx, pod, serviceChild(pod, port)); err != nil {
		return err
	}
	return r.ensureObject(ctx, pod, sliceChild(pod, port))
}

func (r *Reconciler) ensureObject(ctx context.Context, pod *corev1.Pod, desired client.Object) error {
	logger := log.FromContext(ctx)
	kind := kindOf(desired)

	existing := emptyLike(desired)
	err := r.Get(ctx, client.ObjectKeyFromObject(desired), existing)
	switch {
	case apierrors.IsNotFound(err):
		if err := r.Create(ctx, desired); err != nil {
			return fmt.Errorf("create %s %s: %w", kind, client.ObjectKeyFromObject(desired), err)
		}
		logger.Info("created child", "kind", kind, "name", desired.GetName(), "pod", pod.Name)
		r.Recorder.Eventf(pod, nil, consts.EventTypeNormal, consts.ReasonProvisioning,
			consts.ActionProvision, consts.MsgProvisioningFmt, kind, desired.GetName(), "the Pod's annotations")
		return nil
	case err != nil:
		return fmt.Errorf("get %s %s: %w", kind, client.ObjectKeyFromObject(desired), err)
	}

	// Ownership is the whole of the authorisation to write here. The RBAC
	// grants these verbs cluster-wide because it must, so the scoping happens
	// in code: a Service this controller did not create is somebody else's,
	// whatever its name says — and on this path that is very likely to be the
	// one `kubectl run --expose` made.
	if !metav1.IsControlledBy(existing, pod) {
		return fmt.Errorf("%w: %s", consts.ErrUnsupported,
			fmt.Sprintf(consts.MsgChildConflictFmt, kind, existing.GetName()))
	}

	if equal(existing, desired) {
		return nil
	}

	updated := existing.DeepCopyObject().(client.Object)
	copyInto(updated, desired)
	if err := r.Update(ctx, updated); err != nil {
		return fmt.Errorf("update %s %s: %w", kind, client.ObjectKeyFromObject(updated), err)
	}
	logger.Info("updated child", "kind", kind, "name", updated.GetName(), "pod", pod.Name)
	return nil
}

// prune removes the generated objects. keep is false in every current caller
// and exists so the signature says what it does rather than what it is used
// for.
func (r *Reconciler) prune(ctx context.Context, pod *corev1.Pod, keep bool) error {
	if keep {
		return nil
	}
	logger := log.FromContext(ctx)
	name := childName(pod.Name)

	for _, obj := range []client.Object{
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: pod.Namespace, Name: name}},
		&discoveryv1.EndpointSlice{ObjectMeta: metav1.ObjectMeta{Namespace: pod.Namespace, Name: name}},
	} {
		existing := emptyLike(obj)
		if err := r.Get(ctx, client.ObjectKeyFromObject(obj), existing); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return fmt.Errorf("get %s %s: %w", kindOf(obj), client.ObjectKeyFromObject(obj), err)
		}
		// Never delete what we did not create, even at a name we would have
		// used. `kubectl run --expose` puts a Service right here.
		if !metav1.IsControlledBy(existing, pod) {
			continue
		}
		if err := r.Delete(ctx, existing); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete %s %s: %w", kindOf(obj), client.ObjectKeyFromObject(existing), err)
		}
		logger.Info("deleted child no longer asked for", "kind", kindOf(obj), "name", existing.GetName())
	}
	return nil
}

// serviceChild is the Service that stands in front of the Pod.
//
// No selector, deliberately — see the package doc. The tunnel annotations are
// copied across verbatim, which is what makes the Service controller do all the
// remaining work and what makes `tunnel: gateway` on a Pod produce a Gateway
// pair without this package knowing anything about the Gateway API.
func serviceChild(pod *corev1.Pod, port int32) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: objectMeta(pod),
		Spec: corev1.ServiceSpec{
			Type: corev1.ServiceTypeClusterIP,
			Ports: []corev1.ServicePort{{
				Name:       "http",
				Port:       port,
				TargetPort: intstr.FromInt32(port),
				Protocol:   corev1.ProtocolTCP,
			}},
		},
	}
}

// sliceChild names the one Pod IP the Service routes to.
//
// This is the piece that makes a selector-less Service work, and it is the only
// object here whose contents change during the Pod's life: readiness follows
// the Pod's own condition, so a Pod that goes unready stops receiving traffic
// exactly as it would behind an ordinary Service.
func sliceChild(pod *corev1.Pod, port int32) *discoveryv1.EndpointSlice {
	meta := objectMeta(pod)
	// The Service this slice belongs to is named by label, not by reference.
	meta.Labels[discoveryv1.LabelServiceName] = childName(pod.Name)

	ready := podReady(pod)
	portName := "http"
	protocol := corev1.ProtocolTCP
	return &discoveryv1.EndpointSlice{
		ObjectMeta:  meta,
		AddressType: addressType(pod.Status.PodIP),
		Ports: []discoveryv1.EndpointPort{{
			Name:     &portName,
			Port:     &port,
			Protocol: &protocol,
		}},
		Endpoints: []discoveryv1.Endpoint{{
			Addresses:  []string{pod.Status.PodIP},
			Conditions: discoveryv1.EndpointConditions{Ready: &ready},
		}},
	}
}

// addressType reports which family the Pod's address is in. Getting this wrong
// makes the API server reject the slice rather than route it wrongly, which is
// the better failure but still a failure.
func addressType(ip string) discoveryv1.AddressType {
	if strings.Contains(ip, ":") {
		return discoveryv1.AddressTypeIPv6
	}
	return discoveryv1.AddressTypeIPv4
}

func podReady(pod *corev1.Pod) bool {
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// objectMeta is the metadata both generated objects carry.
func objectMeta(pod *corev1.Pod) metav1.ObjectMeta {
	annotations := map[string]string{}
	for key, value := range pod.Annotations {
		// Only ours, and only the ones that mean something downstream. Copying
		// the Pod's whole annotation set would drag kubectl's last-applied and
		// every sidecar injector's marker onto an object they were not written
		// for.
		if _, name, ok := strings.Cut(key, "/"); ok &&
			(name == consts.TunnelAnnotation || name == consts.ProtocolAnnotation) {
			annotations[key] = value
		}
	}

	return metav1.ObjectMeta{
		Name:        childName(pod.Name),
		Namespace:   pod.Namespace,
		Labels:      map[string]string{consts.LabelManagedBy: consts.ManagedBy},
		Annotations: annotations,
		OwnerReferences: []metav1.OwnerReference{
			*metav1.NewControllerRef(pod, corev1.SchemeGroupVersion.WithKind("Pod")),
		},
	}
}
