# Handoff

Invariants are in the agent definition — this file is state and gotchas only.
Where the two overlap the definition wins, **except where this file says the
definition is out of date**. The one place that mattered — the provider
annotation cascade — has since been corrected in the definition itself.

## State — HEAD `5250558`, pushed, CI green, working tree clean

In order: `1b90a58` Ingress provisioning with libtunnel, `28b8f31`
served ports in status, `21e9ba0` ownerReferences on installed classes,
`4a18f9f` kuttl scaffold + `.dockerignore`, `a93e3e8` Ingress e2e, `1cf85ed`
bundled Gateway API CRDs + `--install` split into three, `b4143ff` CRD bundle
generated from the module, `6f572e3` Gateway provisioning, `502deca`
GatewayClass Accepted + its tests, `5250558` the prose sweep and the
regenerated manifest.

**Both halves provision.** Ingress and Gateway each mint a real tunnel and
publish the hostname — Ingress to `status.loadBalancer.ingress[].hostname`,
Gateway to `status.addresses[]` (type `Hostname`). Nothing is a stub any more,
and as of `5250558` nothing in the tree says otherwise: the eleven-item list
of wrong prose that used to live at the bottom of this file is cleared.

Ten packages: `.`, `config`, `consts`, `gateway`, `healthz`, `ingress`,
`metrics`, `readyz`, `tunnels`, plus `charts/tunnel`. `tunnels` is new — the
tunnel store moved there out of `ingress/tunnel.go`, because both halves need
it and neither owns it. `tunnels.Dial` takes a `metav1.Object`, so an
`IngressClass` and a `GatewayClass` both satisfy it; the class's **name** is the
provider host, and that is the whole contract with libtunnel.

## The provider contract — the annotation cascade is gone for good

There is no `tunnel.pizza/provider` annotation and no `consts.DefaultProvider`.
Both were deleted in `1b90a58`. The agent definition described a three-step
cascade for a while after that; it has since been corrected, and the correction
is the authority. If a cascade ever reappears in a prompt, it is a stale copy.

What is true: **the class is the whole configuration.** A class is *named*
for the provider host it mints from, and the class install flags create one per
provider (`consts.InstalledProviders` — `tunnel.pizza`, then
`api.trycloudflare.com`).
`(*Reconciler).class` in each half resolves the named class, checks
`spec.controller` / `spec.controllerName` against `ControllerName`, and returns
it; `class.Name` is then the provider. There is deliberately no annotation on
the installed classes, because the name already says it and two spellings can
disagree.

The invariant — provider is inferred, never a flag — still holds. Only the
mechanism moved. Do not "restore" the cascade; the deleted annotation code is
not coming back.

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

### Traps that cost real time

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
- `make test-e2e` — `PASS: kuttl (83.14s)`, `gateway (21.73s)`, `ingress (20.67s)`,
  including the new GatewayClass `Accepted` assert. Real tunnels minted and both
  schemes served nginx. An earlier run before the sweep: `71.39s` / `15.61s` /
  `19.63s`.
- The negative run matters as much: with `00-assert` deliberately set back to
  `False`/`Waiting`, `kubectl kuttl test --test gateway` fails in `step 0` with
  `.status.conditions.status: value mismatch, expected: False != actual: True`
  and never reaches the mint. Cheap to repeat, costs the provider nothing.
- `curl -sI -H 'Accept: */*' https://tunnel.pizza` → `307` →
  `raw.githubusercontent.com/scaffoldly/tunnel/main/install.yaml`, which returns
  `200`, `cache-control: max-age=300`. The served body is byte-identical to
  `install.yaml` on disk, re-checked after `5250558` went out.
- CI run `30240811031` on `5250558`: `check` and `publish` both green, including
  "Verify install.yaml matches the chart".

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

## The prose sweep is done — how it was wrong, so it does not recur

Eleven places said the Gateway half did not work, or documented the deleted
annotation; all eleven are fixed in `502deca` and `5250558`. The pattern is
worth keeping: **every one of them was written in the same commit as code that
was true at the time, and none of them had a test.** Prose about a capability
ages the moment the capability lands, and nothing in this repo checks it.

The rule that replaced them all, used verbatim wherever an annotation used to
be documented: *a class is named for the host it mints from, and choosing a
class is the whole configuration.* Reuse that sentence rather than inventing a
paraphrase.

`tunnel.header` in `charts/tunnel/templates/_helpers.tpl` is the one to be
careful with — it renders into `install.yaml`, is served from
https://tunnel.pizza and shown inline on the hero page, and reaches every new
`kubectl apply` within 300s of a push to main. Current wording states two
things a user needs before applying and neither of which was there before: a
Gateway takes its backend from the HTTPRoutes that name it (so it has no
address until one exists), and the Gateway API CRDs are installed if they have
none.

## One real gap left, and one spec MUST not implemented

**`SupportedVersion` is never published.** Gateway API says a controller that
marks a GatewayClass `Accepted` MUST also set `SupportedVersion`
(`gatewayv1.GatewayClassConditionStatusSupportedVersion`, reasons
`SupportedVersion` / `UnsupportedVersion`). `502deca` deliberately did not add
it: doing it honestly means reading the CRDs' `bundle-version` annotation at
runtime and deciding a support policy against `bundledVersion`, which is a
decision, not a two-line change. The RBAC already allows the read
(`customresourcedefinitions` get/list/watch). Until it lands we are accepting
classes without the companion condition the spec requires.

The `Accepted` condition itself is now correct and guarded: `Accepted=True`,
reason `Accepted`, `observedGeneration`, message naming the provider. Tests in
`gateway/gateway_test.go` plus the gateway e2e `00-assert`. Both were checked
by mutation, and the e2e one fails at step 0 before any tunnel is minted, so
breaking it costs no provider load. **Assertions there are written against the
literal `"Accepted"`, not `gatewayv1.GatewayClassReasonAccepted`** — an
assertion phrased in the code's own constants would have passed just as
happily against the `False`/`Waiting` condition that shipped for a release.
`TestAcceptedConditionMatchesTheSpec` pins the literals to upstream.

**The gateway half still has thin unit tests for the code that provisions.**
`gateway/` has `crds_test.go`, `gateway_class_test.go`, `reporter_test.go` and
now `gateway_test.go` — but the last covers `ClassReconciler` only. Still
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
