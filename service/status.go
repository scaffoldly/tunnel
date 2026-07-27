package service

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// publish writes the tunnel hostname to the Service's status, and reports
// whether it had to. An empty hostname clears it.
//
// Only ever called for a Service reached through spec.loadBalancerClass. That
// is not a preference: the API server rejects a status.loadBalancer write on a
// Service whose spec.type is not LoadBalancer, and has since v1.29. On the
// annotation path there is nothing to write and nothing this could write.
//
// No ip, deliberately. A hostname alone is a complete LoadBalancerIngress —
// it is how every ELB-backed Service has always looked, and it is what makes
// EXTERNAL-IP render — while an ip would have to be one of Cloudflare's shared
// anycast addresses, which route on SNI and are useless on their own. Setting
// ip would also make ipMode mandatory, forcing a VIP/Proxy claim about an
// address this controller does not own.
func (r *Reconciler) publish(ctx context.Context, svc *corev1.Service, hostname string) (bool, error) {
	var want []corev1.LoadBalancerIngress
	if hostname != "" {
		want = []corev1.LoadBalancerIngress{{
			Hostname: hostname,
			// Both ports, matching what the Ingress half publishes: the edge
			// answers plaintext on 80 as well as TLS on 443, deliberately, so
			// a tunnel hostname behaves the same whichever provider minted it.
			//
			// These are the ports the tunnel serves, not the Service's own —
			// a Service listening on 8080 is still reached on 80 and 443 from
			// outside. The API server does not check the two against each
			// other, which was confirmed rather than assumed.
			//
			// PortStatus.Error is left unset: it is an error channel, not a
			// description, and a value there means this port is broken.
			Ports: []corev1.PortStatus{
				{Port: 80, Protocol: corev1.ProtocolTCP},
				{Port: 443, Protocol: corev1.ProtocolTCP},
			},
		}}
	}

	// DeepEqual over the whole slice rather than a field-by-field comparison.
	// The Ingress half shipped a bug where a hand-written check ignored Ports,
	// so an existing hostname never backfilled and nothing said so.
	if apiequality.Semantic.DeepEqual(svc.Status.LoadBalancer.Ingress, want) {
		return false, nil
	}

	// A merge patch, not an Update: the RBAC grants patch on services/status
	// and nothing on services itself, so the Service's spec and metadata are
	// unreachable from here even by mistake. That split is the decision about
	// what this controller writes, expressed where it cannot quietly erode.
	before := svc.DeepCopy()
	svc.Status.LoadBalancer.Ingress = want
	if err := r.Status().Patch(ctx, svc, client.MergeFrom(before)); err != nil {
		return false, fmt.Errorf("patch service status %s: %w", client.ObjectKeyFromObject(svc), err)
	}
	return true, nil
}
