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

	"github.com/scaffoldly/tunnel/consts"
)

// maxNameLength is the longest name the API server accepts for an Ingress:
// object names are DNS subdomains.
const maxNameLength = 253

// hashLength is how much of the digest goes on the end of a truncated name.
// Eight hex characters is 32 bits — enough that two names colliding after
// truncation is not a thing that happens, and short enough to leave the
// readable part readable.
const hashLength = 8

// ensure creates or updates the child Ingress for one resolved provider and
// returns it as it now exists.
func (r *Reconciler) ensure(ctx context.Context, svc *corev1.Service, want resolved) (*networkingv1.Ingress, error) {
	logger := log.FromContext(ctx)
	desired := child(svc, want)

	var existing networkingv1.Ingress
	err := r.Get(ctx, client.ObjectKeyFromObject(desired), &existing)
	switch {
	case apierrors.IsNotFound(err):
		if err := r.Create(ctx, desired); err != nil {
			return nil, fmt.Errorf("create ingress %s: %w", client.ObjectKeyFromObject(desired), err)
		}
		logger.Info("created child ingress", "ingress", desired.Name, "provider", want.provider)
		r.Recorder.Eventf(svc, nil, consts.EventTypeNormal, consts.ReasonProvisioning,
			consts.ActionProvision, consts.MsgProvisioningFmt, "Ingress", desired.Name, want.provider)
		return desired, nil
	case err != nil:
		return nil, fmt.Errorf("get ingress %s: %w", client.ObjectKeyFromObject(desired), err)
	}

	// Ownership is the whole of the authorisation to write here. The RBAC
	// grants delete on ingresses cluster-wide because it must, so the scoping
	// has to happen in code: anything this controller did not create is
	// somebody else's object, whatever its name says.
	if !metav1.IsControlledBy(&existing, svc) {
		return nil, fmt.Errorf("%w: %s", consts.ErrUnsupported,
			fmt.Sprintf(consts.MsgChildConflictFmt, "Ingress", existing.Name))
	}

	if apiequality.Semantic.DeepEqual(existing.Spec, desired.Spec) &&
		existing.Labels[consts.LabelManagedBy] == desired.Labels[consts.LabelManagedBy] &&
		matches(existing.Annotations, desired.Annotations) {
		return &existing, nil
	}

	// Spec and label only. The child's status belongs to the Ingress half,
	// which publishes the hostname there, and overwriting it here would be
	// two controllers writing one field.
	updated := existing.DeepCopy()
	updated.Spec = desired.Spec
	if updated.Labels == nil {
		updated.Labels = map[string]string{}
	}
	updated.Labels[consts.LabelManagedBy] = consts.ManagedBy
	// Merged, not replaced: an annotation somebody else put on the child is
	// not ours to drop.
	if updated.Annotations == nil {
		updated.Annotations = map[string]string{}
	}
	for k, v := range desired.Annotations {
		updated.Annotations[k] = v
	}
	if err := r.Update(ctx, updated); err != nil {
		return nil, fmt.Errorf("update ingress %s: %w", client.ObjectKeyFromObject(updated), err)
	}
	logger.Info("updated child ingress", "ingress", updated.Name, "provider", want.provider)
	return updated, nil
}

// prune deletes the children of svc that keep does not name.
//
// Owner-reference GC covers the Service being deleted. It does not cover the
// trigger being removed while the Service stays, which is the ordinary case:
// a user deletes the annotation and expects the tunnel to stop. Nothing else
// in the system notices that, so it is done here.
func (r *Reconciler) prune(ctx context.Context, svc *corev1.Service, keep map[string]struct{}) error {
	logger := log.FromContext(ctx)

	var owned networkingv1.IngressList
	if err := r.List(ctx, &owned, client.InNamespace(svc.Namespace)); err != nil {
		return fmt.Errorf("list ingresses in %s: %w", svc.Namespace, err)
	}

	for i := range owned.Items {
		ing := &owned.Items[i]
		// IsControlledBy compares the controller reference's UID, so a Service
		// deleted and recreated under the same name does not inherit the old
		// one's children.
		if !metav1.IsControlledBy(ing, svc) {
			continue
		}
		if _, ok := keep[ing.Name]; ok {
			continue
		}
		if err := r.Delete(ctx, ing); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete ingress %s: %w", client.ObjectKeyFromObject(ing), err)
		}
		logger.Info("deleted child ingress no longer asked for", "ingress", ing.Name)
	}
	return nil
}

// child builds the Ingress that stands for one resolved provider.
//
// A default backend rather than a rule: the tunnel fronts one origin, so there
// is no path to match on, and spec.defaultBackend is what the Ingress half's
// origin resolution already reduces to. The class name is the provider, which
// is the same contract a hand-written Ingress uses.
func child(svc *corev1.Service, want resolved) *networkingv1.Ingress {
	className := want.provider
	return &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:      childName(svc.Name, want.provider),
			Namespace: svc.Namespace,
			Labels:    map[string]string{consts.LabelManagedBy: consts.ManagedBy},
			// The child restates how its origin is dialed, rather than the
			// Ingress half reaching back to the Service for it. That keeps the
			// generated object self-describing — the whole argument for
			// generating a real object instead of hiding the tunnel — and it
			// means a hand-written Ingress can say the same thing the same way.
			Annotations: map[string]string{
				want.provider + "/" + consts.ProtocolAnnotation: want.protocol,
			},
			// Namespaced owner, namespaced dependent, same namespace: legal,
			// unlike the cluster-scoped classes the installer creates, which
			// have to be owned by a Namespace.
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(svc, corev1.SchemeGroupVersion.WithKind("Service")),
			},
		},
		Spec: networkingv1.IngressSpec{
			IngressClassName: &className,
			DefaultBackend: &networkingv1.IngressBackend{
				Service: &networkingv1.IngressServiceBackend{
					Name: svc.Name,
					// By number. A name would be resolved back to this same
					// number by the Ingress half, and the number is what
					// port selection already decided.
					Port: networkingv1.ServiceBackendPort{Number: want.port.number},
				},
			},
		},
	}
}

// childName is the name of the child Ingress for one Service and provider.
//
// Deterministic, because it is the only handle on the child: an unstable name
// orphans the previous one on every reconcile, and prune would then delete and
// recreate a tunnel per loop. Dots become dashes so the provider reads as one
// piece of the name rather than as extra DNS labels.
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

// matches reports whether want is a subset of got. The child may carry
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
