# install.yaml is rendered from charts/tunnel. Edit the chart, not the manifest.
#
# --namespace is load-bearing: without it .Release.Namespace is "default" and
# the manifest installs there while still applying cleanly.

.PHONY: yaml test-e2e

yaml:
	@helm template tunnel charts/tunnel \
	  --namespace tunnel-system \
	  --set namespace.create=true > install.yaml

# Starts its own kind cluster and deletes it after. Settings live in
# kuttl-test.yaml so `kubectl kuttl test` on its own behaves the same.
test-e2e:
	kubectl kuttl test
