package gateway

import (
	"context"
	"errors"
	"fmt"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/scaffoldly/tunnel/config"
	"github.com/scaffoldly/tunnel/consts"
)

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
	c, err := client.New(ctrl.GetConfigOrDie(), client.Options{Scheme: scheme()})
	if err != nil {
		return fmt.Errorf("build client: %w", err)
	}

	owner, err := i.cfg.Owner(ctx, c)
	if err != nil {
		return fmt.Errorf("resolve owner: %w", err)
	}
	return install(ctx, c, owner)
}

// install creates one GatewayClass per provider, and leaves any that already
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
	logger := log.FromContext(ctx).WithValues("gatewayclass", provider)

	// Nothing but the name and a description: (*Reconciler).class hands
	// class.Name to libtunnel as the provider, so any second spelling of it
	// here could only disagree with the first.
	class := &gatewayv1.GatewayClass{
		ObjectMeta: metav1.ObjectMeta{Name: provider},
		Spec: gatewayv1.GatewayClassSpec{
			ControllerName: ControllerName,
			Description:    ptr.To(consts.GatewayClassDescription),
		},
	}
	// Owned by the namespace, so uninstalling collects the classes too. Only
	// on create: an existing class keeps whatever ownership it already has.
	if owner != nil {
		class.OwnerReferences = []metav1.OwnerReference{*owner}
	}

	err := c.Create(ctx, class)
	switch {
	case err == nil:
		logger.Info("created gatewayclass")
		return nil
	case apierrors.IsAlreadyExists(err):
		// spec.controllerName is immutable, so a class owned by someone else
		// is a real conflict rather than a no-op. Report it instead of
		// pretending the install succeeded.
		var existing gatewayv1.GatewayClass
		if err := c.Get(ctx, client.ObjectKey{Name: provider}, &existing); err != nil {
			return fmt.Errorf("get existing gatewayclass %q: %w", provider, err)
		}
		if existing.Spec.ControllerName != ControllerName {
			logger.Info("gatewayclass exists but names a different controller; leaving it alone",
				"controller", existing.Spec.ControllerName)
			return nil
		}
		logger.Info("gatewayclass already present")
		return nil
	default:
		return fmt.Errorf("create gatewayclass %q: %w", provider, err)
	}
}

// scheme carries the types the installer's own client needs; the manager's
// scheme is not reachable from a Runnable.
//
// clientgoscheme as well as the Gateway API types: resolving the owner reads a
// Namespace and creates a SelfSubjectReview, and a client whose scheme does not
// know a kind refuses to touch it.
func scheme() *runtime.Scheme {
	s := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(s))
	utilruntime.Must(apiextensionsv1.AddToScheme(s))
	utilruntime.Must(gatewayv1.Install(s))
	return s
}
