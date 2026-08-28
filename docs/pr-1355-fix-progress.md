# PR #1355 — Fix progress tracker (living doc)

Continuously updated as fixes land. Source of truth for what's done / in-flight / open.
Verification detail lives in `pr-1355-unresolved-full-verification.md`; this doc tracks execution.

**74 unresolved review threads (2026-08-28 round).** Legend: ✅ done+committed ·
🔵 in progress · ⬜ open · 🟢 resolved-on-GitHub · ❎ false-positive.

## Commits so far

- `b3f3e0a7` — batch 1: cache TOCTOU, forEach data-pending, coordinator labels, cost-limit threading.

## GitHub thread state

- 🟢 `registry.go:135` — replied + **resolved** (race genuinely closed; presence+monotonic counter).
- ⬜ `.golangci.yml:43` — replied (doc-exists correction) but **left open** for the scoping judgment.

---

## Batch 1 — DONE (`b3f3e0a7`)

- ✅ `cached.go:128` TOCTOU — Add under write lock, atomic w.r.t. invalidation. (High)
- ✅ `node.go:448` forEach data-pending → ErrDataPending wrap. (High)
- ✅ `coordinator.go:199` empty old-labels on update (+2 tests). (Med)
- ✅ `status.go:396` cost-limit threaded flag→ReconcileConfig→projection. (Med)

## Batch 2 — Executor High-severity (IN PROGRESS)

File: `pkg/graphengine/executor/simple.go` unless noted.

- 🔵 `simple.go:272` Apply loop aborts on first hard error → strands independent nodes + status patch. Fix: collect hard errors, continue, errors.Join after walk; keep `ready` gating.
- ⬜ `simple.go:493` Delete stops on first hard error → strands remaining inventory. Fix: accumulate + errors.Join, continue.
- ⬜ `simple.go:751` collection CREATE permanent rejection soft-forever. Fix: classify IsInvalid/IsBadRequest/IsForbidden as hard.
- ⬜ `simple.go:886` recordUpdateRejected false-ready on permanent UPDATE rejection. Fix: classify permanent rejections as recordFailure (soft not-ready), keep desired[i]=current swap.
- ⬜ `simple.go:1489` SSA contribution release can recreate deleted target. Fix: existence precondition / non-creating patch; NotFound = released.
- ⬜ `simple.go:1492` release fails when target CRD GVK unmaps. Fix: treat confirmed missing API as released, retry transient discovery.

## Batch 3 — Lifecycle / leak (OPEN)

- ⬜ `graph/controller.go:314` + `controller_graph_engine.go:266` contribution lands before annotation persisted → field leak. Fix: write-ahead contribution intent.
- ⬜ `controller_graph_engine.go:207` / `simple.go:1627` duplicate identity detected post-apply. Fix: pre-write duplicate-identity reject, executor-side.
- ⬜ `graph/controller.go:357` Graph-path prune gated on coarse applyErr==nil. Fix: port ownedUnresolved/pruneGate narrowing.
- ⬜ `graph/controller.go:421` release failure returns without flipping ResourcesConverged. Fix: set release-failure condition.
- ⬜ `controller_graph_engine.go:663` prune UID conflict returns success. Fix: requeue while conflict remains.
- ⬜ `graph/controller.go:344` + `tracking.go:137,161` single AppliedServiceAccount / write-ahead skips subgraph+dependent / dynamic empty-ns never dedups.
- ⬜ `tracking.go:34` resourceKey includes apiVersion → apply-then-prune on version-only change.

## Batch 4 — Security (OPEN)

- ⬜ `cmd/controller/main.go:350` missing controller ns/SA flags silently disable self-impersonation guard. Fix: fail startup when GraphKind on.
- ⬜ `graph/controller.go:149` teardown skips ns + self + fail-closed checks. Fix: re-run guards before teardown executor.
- ⬜ `deletion.go:139` release trusts editable annotation under controller privileges. Fix: move to status / validate against ApplySet scope.
- ⬜ `helm cluster-role.yaml:155` cluster-wide impersonate even with GraphKind off. Fix: gate behind opt-in + scope.
- ⬜ `impersonation.go:129` executor cache never evicts. Fix: LRU bound / evict on Graph delete.

## Batch 5 — Compiler / parser (OPEN)

- ⬜ `compiler.go:651` includeWhen self-reference silently dropped. Fix: reject at compile.
- ⬜ `compiler.go:730` patch capture across ancestor frames unchecked. Fix: resolve kind across frames.
- ⬜ `typecheck.go:302` dyn-GVK collection leaves `each` undeclared. Fix: declare `each` as dyn.
- ⬜ `typecheck.go:325` optional<bool> accepted, empty = runtime error. Fix: reject or define empty semantics.
- ⬜ `validation.go:206` requires no-CEL seed node. Fix: validate real DAG cycles.
- ⬜ `cel.go:31` scanner only double-quoted strings (WIDE blast radius — careful).
- ⬜ `parser.go:285` unterminated ${ silently literal. Fix: parser error.
- ⬜ `context.go:237/256` static custom-Kind resolved before CRD applies (deferred-schema unimplemented).

## Batch 6 — Runtime / watch / status (OPEN)

- ⬜ `status.go:205` schema-wrapper lost on condition overlay → typed CEL breaks. (High)
- ⬜ `schemawatcher/watcher.go:457` schema events silently dropped. (High) Fix: lossless coalescing.
- ⬜ `runtime.go:108` child runtime leaks ancestor value under shadowed local ID.
- ⬜ `runtime.go:178` soft-dep collections seeded as {} not list.
- ⬜ `runtime.go:203` apply-order not version-stable.
- ⬜ `status.go:66` dead ProjectInstanceStatus projector — remove or wire.
- ⬜ `cached.go:91` epoch map unbounded growth.
- ⬜ `cached.go:104` cache metrics never updated.

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
