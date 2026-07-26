# Handoff

## State
Scaffolded 2026-07-26. Public repo. Builds, vets, gofmt-clean; multi-arch image verified locally (amd64 + arm64 native ELF, 45.6MB). **Nothing is implemented** — matching Ingresses and Gateways get an `Unimplemented` warning event, GatewayClass gets `Accepted=False` / `Waiting`. CI publishes `ghcr.io/scaffoldly/tunnel:latest` from main.

## Next
1. Implement provisioning in `ingress/ingress.go` and `gateway/gateway.go`. Both `provider()` funcs already return the host; the seam is `libtunnel.Cloudflare().WithProvider(provider)`.
2. Decide what `status` should carry once tunnels are real — Ingress `status.loadBalancer`, Gateway `status.addresses`, both deliberately left empty rather than advertising an address that does not serve.
3. Leader election is off by default. Turn it on before `replicas > 1`, or two replicas mint a duplicate tunnel per reconcile.

## Context
- **Package path is the contract.** `ControllerName` is `reflect.TypeFor[Reconciler]().PkgPath()`, so `spec.controller` / `spec.controllerName` in the manifest must equal the Go import path. `Name` is `path.Base` of it. Never move these into `consts` — they would report the wrong package. Lease ID derives from `debug.ReadBuildInfo().Main.Path`.
- **The manifest lives in the other repo**: `~/scaffoldly/tunnel.pizza/public/install.yaml`, served at `https://tunnel.pizza`. A rename here silently stales it. It carries namespace + SA + RBAC + Deployment and **no classes**.
- **`--install` (default true)** creates the default IngressClass and GatewayClass at startup, both named `tunnel.pizza`. Implemented as a `Runnable` per package that builds its **own** client — the manager's client reads through a cache that has not synced when a Runnable starts. Create is idempotent; a class naming a different controller is left alone, since `spec.controller` is immutable.
- **Provider is inferred, no flag**: `tunnel.pizza/provider` annotation on the resource → same annotation on its class → `consts.DefaultProvider` (`tunnel.pizza`). Verified live: `POST https://tunnel.pizza/tunnel` returns 200 with real credentials.
- **Gateway controllers register only if the CRDs exist** (`gateway.installed`). controller-runtime fails to *start* if it watches an unserved kind, so an Ingress-only cluster would crash-loop otherwise.
- Version alignment matters: k8s.io 0.36.1 + controller-runtime 0.24.1 + gateway-api 1.6.1 move together. Older controller-runtime fails on `HasSyncedChecker`.
- Events use the **new** API (`k8s.io/client-go/tools/events`, `mgr.GetEventRecorder`). Signature is `Eventf(regarding, related, type, reason, action, note, ...)` — `related` is nil, `action` is required. RBAC needs `events.k8s.io` as well as `""`.
- Verify with `actionlint -no-color -oneline .github/workflows/main.yml` and `docker buildx build --platform linux/amd64,linux/arm64 --output=type=cacheonly .` — both installed and passing.
