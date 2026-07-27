{{/*
Name of every object. Not release-prefixed: three of the five are cluster
scoped, and the served manifest names them all tunnel-controller.
*/}}
{{- define "tunnel.fullname" -}}
{{- printf "%s-controller" .Chart.Name -}}
{{- end }}

{{/*
The only labels on any object. Also the Deployment's immutable selector, so
nothing varying per release or chart version may be added here.
*/}}
{{- define "tunnel.labels" -}}
app.kubernetes.io/name: {{ .Chart.Name }}
{{- end }}

{{/*
The one thing a user reads at the point of applying, since this chart is what
https://tunnel.pizza renders and serves, and the hero page displays it inline.
Emitted by whichever object renders first — the Namespace when namespace.create
is on, otherwise the ServiceAccount — so it stays at the top of the file either
way. Keep it short; explanation belongs in Helm comments, which do not render.
*/}}
{{- define "tunnel.header" -}}
# tunnel — https://github.com/scaffoldly/tunnel
#
# The controller creates two classes at startup, one per tunnel provider:
#
#   tunnel.pizza            named hostnames under tunneled.pizza
#   api.trycloudflare.com   Cloudflare quick tunnels, no account, no us
#
# Opt a workload in with `ingressClassName` or `gatewayClassName: <one of the
# above>`. Neither is a default class, so nothing is claimed implicitly. The
# public hostname appears in the ADDRESS column of `kubectl get ingress`, or in
# `status.addresses` on a Gateway, and is not stable across controller restarts.
#
# A Gateway takes its backend from the HTTPRoutes that name it, so it has no
# address until one exists. Gateway API CRDs are installed if you have none.
{{- end }}
