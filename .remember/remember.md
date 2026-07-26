# Handoff

Invariants are in the agent definition — this file is state and gotchas only.
Where the two overlap, the definition wins; here you get what changed, what was
verified, and what is still missing.

## State — HEAD `f92917d`, pushed, CI green

Three commits total: `45dfcef` scaffold, `a2f8e19` manifest move, `f92917d`
RBAC cut. Working tree clean.

Nothing provisions. A matching Ingress or Gateway gets an `Unimplemented`
warning event; a matching GatewayClass gets `Accepted=False` / reason
`Waiting`. `status.loadBalancer` and `status.addresses` are left empty on
purpose.

8 packages, all building: `.`, `config`, `consts`, `gateway`, `healthz`,
`ingress`, `metrics`, `readyz`.

Verified 2026-07-26 by running, not by assumption:

- `go build ./... && go vet ./... && go test ./...` — clean; `gofmt -l .`
  silent; `go mod tidy` leaves `go.mod`/`go.sum` unchanged; `actionlint` clean.
- `curl -D- https://tunnel.pizza` → `307` → `location:
  https://raw.githubusercontent.com/scaffoldly/tunnel/main/install.yaml`; that
  URL returns `200`, `cache-control: max-age=300`, `content-length: 6783`
  (matches the file on disk).
- `docker buildx imagetools inspect ghcr.io/scaffoldly/tunnel:latest` →
  `application/vnd.oci.image.index.v1+json`, digest
  `sha256:7b18b49307d11cdfcd0fdc75effae3ee1c52caa098535d35a9fbec4d65e18fb7`,
  with `linux/amd64` + `linux/arm64` manifests plus one attestation manifest
  per platform (`unknown/unknown`, `vnd.docker.reference.type:
  attestation-manifest` — those two entries are normal, not junk).

## `install.yaml` now lives here

Moved in `a2f8e19`. `~/scaffoldly/tunnel.pizza/public/install.yaml` is gone —
confirmed absent. Do not recreate it there.

The redirect is **Accept-header conditional**, in
`~/scaffoldly/tunnel.pizza/src/app/page.tsx`: `WANTS_PAGE = /text\/(html|x-component)/i`.
A browser gets the marketing page; `kubectl`/`curl` (`Accept: */*`) gets the
307. So "open tunnel.pizza in a browser" will never show you the manifest, and
a redirect regression will not be visible that way either — test with `curl`.

`MANIFEST` over there tracks `main`, unpinned. Consequence: **any push to main
that touches `install.yaml` reaches every new `kubectl apply` within 300s**,
with no release gate. Pinning to a tag is a deliberate later step, and it is a
cross-repo change (their constant, our tag).

## RBAC was cut 12 rules → 6 (`f92917d`)

Remaining, each traceable to a call site: `ingresses` and `gateways`
(get/list/watch), `ingressclasses` and `gatewayclasses`
(get/list/watch/**create** — create is `--install`, no `update` because a class
naming someone else is left alone), `gatewayclasses/status` (`update` only —
`Status().Update` is a PUT on the subresource), `events.k8s.io/events`
(create/patch).

Removed: `ingresses/status`, `gateways/status`, `httproutes`, `grpcroutes`,
`referencegrants` and their status, `""/services,namespaces`,
`discovery.k8s.io/endpointslices`, `""/events`, `coordination.k8s.io/leases`,
and the whole namespaced Role + RoleBinding (its only purpose was Secret
access). They come back rule-by-rule in the same commit as the code that calls
them.

The two leader-election / events traps from the definition are written down
where they bite: the comment block at the end of the ClusterRole in
`install.yaml`, and the `f92917d` commit message. Read them before you touch
`replicas` or `--leader-elect`.

**Unverified, and worth doing when a cluster is handy:** sufficiency of these
six rules was derived by reading controller-runtime and client-go, not observed.
An `kubectl auth can-i --as=system:serviceaccount:tunnel-system:tunnel-controller`
pass would close it.

## Real gap: zero tests

`go test ./...` passes vacuously — 0 `*_test.go` files across all 8 packages.
CI's `Test` step is therefore green and meaningless.

Two first targets, chosen because both fail on a user's cluster rather than in
CI:

1. **Provider-resolution precedence** — `provider()` in both
   `ingress/ingress.go` and `gateway/gateway.go`. Cover: resource annotation
   beats class annotation beats `consts.DefaultProvider`; empty/nil class name
   → not ours; `IsNotFound` on the class → not ours, no error; class naming a
   different controller → not ours. `sigs.k8s.io/controller-runtime/pkg/client/fake`
   is already in the module graph.
2. **`installed()` no-match path** — `gateway/gateway.go`. The `meta.IsNoMatchError`
   branch must return `(false, nil)`, not an error. Getting this wrong
   crash-loops every Ingress-only cluster and nothing in CI would notice. Needs
   a `RESTMapper` stub, not a fake client.

`upsert()` in `gateway/gateway.go` is also cheap to cover and pure — it decides
whether a status write happens at all, so a bug there is a hot reconcile loop.

## Other loose ends

- `readyz` is `healthz.Ping`, i.e. it says nothing liveness does not already
  say. The honest check is cache sync; controller-runtime exposes no
  non-blocking way to ask. Fine at `replicas: 1`, not fine once anything routes
  to this pod. Reasoning is in the `readyz` package doc.
- Metrics on `:8080` are unauthenticated plaintext and leak namespace/resource
  names via labels. `SecureServing` + a `FilterProvider` before anything
  outside the cluster can reach it. Noted in the `metrics` package doc.
- The implementation seam for both halves is the same call, currently only in
  doc comments: `libtunnel.Cloudflare().WithProvider(provider)`
  (`github.com/cnuss/libtunnel`, not yet a dependency — it is not in `go.mod`).
- No versioned image tags exist yet; CI publishes `:latest` and `:sha-<full>`
  from main, and `type=semver` tags only fire on a `v*` tag push. No `v*` tag
  has been pushed.
- Pinned versions that move together: `k8s.io/* v0.36.1`,
  `controller-runtime v0.24.1`, `gateway-api v1.6.1`, `go 1.26.0` (Dockerfile
  build stage is `golang:1.26-alpine`; bump both together).
