# install.yaml is rendered from charts/tunnel. Edit the chart, not the manifest.
#
# --namespace is load-bearing: without it .Release.Namespace is "default" and
# the manifest installs there while still applying cleanly.

.PHONY: yaml crds test-e2e

yaml:
	@helm template tunnel charts/tunnel \
	  --namespace tunnel-system \
	  --set namespace.create=true > install.yaml

# Re-vendor the Gateway API CRDs the controller embeds, at whatever version
# go.mod pins. Installing a schema the controller cannot deserialize is the
# failure this prevents.
crds:
	@version=$$(awk '$$1 == "sigs.k8s.io/gateway-api" {print $$2}' go.mod); \
	  curl -fsSL "https://github.com/kubernetes-sigs/gateway-api/releases/download/$$version/standard-install.yaml" \
	    -o gateway/crds/standard-install.yaml

# Starts its own kind cluster and deletes it after. Settings live in
# kuttl-test.yaml so `kubectl kuttl test` on its own behaves the same.
test-e2e:
	kubectl kuttl test
