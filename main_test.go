package main

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/scaffoldly/tunnel/consts"
)

// TestCacheOptionsRestrictPodsOnly pins an asymmetry that looks like an
// oversight and will otherwise be "fixed".
//
// Pods are restricted because they are the most numerous and highest-churn
// object in a cluster. Services are NOT, and must not be: a Service can ask for
// a tunnel through spec.loadBalancerClass, which lives in the spec where no
// label selector can see it, so restricting that cache would silently stop
// delivering those Services and that trigger would just quietly stop working.
func TestCacheOptionsRestrictPodsOnly(t *testing.T) {
	opts := cacheOptions()

	var podSelector labels.Selector
	for obj, byObject := range opts.ByObject {
		switch obj.(type) {
		case *corev1.Pod:
			podSelector = byObject.Label
		case *corev1.Service:
			t.Fatal("the Services cache is restricted; spec.loadBalancerClass would stop being delivered")
		}
	}
	if podSelector == nil {
		t.Fatal("the Pods cache is unrestricted; the informer would hold every pod in the cluster")
	}

	// Exists, not equality: the value chooses a branch and may be any of
	// several, so it is the key being selected on.
	for _, value := range []string{"true", "ingress", "gateway", "none"} {
		if !podSelector.Matches(labels.Set{consts.TunnelLabel: value}) {
			t.Errorf("a pod labelled %s=%q is filtered out", consts.TunnelLabel, value)
		}
	}
	if podSelector.Matches(labels.Set{"run": "nginx"}) {
		t.Error("an unlabelled pod matches, so the restriction buys nothing")
	}
}
