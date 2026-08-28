# PR #1355 — Fix progress tracker (living doc)

Continuously updated as fixes land. Source of truth for what's done / in-flight / open.
Verification detail lives in `pr-1355-unresolved-full-verification.md`; this doc tracks execution.

**74 unresolved review threads (2026-08-28 round).** Legend: ✅ done+committed ·
🔵 in progress · ⬜ open · 🟢 resolved-on-GitHub · ❎ false-positive.

## Commits so far

- `b3f3e0a7` — batch 1: cache TOCTOU, forEach data-pending, coordinator labels, cost-limit threading.
- `2559b399` — batch 2: executor Apply/Delete hard-error aggregation, update-rejection log signal, malformed-create fail-fast.
- `5f8c171b`, `7ce66a90`, `803597d4` — batch 5: compiler/parser (7 findings). VERIFIED.
- `4f0c2777`, `4b0ff871`, `5d6fd30b`, `a4a87dda` — batch 6: schema-wrapper typing, lossless schema events, scope-leak, soft-dep list seed, dead-projector removal, cache metrics. VERIFIED (-race).
- `814e91d2`, `f64591e8`, `2252bb81` — batch 7 (agent): api spec Required + codegen, lint cmd/kro, helm graph-concurrent-reconciles. VERIFIED.
- `d8d3d876` — main.go:350 fail-closed on incomplete controller identity when GraphKind on.
- `65d14946` — batch 3: instance contribution-release not-ready flip + prune UID-conflict requeue.
- `bca...`/`0ee2b54b` — batch 3: graph release-failure condition (421) + soft-not-ready prune narrowing (357).

## GitHub thread state

- 🟢 `registry.go:135` — replied + **resolved** (FP).
- ⬜ `.golangci.yml:43` — replied (doc-exists), left open for scoping judgment.
- 💬 batch 2/5/6 fixes — replied on-thread (left open for author to resolve).
- 💬 `886`, `runtime.go:203`, `cached.go:91` — replied as open design decisions.

## GitHub thread state

- 🟢 `registry.go:135` — replied + **resolved** (race genuinely closed; presence+monotonic counter).
- ⬜ `.golangci.yml:43` — replied (doc-exists correction) but **left open** for the scoping judgment.
- 💬 `simple.go:272/493/751` — replied (fixed in 2559b399); left open for author to resolve.
- 💬 `simple.go:886` — replied; **open design decision** (tolerate+log vs unconverged; 3 tests + integration pin tolerate).

---

## Batch 1 — DONE (`b3f3e0a7`)

- ✅ `cached.go:128` TOCTOU — Add under write lock, atomic w.r.t. invalidation. (High)
- ✅ `node.go:448` forEach data-pending → ErrDataPending wrap. (High)
- ✅ `coordinator.go:199` empty old-labels on update (+2 tests). (Med)
- ✅ `status.go:396` cost-limit threaded flag→ReconcileConfig→projection. (Med)

## Batch 2 — Executor High-severity (DONE `2559b399`, 2 items deferred)

File: `pkg/graphengine/executor/simple.go` unless noted.

- ✅ `simple.go:272` Apply loop: collect hard errors, continue, errors.Join after walk; `ready` gating preserved. Status patch + independent nodes now run.
- ✅ `simple.go:493` Delete: accumulate + errors.Join, continue whole inventory.
- ✅ `simple.go:751` collection CREATE: Invalid/BadRequest → hard fail-fast; transient stays soft. +test.
- 💬 `simple.go:886` recordUpdateRejected — RESOLVED as Opt 2 (`4e2a5955`): converge + Warning Event ('UpdateRejected') on the affected child via observational OnToleratedRejection hook; classifyRejection surfaces permanent(immutable/invalid) vs transient reason. Never flips aggregate Ready (no wedge). Graph path log-only (no recorder).
- ⬜ `simple.go:1489` SSA contribution release can recreate deleted target — DEFERRED to batch 3 (release semantics, groups with 1492).
- ⬜ `simple.go:1492` release fails when target CRD GVK unmaps — DEFERRED to batch 3.

## Batch 7 — API / docs / examples / helm / cleanup

- ✅ `graph_types.go:258` spec Required + CRD regen. `814e91d2` (agent, verified: `make manifests` clean)
- ✅ `.golangci.yml:98` lint covers cmd/kro. `f64591e8` (agent)
- ✅ `helm deployment.yaml:188` --graph-concurrent-reconciles wired. `2252bb81` (agent)
- ✅ `graph.md` doc accuracy (patch RawExtension API, RGD→graphengine unconditional, status-patch force split, nested-Graph revisions unimplemented, async-nesting boundary, deferral syntax, applier SA/RBAC note, illustrative-example caveat). `1ceb79bf` (agent, VERIFIED: force-split + deferral syntax probed against real parser)
- ✅ nested inline-`graph:` compile test. `d9b0ca5d` (agent, passes)
- NOTE: caught + reverted a stale foreign CRD reformatting (indentation churn) in the working tree; committed CRD matches `make manifests` exactly (verify-codegen safe).
- ⬜ examples rewrites (`rgd.yaml` unregistered CEL / `.ready()` deferred to KREP-006, `coredns` inline blocks, `singleton` fan-out) — RESOLVED (#6): keep examples in-tree, flag aspirational parts as FUTURE WORK. rgd.yaml banner + singleton/coredns callouts (`13122c53`); all example threads replied. L0 compile test still passes.

## Batch 3 — Lifecycle / leak (IN PROGRESS)

- ✅ `controller_graph_engine.go:266` contribution-release failure now flips ResourcesNotReady + requeues. `65d14946`
- ✅ `controller_graph_engine.go:663` prune UID-conflict now soft-requeues. `65d14946`
- ✅ `graph/controller.go:421` release failure flips ResourcesConverged=False (ReleaseFailed). `0ee2b54b`
- ✅ `graph/controller.go:357` prune retired nodes on soft not-ready (E2 twin). `0ee2b54b`
- ✅ `simple.go:1489` release GET-first — no longer recreates a deleted target. `7a3ffb47`
- ✅ `simple.go:1492` release tolerates removed CRD (NoMatch), retries transient. `7a3ffb47`
- ✅ `impersonation.go:129` impersonated-executor cache bounded with LRU (evict-safe; size bound). `025382e6`
- ✅ `controller_graph_engine.go:207` / `simple.go:1627` duplicate identity now rejected PRE-WRITE (executor identity-claim guard in prepareItem; covers RGD + Graph + subgraph frames; ErrDuplicateIdentity hard error). `f0004a3c`
- ✅ `graph/controller.go:314` contribution write-ahead before Apply (shared executor.PatchFieldManager, drift-proof; intendedContributions projection). `51c90831`
- ✅ `tracking.go:34` resourceKey now keys on Group+Kind (not full apiVersion) — version-only change no longer apply-then-prunes. `f50ab83b` (turned out mechanical: Group parses from apiVersion, no RESTMapper)
- ✅ `tracking.go:137` write-ahead now recurses subgraphs (both intendedManagedResources + intendedContributions; qualified NodeID + field-manager match executor byte-for-byte). `62d570da` (agent, VERIFIED)
- ✅ `tracking.go:161` dynamic-node empty-namespace — skip ambiguous dynamic-GVK node (no explicit ns) from write-ahead intent; kills idle churn. `767242b2` (approach (b), I/O-free; RESTMapper alternative offered on-thread)

## Batch 4 — Security (decisions)

- ✅ #4 `deletion.go:139` editable-annotation release — DONE (both paths):
  - Graph path: contribution inventory moved annotation → `GraphStatus.Contributions` (RBAC-separable status subresource; API type + codegen + controller rewrite). `f08df2e8` (agent, VERIFIED: codegen in sync, CRD has status.contributions, graph+instance suites pass)
  - Instance path (status is a no-go on a dynamic CR): finalizer releases ONLY kro patch field managers (`executor.IsPatchFieldManager`); forged non-kro managers refused. Scope-GK filter rejected as security theater (non-secret hash). `973f2248`
- ✅ #5 `cluster-role.yaml:155` cluster-wide impersonate gated behind GraphKind. `430685c4` (verified via helm template)
- ⬜ `impersonation.go:129` executor cache eviction — already fixed earlier (`025382e6`, LRU bound).

- ⬜ `cmd/controller/main.go:350` missing controller ns/SA flags silently disable self-impersonation guard. Fix: fail startup when GraphKind on.
- ⬜ `graph/controller.go:149` teardown skips ns + self + fail-closed checks. Fix: re-run guards before teardown executor.
- ⬜ `deletion.go:139` release trusts editable annotation under controller privileges. Fix: move to status / validate against ApplySet scope.
- ⬜ `helm cluster-role.yaml:155` cluster-wide impersonate even with GraphKind off. Fix: gate behind opt-in + scope.
- ⬜ `impersonation.go:129` executor cache never evicts. Fix: LRU bound / evict on Graph delete.

## Batch 5 — Compiler / parser (DONE — 5f8c171b, 7ce66a90, 803597d4; verified)

Skipped context.go:237/256 (deferred-schema / CRD-CEL — product decision, not a code fix).

- ✅ `compiler.go:651` includeWhen self-reference → compile-time reject. +test.
- ✅ `compiler.go:730` ancestor patch capture → framePatchKind walks frames, rejects. +test.
- ✅ `typecheck.go:302` dyn-GVK collection `each` → declared as cel.DynType. +test.
- ✅ `typecheck.go:325` optional<bool> conditions → compile-time reject w/ orValue hint. +test.
- ✅ `validation.go:206` no-CEL-seed heuristic → requireResolvableRoot (real root check; cycles still caught by AddDependencies+TopologicalSort, verified double-guarded). +test.
- ✅ `cel.go:31` scanner now tracks single-quoted literals identically to double. +tests. (verified no example regression)
- ✅ `parser.go:285` unterminated ${ → ErrUnterminatedExpression. +tests.

- ⬜ `compiler.go:651` includeWhen self-reference silently dropped. Fix: reject at compile.
- ⬜ `compiler.go:730` patch capture across ancestor frames unchecked. Fix: resolve kind across frames.
- ⬜ `typecheck.go:302` dyn-GVK collection leaves `each` undeclared. Fix: declare `each` as dyn.
- ⬜ `typecheck.go:325` optional<bool> accepted, empty = runtime error. Fix: reject or define empty semantics.
- ⬜ `validation.go:206` requires no-CEL seed node. Fix: validate real DAG cycles.
- ⬜ `cel.go:31` scanner only double-quoted strings (WIDE blast radius — careful).
- ⬜ `parser.go:285` unterminated ${ silently literal. Fix: parser error.
- ⬜ `context.go:237/256` static custom-Kind resolved before CRD applies (deferred-schema unimplemented).

## Batch 6 — Runtime / watch / status / cache (DONE — 4f0c2777, 4b0ff871, 5d6fd30b, a4a87dda; verified -race)

LEFT OUT (resolved as Opt A): `runtime.go:203` apply-order — documented revision-locality at the computation site (`f274f34e`); deletion path already tolerant of mixed schemes.

- ✅ `status.go:205` schema-wrapper preserved: extract underlying map from the ref.Val, overlay conditions, re-wrap with InstanceSchemaForCEL so byte/date-time/number typing survives. +tests. (High)
- ✅ `schemawatcher/watcher.go:457` lossless enqueue: dirty-set + wakeup-signalled drainer, blocking send (backpressure not drop), coalesced by key; stop-channel escape, no leak/deadlock. +2 tests, -race. (High)
- ✅ `runtime.go:108` child scope no longer leaks ancestor under shadowed local ID (override-exempt). +tests.
- ✅ `runtime.go:178` soft-dep collections seeded as []any{} not {}. +test.
- ✅ `status.go:66` dead ProjectInstanceStatus + exclusive helpers (evalStatusExpr/setAtPath/getAtPath) removed; verified only worktree refs remained.
- ✅ `cached.go:104` cache metrics instrumented (hits/misses/duration/errors/singleflight/size/evictions via onEvict). +tests.
- ✅ `cached.go:91` epoch map growth — Opt A: documented as intentional/bounded (O(1)/GK ≈ installed CRDs; reclamation reintroduces the fixed TOCTOU). Committed NOTE in `a4a87dda`; thread replied.

## Batch 7 — API / docs / examples / helm / cleanup (OPEN)

- ⬜ `graph_types.go:258` spec omitempty → empty Graph passes admission. Fix: Required + regen.
- ⬜ `graph_types.go:77` managedResources unbounded. Fix: MaxItems + aggregate cap.
- ⬜ `helm deployment.yaml:188` --graph-concurrent-reconciles not wired. Fix: value + arg.
- ⬜ `cmd/controller/main.go:344` example RGD-as-Graph dual-owns RGD lineage.
- ⬜ `cmd/controller/graphengine.go:56` duplicate cluster-wide watch router.
- ⬜ `.golangci.yml:98` lint skips cmd/kro module.
- ⬜ examples: rgd.yaml unregistered CEL (plural/toOpenAPI/.ready), singleton claims[0] only.
- ⬜ docs graph.md:3/294/304 stale PatchSpec banner, force claim, nested revisions.
- ⬜ `user-cluster-role.yaml:19` no instance access (PARTIAL — claim inaccurate).

## Non-actionable / decided

- ❎ `registry.go:135` — resolved.
- PARTIAL `graph/controller.go:306` (double-write growth-gated), `controller_graph_engine.go:266` (degraded not folded — will fix in batch 3).
