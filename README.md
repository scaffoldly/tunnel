# tunnel

Kubernetes controllers giving a cluster public reachability through a tunnel
provider — no port forwarding, no DynDNS, no router configuration, and it works
from behind carrier-grade NAT where port forwarding is impossible.

**Ingress.** A claimed Ingress gets a tunnel to its backend Service, and its
public hostname appears in `status.loadBalancer.ingress[].hostname` — the
ADDRESS column of `kubectl get ingress`.

**Gateway API.** A claimed Gateway gets a tunnel to the Service its HTTPRoutes
name, published to `status.addresses` as a `Hostname`, and the GatewayClass
reports `Accepted`. A Gateway names no backend itself, so one with no route
attached yet has no address. The CRDs are installed if the cluster has none.

The tunnels are ephemeral quick tunnels held in the controller process, so the
hostname changes whenever the controller restarts.

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

It is never a flag, and never an annotation: **the class is the provider.** A
class is named for the host it mints from, so picking a class is the whole
choice, and `kubectl get ingressclass` reads as the list of control planes this
cluster can reach. Two are created at startup:

| Class | Mints from |
|---|---|
| `tunnel.pizza` | named hostnames under `tunneled.pizza` |
| `api.trycloudflare.com` | Cloudflare quick tunnels — no account, no us |

For a different control plane, create a class named for that host with
`spec.controller` (or `spec.controllerName`) set to the import path above.
Nothing else selects one, so there is no second spelling to disagree with the
name.

## Install flags

Three, all defaulting to true, because their blast radii differ:

| Flag | Creates |
|---|---|
| `--install-ingress-classes` | one IngressClass per provider |
| `--install-gateway-classes` | one GatewayClass per provider |
| `--install-gateway-api` | the Gateway API CRDs, if the cluster has none |

The class flags keep the shipped manifest down to a Deployment and its RBAC,
and keep a GatewayClass out of a file that must apply on clusters without the
Gateway API CRDs. Turn them off when the classes are managed by Helm, Argo, or
anything else that would fight over ownership.

`--install-gateway-api` writes cluster-scoped CRDs that every Gateway API
implementation in the cluster reads. A cluster already running Istio or Cilium
should turn that one off and keep the others. The controller never deletes or
downgrades them, and it lacks the RBAC to do so.

## Install a pinned version

The unversioned install tracks `main`. To pin one:

```bash
helm repo add tunnel https://scaffoldly.github.io/tunnel
helm install tunnel tunnel/tunnel --version 0.1.0 \
  --namespace tunnel-system --create-namespace
```

The same chart is published to `oci://ghcr.io/scaffoldly/charts/tunnel`. Both
exist because listing versions on a registry needs authentication and the Helm
repository's `index.yaml` does not.

A chart version pins its image version: `image.tag` defaults to the chart's
`appVersion`, so `--version 0.1.0` installs `ghcr.io/scaffoldly/tunnel:0.1.0`.

## Development

```bash
go build ./...
go run . --kubeconfig ~/.kube/config
```

### What gets served

`https://tunnel.pizza` reads `index.yaml` from the Helm repository, picks the
newest stable release and renders that tarball — so what a user applies is a
versioned chart, and tagging is what ships it. There is no committed manifest.
To see what a user gets:

```bash
helm template tunnel charts/tunnel --namespace tunnel-system --set namespace.create=true
```
