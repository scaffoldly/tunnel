package ingress

import (
	"context"
	"errors"
	"fmt"

	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/scaffoldly/tunnel/config"
	"github.com/scaffoldly/tunnel/consts"
)

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
	c, err := client.New(ctrl.GetConfigOrDie(), client.Options{})
	if err != nil {
		return fmt.Errorf("build client: %w", err)
	}

	owner, err := i.cfg.Owner(ctx, c)
	if err != nil {
		return fmt.Errorf("resolve owner: %w", err)
	}
	return install(ctx, c, owner)
}

// install creates one IngressClass per provider, and leaves any that already
// exist alone. Separate from Start so it can be exercised against a fake
// client; Start's job is only to build the real one.
//
// One provider failing does not stop the others: a cluster that can reach
// Cloudflare but not us should still end up with a usable class.
func install(ctx context.Context, c client.Client, owner *metav1.OwnerReference) error {
	var errs []error
	for _, provider := range consts.InstalledProviders {
		if err := installClass(ctx, c, provider, owner); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func installClass(ctx context.Context, c client.Client, provider string, owner *metav1.OwnerReference) error {
	logger := log.FromContext(ctx).WithValues("ingressclass", provider)

	// No provider annotation: the name is the provider. (*Reconciler).provider
	// falls back to the class's own name, so annotating it here would only
	// restate it — and then two places could disagree.
	class := &networkingv1.IngressClass{
		ObjectMeta: metav1.ObjectMeta{Name: provider},
		Spec:       networkingv1.IngressClassSpec{Controller: ControllerName},
	}
	// Owned by the namespace, so uninstalling collects the classes too. Only
	// on create: an existing class keeps whatever ownership it already has.
	if owner != nil {
		class.OwnerReferences = []metav1.OwnerReference{*owner}
	}

	err := c.Create(ctx, class)
	switch {
	case err == nil:
		logger.Info("created ingressclass")
		return nil
	case apierrors.IsAlreadyExists(err):
		// Someone else owns this name. spec.controller is immutable, so if it
		// points elsewhere this is a genuine conflict rather than a no-op —
		// say so instead of pretending the install succeeded.
		var existing networkingv1.IngressClass
		if err := c.Get(ctx, client.ObjectKey{Name: provider}, &existing); err != nil {
			return fmt.Errorf("get existing ingressclass %q: %w", provider, err)
		}
		if existing.Spec.Controller != ControllerName {
			logger.Info("ingressclass exists but names a different controller; leaving it alone",
				"controller", existing.Spec.Controller)
			return nil
		}
		logger.Info("ingressclass already present")
		return nil
	default:
		return fmt.Errorf("create ingressclass %q: %w", provider, err)
	}
}
