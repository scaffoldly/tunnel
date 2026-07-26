# tunnel

Kubernetes controllers giving a cluster public reachability through a tunnel
provider — no port forwarding, no DynDNS, no router configuration, and it works
from behind carrier-grade NAT where port forwarding is impossible.

**Ingress works.** A claimed Ingress gets a tunnel to its backend Service, and
its public hostname appears in `status.loadBalancer.ingress[].hostname` — the
ADDRESS column of `kubectl get ingress`. The tunnels are ephemeral quick
tunnels held in the controller process, so the hostname changes whenever the
controller restarts.

**Gateway API is still a stub.** Matching Gateways receive an `Unimplemented`
warning event and no tunnel is created.

## Install

```bash
kubectl apply -f https://tunnel.pizza
```

Then set `ingressClassName: tunnel.pizza` (or `gatewayClassName`) on your
resource. This is not a default class, so nothing is claimed implicitly.

## Layout

One binary serves both APIs.

| Path | Purpose |
|---|---|
| `main.go` | Manager entrypoint; registers both controllers |
| `ingress/` | Ingress — `github.com/scaffoldly/tunnel/ingress` |
| `gateway/` | Gateway API — `github.com/scaffoldly/tunnel/gateway` |

`spec.controller` on an IngressClass, and `spec.controllerName` on a
GatewayClass, are the Go import paths of the packages implementing them, so a
manifest and its implementation cannot drift.

Gateway API CRDs are not installed on every cluster, and a manager that watches
a kind the API server does not serve fails to start. The Gateway controllers
are therefore registered only when the CRDs are present, so an Ingress-only
cluster does not crash-loop.

## Providers

The provider is the host tunnels are minted from — `tunnel.pizza` means
`https://tunnel.pizza/tunnel` — and it is what the controller hands to
[libtunnel](https://github.com/cnuss/libtunnel):

```go
libtunnel.Cloudflare().WithProvider(provider)
```

It is inferred, most-specific first:

1. `tunnel.pizza/provider` annotation on the Ingress or Gateway
2. the same annotation on its IngressClass or GatewayClass
3. the built-in default, `tunnel.pizza`

There is no flag. A cluster defaults to `tunnel.pizza` and sends individual
workloads elsewhere by annotating them, without needing a class per provider.

## Install flag

`--install` (default true) has the controller create default IngressClasses and
GatewayClasses at startup, named `tunnel.pizza`. That keeps the shipped
manifest to a Deployment and its RBAC, and keeps a GatewayClass out of a file
that must apply on clusters without the Gateway API CRDs.

Turn it off when the classes are managed by Helm, Argo, or anything else that
would fight over ownership.

## Development

```bash
go build ./...
go run . --kubeconfig ~/.kube/config
```

`install.yaml` is rendered from `charts/tunnel` — edit the chart, not the
manifest, then regenerate. CI fails on a stale manifest.

```bash
make yaml
```
