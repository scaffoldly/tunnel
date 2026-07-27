# Handoff

Invariants are in the agent definition — this file is state and gotchas only.
Where the two overlap the definition wins, **except where this file says the
definition is out of date**. The one place that mattered — the provider
annotation cascade — has since been corrected in the definition itself.

## State — HEAD `5d3db56`, pushed, CI green, working tree clean

In order: `1b90a58` Ingress provisioning with libtunnel, `28b8f31`
served ports in status, `21e9ba0` ownerReferences on installed classes,
`4a18f9f` kuttl scaffold + `.dockerignore`, `a93e3e8` Ingress e2e, `1cf85ed`
bundled Gateway API CRDs + `--install` split into three, `b4143ff` CRD bundle
generated from the module, `6f572e3` Gateway provisioning, `502deca`
GatewayClass Accepted + its tests, `5250558` the prose sweep and the
regenerated manifest, `9f7e5d0` GatewayClass SupportedVersion, `5c40e97`
provider resolution for annotation-driven tunnels (phase 1), `5d3db56` the
Service controller and its e2e (phase 2).

**Both halves provision.** Ingress and Gateway each mint a real tunnel and
publish the hostname — Ingress to `status.loadBalancer.ingress[].hostname`,
Gateway to `status.addresses[]` (type `Hostname`). Nothing is a stub any more,
and as of `5250558` nothing in the tree says otherwise: the eleven-item list
of wrong prose that used to live at the bottom of this file is cleared.

Eleven packages: `.`, `config`, `consts`, `gateway`, `healthz`, `ingress`,
`metrics`, `readyz`, `service`, `tunnels`, plus `charts/tunnel`. `service` is
newest: it turns a Service that asks for a tunnel into a child Ingress that
gets one. `tunnels` came before it — the
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

## Annotation-driven tunnels: what phase 0 settled against a real cluster

The design is `designs/annotation-driven-tunnels.md` in the coordinator's repo,
not this one. Phase 0 was one factual question and it came back **no**, which
the design did not expect. Everything here was observed on kind, server
**v1.35.0**, not read out of the source.

- **`status.loadBalancer.ingress[]` cannot be written to a Service whose
  `spec.type` is not `LoadBalancer`.** `kubectl patch --subresource=status`
  against a ClusterIP *and* a NodePort Service both fail with
  `status.loadBalancer.ingress: Forbidden: may only be used when 'spec.type' is
  'LoadBalancer'`. The same patch on a `type: LoadBalancer` Service succeeds.
- **There is no version where the alternative worked.** The validation is
  kubernetes/kubernetes#119789, milestone **v1.29**. Its gate
  `AllowServiceLBStatusOnNonLB` was off by default then and is gone now — the
  1.35 apiserver publishes 218 `kubernetes_feature_enabled` series and none
  matches `ServiceLB`. And per that PR, *before* 1.29 the write was accepted
  but any `metadata` or `spec` update wiped it. So the choice was never
  "works on old clusters, fails on new" — it was "fails loudly" or "fails
  silently".
- The display claim the design rests on is confirmed: `kubectl get svc` shows
  `<none>` under EXTERNAL-IP for ClusterIP and NodePort Services regardless of
  status, and the hostname for a `type: LoadBalancer` one.

**Decision 2 has since been rewritten twice and is settled**: split by trigger.
The annotation path writes nothing at all; the `loadBalancerClass` path writes
status. The mirrored `{provider}/hostname` annotation that this finding first
suggested was considered and **killed** — nothing writes that key. See the
phase 2 section.

Adjacent facts from the same session, expensive to rediscover:

- A `metadata` or `spec` update on a **LoadBalancer** Service does *not* clobber
  `status.loadBalancer`. Annotating one and then adding a port both left the
  hostname in place. This mattered when a second write to the Service was still
  planned; it is now only reassurance that another controller touching the
  Service does not wipe what we published.
- Flipping `type` from `LoadBalancer` back to `ClusterIP` while status is set is
  **accepted** and clears status automatically. A user downgrading a Service is
  not a case the controller has to unwedge.
- `allocateLoadBalancerNodePorts: false` renders EXTERNAL-IP exactly the same
  and allocates no node port — `80/TCP` rather than `80:31262/TCP`. The
  documented tidy-up works and is still the user's to set, not ours.
- `spec.loadBalancerClass` is **forbidden unless `type` is `LoadBalancer`**
  (`Forbidden: may only be used when 'type' is 'LoadBalancer'`) and **immutable**
  once set — both adding it to an existing LoadBalancer Service and changing it
  on one that has it fail with `may not change once set`.
- Dotted class values are legal: both `tunnel.pizza` and `api.trycloudflare.com`
  are accepted as `loadBalancerClass`. It is validated as a qualified name, and
  dots are legal in the name half, so the provider vocabulary needs no mangling.

## Phase 2: the Service controller, Ingress branch

`service/service.go`, `children.go`, `status.go`. Both triggers reach one
reconciler; the child is an Ingress; the Gateway branch is refused with an
event until phase 3.

**How `spec.loadBalancerClass` gets watched, since a metadata watch cannot see
it.** Three facts settled it, all measured (apiserver v1.35.0): Services do
**not** maintain `metadata.generation` — it is absent, where a Deployment's is
`1` — so an update predicate cannot key on a spec change; `spec.type` **is** a
supported field selector for Services but `spec.loadBalancerClass` is **not**
(`field label not supported`); and the class can appear on an update, not only
at creation, because it may be set when a Service's type changes to
LoadBalancer.

So: **one metadata-only watch over all Services, every event enqueued, and the
full Service read through `mgr.GetAPIReader()`.** Memory stays proportional to
Service count, which is the constraint the design set. The cost is an uncached
GET per Service event — Services are low-churn, so this is a startup burst of
one GET each and very little after. The alternative, rejected as a design
change rather than taken quietly, is a spec-cached informer field-selected to
`spec.type=LoadBalancer`: precise and small, since LoadBalancer Services are
rare, but it caches specs and that was the coordinator's call, not mine.

Do **not** switch the Service read to the cached client. controller-runtime
builds a second, structured informer for any type you Get through the cache,
on top of the metadata one, and its own docs say the two then race.

**What is written where.** Decision 2 in its final form splits by trigger: the
annotation path writes **nothing** to the Service, the `loadBalancerClass` path
writes `status.loadBalancer.ingress[]` with `hostname` and ports 80/443 and no
`ip`. The RBAC is where that boundary cannot erode — `services` is
get/list/watch with **no write verb**, and `services/status` is `patch` alone.
`publish` sends a merge patch, so `patch` and not `update` is the right verb.

Verified on a cluster before building on it, because the inference had already
been wrong twice:

- `status.loadBalancer.ingress[].ports` **persists** — hostname and ports
  round-trip exactly, `ip` absent from the stored entry.
- Status ports need **no correspondence** with `spec.ports`. A Service
  declaring only `8080` accepted status ports 80 and 443. There is no
  validation there at all: `port: 70000` was also accepted, and
  `PortStatus.Error` took `"THIS IS NOT A QUALIFIED NAME"`. Nothing will catch
  a wrong value, so the controller has to be right on its own.

**Child objects.** `<service>-<provider with dots as dashes>`, hash-suffixed
past 253 characters. Service names cannot contain dots, so the result never
does either and only the 253 limit is reachable. `spec.defaultBackend`, not a
rule — one origin, no paths. `metav1.NewControllerRef` to the Service; every
delete is scoped by `metav1.IsControlledBy`, which is the only thing standing
between a cluster-wide `delete` grant and somebody else's Ingress. An Ingress
that already holds the name and is not ours is left alone with a warning event.

**Removing the trigger deletes the child** — owner-reference GC does not cover
that, only Service deletion, and it is the case a user hits first.

## Phase 1: `service` resolves, and nothing calls it yet

`service/providers.go` is one pure function, `providers(*corev1.Service, known
[]string) ([]resolved, error)`, plus its tests. **No controller, no informer, no
RBAC, and no caller** — that is phase 2, and the metadata-only watch the design
insists on is the thing to get right there.

The shape worth keeping: both triggers are read in **one pass over the whole
Service**, into one map keyed by provider. Deduplication is on provider, not on
trigger, and it is free that way and awkward any other way. Output is sorted by
provider, because phase 2's child object names derive from it and an unstable
order orphans a child per reconcile.

`known` is an argument rather than a package-level read of
`consts.InstalledProviders`, because the honest vocabulary is "classes in this
cluster naming this controller" — a cluster read a pure function must not do.
Widening it is a change to the caller.

Decisions in here that will look arbitrary later:

- **An unknown annotation prefix is an error; an unknown `loadBalancerClass` is
  silently not ours.** The asymmetry is the point. Nothing else in the ecosystem
  defines a `{prefix}/tunnel` key, so an unrecognised one is a typo and saying
  nothing looks identical to the controller being down. `loadBalancerClass` is
  how a Service says which implementation owns it, so an unrecognised value
  names MetalLB or a cloud provider, and erroring would put a warning on every
  foreign LoadBalancer Service in the cluster.
- **An explicit `false` beats every trigger**, including `loadBalancerClass`.
  Since that field is immutable, an annotation is the only way to turn it off
  without deleting the Service.
- **Only TCP ports are candidates.** A tunnel carries HTTP over TCP, so a UDP
  port is not a worse choice, it is not a choice — one HTTP port beside a UDP
  one resolves rather than being refused for ambiguity it does not have. This
  is slightly beyond what decision 3 says; it only ever turns a refusal into a
  correct answer. Flagged to the coordinator, not smuggled.
- **A `{provider}/tunnel-api` naming no tunnel is an error**, unless the same
  provider also carries an explicit `false` — keeping the api line while
  flipping the switch off is what the explicit off is for.
- `spec.loadBalancerClass` is read only when `spec.type` is `LoadBalancer`. The
  other combination is unreachable (see phase 0 above) and the check is there to
  say so, not to defend against it.

**`errUnsupported` now lives in `consts` as `ErrUnsupported`.** `ingress` and
`gateway` keep a one-line local alias so their call sites read unqualified, and
`errors.Is` holds across all three — a caller composing a Service with a child
Ingress should not have to know which package's sentinel to test. Nothing in
`service` can be transient: it reads one object and nothing else, so every error
it returns wraps the sentinel and a caller reports and drops rather than
requeueing.

**Twenty-one mutations, all killed** (`service/providers_test.go`): order not
sorted; the class keyed separately from the annotation so the two triggers stop
deduplicating; explicit off ignored; `false` treated as on; unknown provider
ignored; unparseable value treated as off; prefixless annotation ignored;
`https` preferred over `http`; first port taken instead of the preferred name;
ambiguous ports resolved instead of refused; protocol filter dropped; port
resolved for a Service that asked for nothing; orphaned `tunnel-api` not
reported; `tunnel-api` beside an off wrongly reported as orphaned; annotations
scanned in map order; `loadBalancerClass` read regardless of type; a foreign
class claimed as ours; default API flipped to gateway; unknown `tunnel-api`
defaulted instead of refused; errors no longer wrapping `ErrUnsupported`; and
the mirrored `{provider}/hostname` annotation read as the trigger. The last one
is still worth keeping, but not for the reason it was written: decision 2 has
since killed the mirrored annotation, so nothing writes that key. It stays
because a *user* can write it, and a controller that treats an arbitrary
`{provider}/hostname` as a request for a tunnel is wrong whoever set it.

Two tests exist only to survive Go's randomised map iteration —
`TestProvidersOrderIsStable` and `TestProvidersReportsTheSameErrorEveryTime`,
both 100 iterations. The table alone would flake rather than fail.

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
carried deliberately. `bundledVersion` is `gatewayconsts.BundleVersion` — the
module's own release constant, not a string maintained here — and
`TestBundleVersionMatchesGoMod` pins the embedded file against it, so `make
crds` after a go.mod bump is not something anyone has to remember.
`.dockerignore` is an allow-list and re-includes
`gateway/crds/zz_generated.*.yaml` by negation — that is why `Dockerfile` line 1
is `# check=skip=CopyIgnoredFile`.

## The service e2e annotates the API server's own Service

Two suites, deliberately separate. `tests/e2e/service/` is the
`loadBalancerClass` case: an nginx fixture carrying the `status.loadBalancer`
assertions. `tests/e2e/service-annotate/` is the annotation case, split out so
it can be run and skipped on its own (`kubectl kuttl test --test
service-annotate`) because it is the only test in the tree that touches an
object it did not create: it annotates **`kubernetes` in `default`** — the API
server's own Service —
because a fixture Service is written for us and therefore proves nothing about
annotating a Service you already have. Christian decided this after being told
what it costs.

What it costs: a real public tunnel is minted fronting the kube-apiserver,
because minting is triggered by the child Ingress existing and not by what the
test asserts. Two things bound it.

- `03-unannotate.yaml` removes the annotation as the last step, which deletes
  the child and tears the tunnel down at once.
- **A failing run never reaches that step.** kuttl aborts at the first failed
  step, and the annotation is on a pre-existing object in `default`, so kuttl's
  namespace cleanup will not revert it. The backstop is that the kind cluster
  is deleted afterwards, which cancels every tunnel context — and that backstop
  **does not apply under `--skip-delete`**, which is exactly the flag you are
  using when debugging a failure here. The comment saying so is in
  `01-annotate.yaml`, where someone debugging will actually read it.

**The tunnel carries traffic to the API server, which then refuses it — and
that is asserted, not assumed.** `consts.OriginScheme` is `http` and the `kubernetes` Service's only
port is 443 speaking TLS, so the origin is `http://kubernetes.default.svc:443`.
From inside the cluster that returns `HTTP/1.0 400 Bad Request` with the body
`Client sent an HTTP request to an HTTPS server.` — every request through the
tunnel fails at the origin. For contrast `https://` to the same port answers
`403` (anonymous is forbidden), so even a scheme fix would not expose the API.
This materially reduces what the test exposes, and it is a different risk than
the one the exposure was accepted against.

The suite **asserts exactly that**, at Christian's request: it curls
`https://<tunnel>/healthz` and requires a `400` carrying that body. The 400 is
the proof — the bytes crossed the tunnel, reached the API server and came
back, which a `502` or `530` from the edge would not show. Asserting `ok`
instead would require the controller to dial TLS origins: a change to
`consts.OriginScheme` and a deliberate decision about exposing an API server,
not a test change.

Four real tunnels per full run now, not two: ingress, gateway and service one
each, plus service-annotate's.

**A kuttl layout trap, caught only because the run output was read rather than
trusted:** each `testDirs` entry is a directory *of* tests — every immediate
subdirectory becomes one suite. An entry pointing straight at step files logs
`testsuite: ./tests/service-annotate/ has 0 tests` and **still exits 0**, so
the suite silently does not run and the overall result is still PASS. That is
why `service-annotate` lives under `tests/e2e/` rather than at
`tests/service-annotate/`.

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
- `make test-e2e` — green on every run: `83.14s` with the `Accepted` assert,
  then `gateway (15.62s)` / `ingress (20.64s)` with `SupportedVersion` added.
  Real tunnels minted and both schemes served nginx each time.
- The negative runs matter as much, and there is one per condition. Flip the
  assert and `kubectl kuttl test --test gateway` fails in `step 0` —
  `.status.conditions.status: value mismatch, expected: False != actual: True`
  for `Accepted`, `.status.conditions.reason: ... expected: UnsupportedVersion
  != actual: SupportedVersion` for the other — and never reaches the mint, so
  re-checking either costs the provider nothing.
- What the real cluster publishes, from the kuttl diff rather than a unit test:

      Accepted=True/Accepted
        Gateways on this class get a tunnel minted from https://tunnel.pizza/tunnel
      SupportedVersion=True/SupportedVersion
        Gateway API CRDs are at v1.6.1, which this controller supports (v1.6.x)

  Both with `observedGeneration: 1`. That the second exists at all is the proof
  that a metadata-only `PartialObjectMetadataList` read works through
  `mgr.GetAPIReader()` — nothing else exercises that path.
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

## GatewayClass conditions are complete — the policy, and where it came from

Both conditions the spec requires are published and asserted. `SupportedVersion`
landed in `9f7e5d0`; the policy is documented at length on `checkVersions` in
`gateway/crds.go`, and this is the short version plus the reasoning that does
not belong in a doc comment.

- **Input is fixed by upstream**, not chosen: the version of a Gateway API CRD
  *is* its `gateway.networking.k8s.io/bundle-version` annotation.
- **Compared on major.minor, patch ignored.** Upstream's versioning policy
  forbids schema changes in a patch, so pinning the patch would report a
  cluster unsupported over a difference that cannot reach us. This is also what
  nginx-gateway-fabric does (`internal/controller/state/graph/gatewayclass.go`,
  `validateCRDVersions`) — worth reading if the policy is ever revisited.
- **A missing annotation is `False`, not an exemption.** This is the one that
  reads wrong at first and is nevertheless what the spec says: "CRDs that
  either do not have this annotation set, **or** have it set to a version that
  is not recognized ... MUST be set to false." An implementation cannot vouch
  for CRDs it cannot identify. The distinction between "wrong version" and "no
  annotation" lives in the message, which is where upstream puts it.
- **`Accepted` stays `True` regardless** — the "best effort" one of the two
  behaviours upstream permits. The alternative is what NGF does, and
  nginx/nginx-gateway-fabric#4762 is a user whose working cluster was refused
  over exactly this. Refusing a class whose Gateways provision is the bug
  `502deca` fixed; re-introducing it through the version check would be the
  same bug wearing a different hat.
- **Every Gateway API CRD counts**, including kinds this controller never
  reads. A stale `v0.x` TCPRoute left in a cluster flips the condition and gets
  named in the message. That is diagnosable and costs nothing, because it does
  not stop provisioning.
- **Channel is not part of it.** Experimental at our version is a superset of
  our schema. `installCRDs` cares about the channel because *writing* across
  channels drops data; reading does not.

Two implementation notes that are easy to undo by accident. The read is
metadata-only (`metav1.PartialObjectMetadataList`) through the manager's
**uncached** reader: an informer on CustomResourceDefinition caches every CRD
schema in the cluster, which on a cluster running cert-manager or Istio is more
memory than the rest of this controller. The cost is that a CRD upgrade under a
running controller does not refresh the condition until the class is touched or
the process restarts, which is documented on the `CRDs` field. And
`bundledVersion` is now `gatewayconsts.BundleVersion` — the module's own
constant — so a gateway-api bump no longer needs it edited; `make crds` and the
version literals in `gateway_test.go` are the only manual steps, and
`TestSupportedVersionsPin` is what tells you.

The `Accepted` condition itself is correct and guarded: `Accepted=True`,
reason `Accepted`, `observedGeneration`, message naming the provider. Tests in
`gateway/gateway_test.go` plus the gateway e2e `00-assert`. Both were checked
by mutation, and the e2e one fails at step 0 before any tunnel is minted, so
breaking it costs no provider load. **Assertions for both conditions are
written against literals — `"Accepted"`, `"SupportedVersion"`, `"v1.6.1"` — not
against the constants the controller uses** — an assertion phrased in the
code's own constants would have passed just as happily against the
`False`/`Waiting` condition that shipped for a release.
`TestConditionsMatchTheSpec` pins the condition literals to upstream and
`TestSupportedVersionsPin` pins the version ones to the build, so a rename or a
bump fails there rather than quietly weakening every other assertion.

Nine mutations were run against the version check and all nine failed the
suite: version never checked, unannotated ignored, patch pinned, reason not
flipped with the status, `observedGeneration` dropped, an unreadable cluster
assumed fine, only the watched kinds inspected, `Accepted` withdrawn on skew,
and the condition not published at all.

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
  Bumping gateway-api means `go generate ./gateway/...`, and — on a new minor —
  the version literals in `gateway/gateway_test.go` and the `SupportedVersion`
  assert in `tests/e2e/gateway/00-assert.yaml`. `bundledVersion` follows the
  module on its own now. `TestSupportedVersionsPin` fails with the list.
