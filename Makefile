# install.yaml is rendered from charts/tunnel. Edit the chart, not the manifest.
#
# --namespace is load-bearing: without it .Release.Namespace is "default" and
# the manifest installs there while still applying cleanly.

.PHONY: yaml

yaml:
	@helm template tunnel charts/tunnel \
	  --namespace tunnel-system \
	  --set namespace.create=true > install.yaml
