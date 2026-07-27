// Package config carries the runtime knobs shared by the controllers.
package config

import (
	"context"
	"fmt"
	"strings"

	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Config is resolved from flags once, in main, and passed to each New.
type Config struct {
	// InstallIngressClasses has the controller create its default
	// IngressClasses at startup, so the shipped manifest is a Deployment and
	// its RBAC rather than a list of classes. Off when they are managed
	// elsewhere — Helm, Argo, anything that would fight over ownership.
	InstallIngressClasses bool

	// InstallGatewayClasses is the same for GatewayClasses. Separate because a
	// cluster can want one API's classes and not the other's.
	InstallGatewayClasses bool

	// InstallGatewayAPI has the controller install the Gateway API CRDs where
	// a cluster has none.
	//
	// Its own switch, and the one worth turning off deliberately: the CRDs are
	// cluster-scoped and shared by every Gateway API implementation present,
	// so this is the only install that can affect software this controller did
	// not deploy. A cluster running Istio or Cilium already has them, and
	// should own them.
	InstallGatewayAPI bool
}

// saPrefix begins the username the API server gives a ServiceAccount:
// system:serviceaccount:<namespace>:<name>.
const saPrefix = "system:serviceaccount:"

// namespace is swappable so tests need no API server.
var namespace = selfNamespace

// selfNamespace asks the API server who this controller authenticates as, and
// reads the namespace out of the answer.
//
// SelfSubjectReview rather than the downward API or the projected token file:
// the credential already knows, so there is no environment variable to forget
// to set and no file path to hardcode. It needs no RBAC of its own — the
// built-in system:basic-user role grants create on selfsubjectreviews to
// system:authenticated, so anything that can talk to the API server at all can
// ask this.
//
// Not SubjectAccessReview, which answers "may I do X" rather than "who am I".
//
// Returns empty when the caller is not a ServiceAccount — a kubeconfig from a
// laptop authenticates as a user, and there is then no namespace to own
// anything.
func selfNamespace(ctx context.Context, cl client.Client) (string, error) {
	review := &authenticationv1.SelfSubjectReview{}
	if err := cl.Create(ctx, review); err != nil {
		return "", fmt.Errorf("selfsubjectreview: %w", err)
	}

	rest, ok := strings.CutPrefix(review.Status.UserInfo.Username, saPrefix)
	if !ok {
		return "", nil
	}
	ns, _, ok := strings.Cut(rest, ":")
	if !ok {
		return "", nil
	}
	return ns, nil
}

// Owner resolves the object that should own the classes this controller
// creates, so deleting the install takes them with it.
//
// It is the controller's own Namespace, and it has to be: the classes are
// cluster-scoped, and a cluster-scoped dependent whose owner is namespaced is
// treated as having an unresolvable reference — never garbage collected, and
// flagged with an OwnerRefInvalidNamespace event. That rules out the
// Deployment, which is the intuitive choice. The Namespace is the nearest
// cluster-scoped thing the chart creates and uninstalling removes.
//
// Returns nil when there is nothing to own the classes: authenticating as
// anything other than a ServiceAccount means no namespace, which is the case
// running against a kubeconfig from a laptop. Unowned classes are the status
// quo, so that is a degradation rather than a failure.
func (c Config) Owner(ctx context.Context, cl client.Client) (*metav1.OwnerReference, error) {
	name, err := namespace(ctx, cl)
	if err != nil {
		return nil, err
	}
	if name == "" {
		return nil, nil
	}

	var ns corev1.Namespace
	if err := cl.Get(ctx, client.ObjectKey{Name: name}, &ns); err != nil {
		return nil, fmt.Errorf("get namespace %q: %w", name, err)
	}

	// No BlockOwnerDeletion: it would need update access on the owner's
	// finalizers subresource, and nothing here is worth holding a namespace
	// deletion open for.
	return &metav1.OwnerReference{
		APIVersion: corev1.SchemeGroupVersion.String(),
		Kind:       "Namespace",
		Name:       ns.Name,
		UID:        ns.UID,
	}, nil
}
