# Handoff

Invariants are in the agent definition — this file is state and gotchas only.
Where the two overlap the definition wins, **except where this file says the
definition is out of date**; one such place is marked below and it matters.

## State — HEAD `6f572e3`, pushed, working tree clean

Tonight, in order: `1b90a58` Ingress provisioning with libtunnel, `28b8f31`
served ports in status, `21e9ba0` ownerReferences on installed classes,
`4a18f9f` kuttl scaffold + `.dockerignore`, `a93e3e8` Ingress e2e, `1cf85ed`
bundled Gateway API CRDs + `--install` split into three, `b4143ff` CRD bundle
generated from the module, `6f572e3` Gateway provisioning.

**Both halves provision.** Ingress and Gateway each mint a real tunnel and
publish the hostname — Ingress to `status.loadBalancer.ingress[].hostname`,
Gateway to `status.addresses[]` (type `Hostname`). Nothing is a stub any more.
Anything in the tree that still says "stub", "scaffold" or "Unimplemented" is
prose that did not keep up; the list is at the bottom and it includes the
manifest users apply.

Ten packages: `.`, `config`, `consts`, `gateway`, `healthz`, `ingress`,
`metrics`, `readyz`, `tunnels`, plus `charts/tunnel`. `tunnels` is new — the
tunnel store moved there out of `ingress/tunnel.go`, because both halves need
it and neither owns it. `tunnels.Dial` takes a `metav1.Object`, so an
`IngressClass` and a `GatewayClass` both satisfy it; the class's **name** is the
provider host, and that is the whole contract with libtunnel.

## The provider contract changed — the definition is stale on this

There is no `tunnel.pizza/provider` annotation and no `consts.DefaultProvider`.
Both were deleted in `1b90a58`. The definition's "annotation on the resource,
then on its class, then `consts.DefaultProvider`" describes code that no longer
exists.

What is true now: **the class is the whole configuration.** A class is *named*
for the provider host it mints from, and `--install` creates one per provider
(`consts.InstalledProviders` — `tunnel.pizza`, then `api.trycloudflare.com`).
`(*Reconciler).class` in each half resolves the named class, checks
`spec.controller` / `spec.controllerName` against `ControllerName`, and returns
it; `class.Name` is then the provider. There is deliberately no annotation on
the installed classes, because the name already says it and two spellings can
disagree.

The definition's actual invariant — provider is inferred, never a flag — still
holds. Only the mechanism moved. Do not "restore" the cascade on the strength
of the definition alone; ask first.

## Origin resolution is where the two halves genuinely differ

`ingress/origin.go`: an Ingress names its backend inline (`spec.defaultBackend`
plus every rule path backend). Reduce to one distinct Service or refuse.
Resource backends refused. Port may be a name or a number.

`gateway/origin.go`: a Gateway names **no** backend. Routes attach to it, so
the origin is whatever `HTTPRoute`s in the Gateway's namespace name it as a
parent (`attaches()` honours the Gateway API's defaulting: a parentRef with no
namespace means the route's own, not "any"). Consequences worth knowing:

- A Gateway with no HTTPRoute has nothing to point a tunnel at. It gets an
  `Unsupported` warning event, not an error, and no address. This is normal and
  transient — the e2e applies Gateway and route together and the first
  reconcile logs exactly that, then succeeds seconds later once the route lands.
- Non-Service backends, non-core groups and portless backendRefs are refused.
- Cross-namespace backendRefs are refused outright rather than followed:
  honouring one without checking a `ReferenceGrant` is a confused deputy.
  `referencegrants` is deliberately not in the RBAC.
- `HTTPRoute`s are watched and mapped to their parent Gateways (`routeParents`),
  because a Gateway's origin changes when its routes do without the Gateway
  itself being touched.

Both halves then confirm the Service exposes the port through the manager's
**uncached** reader (`mgr.GetAPIReader()`), so no informer over every Service in
the cluster. One tunnel fronts one origin in both halves; fan-out is refused
rather than silently misrouted.

## `--install` is three flags now

`--install-ingress-classes`, `--install-gateway-classes`,
`--install-gateway-api`, all defaulting true (`consts.Flag*`, `config.Config`).
Chart values `install.ingressClasses` / `.gatewayClasses` / `.gatewayAPI`; the
Deployment renders an arg only to turn one **off**.

`--install-gateway-api` is the one with real blast radius: it writes
cluster-scoped CRDs that every Gateway API implementation in the cluster reads.
A cluster running Istio or Cilium turns that one off and keeps the others.

- `decide()` in `gateway/crds.go` is upstream's bundling ruleset and nothing
  else: never overwrite an unrecognized or newer version, never overwrite a
  different channel, never remove. Anything but strictly-older-same-channel is
  left alone. Biased toward "no" on purpose — a wrong no leaves our Gateway half
  off, a wrong yes breaks someone else's.
- **Rule 3 is enforced by RBAC, not by code.** The ClusterRole has
  `get/list/watch/create/update` on `customresourcedefinitions` and no `delete`.
  Deleting Gateway API CRDs destroys every Gateway object in the cluster, so
  withholding the verb means a bug cannot do it. Do not add it.
- Install runs in `gateway.New()` **before** the `installed()` capability probe,
  not as a Runnable: the probe decides whether anything registers, and a
  Runnable would not run until the manager had already started without those
  watches. It then blocks in `awaitEstablished` on `gatewayclasses` and
  `gateways` only — a CRD exists before it is servable, and controller-runtime
  fails to *start* a manager watching a kind the API server does not serve.
- Class installers (both halves) are still Runnables with their own clients, for
  the reason in the definition.

## The CRD bundle is generated

`gateway/crds/zz_generated.standard-install.yaml`, embedded via `go:embed`.
Regenerate with `go generate ./gateway/...`, which shells to `make crds`, which
copies `$(go list -m -f '{{.Dir}}' sigs.k8s.io/gateway-api)/config/crd/standard/*.yaml`.

Sourced from the module the compiler resolved, **not** a release URL: no
network, no chance of the file and the compiled types naming different
releases, and it follows a `replace` if there ever is one. `go:embed` cannot
reach into a dependency, hence the vendored copy; the upstream Apache notice is
carried deliberately. `TestBundleVersionMatchesGoMod` pins `bundledVersion`
(`v1.6.1`) against the file, so `make crds` after a go.mod bump is not something
anyone has to remember. `.dockerignore` is an allow-list and re-includes
`gateway/crds/zz_generated.*.yaml` by negation — that is why `Dockerfile` line 1
is `# check=skip=CopyIgnoredFile`.

## e2e: kuttl, `make test-e2e`, both suites green

`kuttl-test.yaml` starts its own kind cluster (`tunnel-e2e`), builds the working
tree, side-loads the image, `helm upgrade --install`s the chart from this tree
with `--wait`, and asserts. `tests/e2e/ingress` and `tests/e2e/gateway` each:
assert the two classes exist with the reflection-derived `spec.controller` /
`spec.controllerName`, apply nginx behind an Ingress/Gateway, assert status,
then `curl` the published address from the public internet over **both** http
and https.

- **Fixed `:e2e` tag, never `:latest`.** Kubernetes infers `imagePullPolicy`
  from the tag and `:latest` infers `Always`, which would discard the loaded
  image and pull the published one. `pullPolicy=Never` on top so a missing local
  image fails fast with `ErrImageNeverPull`.
- The suite installs **no** Gateway API CRDs. That the gateway suite's
  `00-assert` passes on a bare kind cluster is the proof that
  `--install-gateway-api` works, including that the manager's RESTMapper picks
  the new kinds up after `awaitEstablished`.
- `parallel: 1`, `timeout: 180`. Both suites mint **real** tunnels against the
  live, still-unauthenticated `POST /tunnel` and drive public traffic through
  them. Serial because the mint is the long pole either way and concurrency only
  doubled the load on that endpoint.
- The http fetch is not redundant: the edge answers plaintext on 80 rather than
  redirecting, to match a Cloudflare quick tunnel. Turning on "Always Use HTTPS"
  for the zone would make it a 301 with no body and fail here. That was a
  dashboard setting with nothing recording it.

### Traps that cost real time tonight

- **Never pipe `make test-e2e` into `head`.** It takes SIGPIPE and orphans the
  kind cluster; the next run dies with `KIND is already running`. Recovery:
  `kind delete cluster --name tunnel-e2e`. Redirect to a file and read that.
- kuttl writes `./kubeconfig` in the working directory, is not configurable
  about it, and does not clean it up — it survives the cluster it points at, so a
  stale one gives `connection refused` on a port nothing serves. Gitignored,
  along with `.kuttl/`.
- **macOS `sed` has no `\b`.** Word-boundary substitutions silently do nothing.
  Use perl.
- **`gofmt -l . && echo "clean"` reports clean even when it lists files** — it
  exits 0 either way. A commit went out unformatted because of that. CI now
  captures the output and tests it for emptiness; do the same locally.

## Verified 2026-07-27 by running, not by assumption

- `go build ./... && go vet ./... && go test ./...` — clean. `gofmt -l .` prints
  nothing. `go mod tidy` leaves `go.mod`/`go.sum` unchanged. `actionlint` clean.
- `docker buildx build --platform linux/amd64,linux/arm64 --output=type=cacheonly .`
  — succeeds, cross-compiled from one native build stage via TARGETOS/TARGETARCH.
- `make test-e2e` — `PASS: kuttl (71.39s)`, `gateway (15.61s)`, `ingress (19.63s)`.
  Real hostnames minted (`respective-coyote.tunneled.pizza`,
  `mere-earthworm.tunneled.pizza`) and both schemes served nginx.
- `curl -sI -H 'Accept: */*' https://tunnel.pizza` → `307` →
  `raw.githubusercontent.com/scaffoldly/tunnel/main/install.yaml`, which returns
  `200`, `cache-control: max-age=300`. The served body is byte-identical to
  `install.yaml` on disk (4132 bytes).

The old open question — whether the cut-down RBAC is actually sufficient — is
**closed by observation**. The e2e installs the chart's real ClusterRole and
both halves complete: classes created with ownerReferences, CRDs installed,
status written on both kinds, events emitted. No `kubectl auth can-i` sweep
needed.

## `install.yaml` is generated from the chart

`charts/tunnel` is the source of truth; `make yaml` renders `install.yaml` with
`--namespace tunnel-system --set namespace.create=true`. CI fails on a stale
manifest ("Verify install.yaml matches the chart"). Edit the chart, never the
manifest. `--namespace` is load-bearing: without it `.Release.Namespace` is
`default` and the manifest installs there while still applying cleanly.

The redirect is Accept-header conditional, in `~/scaffoldly/tunnel.pizza`
(`src/app/page.tsx`, `WANTS_PAGE = /text\/(html|x-component)/i`). A browser gets
the marketing page; `kubectl`/`curl` get the 307. Test with `curl`, never a
browser. `MANIFEST` over there tracks `main`, unpinned — **any push to main that
touches `install.yaml` reaches every new `kubectl apply` within 300s**, with no
release gate. Pinning to a tag is a cross-repo change.

CI ignores `**.md`, `.remember/**` and `LICENSE*` on both `push` and
`pull_request`, so a docs-only or handoff-only commit produces **no run at all**
— not a green one. Do not go looking for it.

## Prose that is now wrong, in rough order of who it hurts

This is the actively-misleading category, not merely stale. All of it says the
Gateway half does not work, or documents the deleted annotation.

1. `charts/tunnel/templates/_helpers.tpl`, `tunnel.header` — "Gateway API is not
   implemented yet: matching Gateways get an Unimplemented event and no tunnel."
   This renders into `install.yaml` and is **served at https://tunnel.pizza and
   displayed inline on the hero page**. Widest blast radius of anything here.
2. `README.md` — "**Gateway API is still a stub**"; the whole "Providers"
   section (three-step annotation cascade); the "Install flag" section (singular
   `--install`, which no longer exists).
3. `charts/tunnel/templates/NOTES.txt` — "Gateways are still a scaffold"; the
   `tunnel.pizza/provider` override at the end.
4. `charts/tunnel/templates/serviceaccount.yaml` — the "Provider override, on
   the resource or on its class" block.
5. `main.go` package doc — "The Gateway API half is still a stub". (Its second
   clause, that GatewayClasses report `Accepted=False`, is still true — below.)
6. `ingress/ingress.go` package doc — "resolve the provider host from the
   annotation cascade".
7. `consts/consts.go` on `InstalledProviders` — "the annotation cascade reads the
   value off the class rather than inferring it from the name". Exactly inverted:
   it infers from the name.
8. `ingress/ingress_class.go` and `gateway/gateway_class.go`, both at the class
   literal — "(*Reconciler).provider falls back to the class's own name". There
   is no `provider` method any more; it is `class()`.
9. `charts/tunnel/templates/clusterrole.yaml` — the `ingressclasses` note says
   classes resolve to "a controller and a provider annotation"; the
   `gatewayclasses/status` note says "until provisioning exists".
10. `tests/e2e/gateway/00-assert.yaml` — "the suite installs the CRDs before the
    chart for exactly this reason". It deliberately does not; that is the point
    of the test.
11. `consts.ReasonUnimplemented` and `consts.MsgUnimplementedFmt` are dead — no
    references outside `consts`.

## Two real gaps

**The GatewayClass still reports `Accepted=False` / `Waiting` / "tunnel
provisioning is not implemented yet"** (`ClassReconciler.Reconcile`,
`gateway/gateway.go`) while Gateways on that very class provision and serve.
`kubectl describe gatewayclass tunnel.pizza` contradicts `kubectl get gateway`.
Nothing catches it: the Gateway reconciler never reads the condition and no test
asserts on it. Gateway API requires the implementing controller to publish
`Accepted`, and publishing False when it works is arguably worse than not
publishing — a conformant consumer may refuse the class. This is the first thing
to fix, and it is a two-line change plus a test.

**The gateway half has no unit tests for the code that provisions.**
`gateway/` has `crds_test.go`, `gateway_class_test.go`, `reporter_test.go` — and
nothing for `Reconciler.Reconcile`, `origin.go` (`single`, `attaches`, `port`)
or `routeParents`. The ingress half has seven reconcile tests plus
`origin_test.go` and `class_test.go`; `tunnels` has six store tests.
`tunnels/testing.go` exports `NewFake` and `NewTestStore` in a non-test file
precisely so the gateway tests can use them, and nothing does yet. `attaches`'s
namespace defaulting and `single`'s cross-namespace refusal are the two worth
writing first — both are pure, and both fail on a user's cluster rather than in
CI.

## Loose ends carried forward, still true

- `readyz` is `healthz.Ping`, i.e. it says nothing liveness does not. The honest
  check is cache sync; controller-runtime exposes no non-blocking way to ask.
  Fine at `replicas: 1`. Reasoning is in the package doc.
- Metrics on `:8080` are unauthenticated plaintext and leak namespace/resource
  names via labels. `SecureServing` + `FilterProvider` before anything outside
  the cluster can reach it.
- `replicas: 1` with `strategy: Recreate`, and `--leader-elect` defaults false.
  Both are load-bearing: two controllers minting for one object leak a tunnel per
  reconcile, and `maxSurge` rounds up to one surge pod at `replicas: 1`. Turning
  leader election on needs `coordination.k8s.io/leases` **and** core `""` events
  back — the lock recorder is the deprecated core-v1 path.
- No `v*` tag has been pushed; CI publishes `:latest` and `:sha-<full>` from main,
  and the `type=semver` tags only fire on a tag push.
- Pinned versions that move together: `k8s.io/* v0.36.1` (`apiextensions-apiserver
  v0.36.0`), `controller-runtime v0.24.1`, `gateway-api v1.6.1`, `libtunnel
  v0.0.37`, `go 1.26.0` (Dockerfile stage `golang:1.26-alpine`; bump both).
  Bumping gateway-api means `go generate ./gateway/...` and a new
  `bundledVersion`.
