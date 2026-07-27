# install.yaml is rendered from charts/tunnel. Edit the chart, not the manifest.
#
# --namespace is load-bearing: without it .Release.Namespace is "default" and
# the manifest installs there while still applying cleanly.

.PHONY: yaml crds test-e2e

yaml:
	@helm template tunnel charts/tunnel \
	  --namespace tunnel-system \
	  --set namespace.create=true > install.yaml

# Re-vendor the Gateway API CRDs the controller embeds.
#
# Sourced from the module the compiler resolved, not a release URL built from a
# parsed version string. The module ships the same files the release asset is
# built from, so this cannot drift from the types the controller compiles
# against, needs no network, and follows a replace directive if there ever is
# one. go:embed cannot reach into a dependency, hence the copy.
#
# The files carry no leading separator, so one is added between documents. The
# upstream license notice is carried over: this is a vendored copy of someone
# else's Apache-2.0 work, and the release bundle ships that header for a reason.
crds:
	@dir=$$(go list -m -f '{{.Dir}}' sigs.k8s.io/gateway-api); \
	  version=$$(go list -m -f '{{.Version}}' sigs.k8s.io/gateway-api); \
	  { echo "# Copyright The Kubernetes Authors."; \
	    echo "# Licensed under the Apache License, Version 2.0."; \
	    echo "# http://www.apache.org/licenses/LICENSE-2.0"; \
	    echo "#"; \
	    echo "# Gateway API standard channel, $$version."; \
	    echo "# Generated from the sigs.k8s.io/gateway-api module."; \
	    echo "# Do not edit: run \`go generate ./gateway/...\`."; \
	    for f in $$dir/config/crd/standard/*.yaml; do \
	      echo '---'; echo "# source: $$(basename $$f)"; cat "$$f"; \
	    done; } > gateway/crds/zz_generated.standard-install.yaml

# Starts its own kind cluster and deletes it after. Settings live in
# kuttl-test.yaml so `kubectl kuttl test` on its own behaves the same.
test-e2e:
	kubectl kuttl test

