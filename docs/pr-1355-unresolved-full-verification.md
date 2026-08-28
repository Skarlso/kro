# PR #1355 — FULL unresolved-thread verification (08-28 round)

**This supersedes the earlier doc's scope.** My first pass only saw the ~30 findings
shown in the truncated PR-body view. A GraphQL query for `reviewThreads(isResolved:false)`
revealed the real state: **74 unresolved threads, all dated 2026-08-28** — a later,
far more thorough review round. **~60 of these were never in my first analysis.**
All 74 were re-verified by agents reading current code (not trusting any tracking doc),
with an explicit adversarial check on claimed guard/epoch fixes.

Outcome: **~66 CONFIRMED**, **~5 FALSE-POSITIVE**, **~3 PARTIAL**. Almost none fixed.

Two corrections to my earlier doc:

- **cached.go TOCTOU (my C4):** I said FIXED. **WRONG — it is CONFIRMED OPEN.** The epoch
  check is under `RLock`, but `cache.Add` runs after the unlock, so `InvalidateGroupKind`
  can bump epoch + clear cache in the window → stale schema repopulated. High severity.
  One-line fix: hold the write lock across re-check + `Add`.
- **registry.go epoch race (my C3):** I said FIXED. Agent re-verified: genuinely **closed**
  (presence-tracking `hadEpoch` + monotonic `nextEpoch`). So that verdict was right.

---

## HIGH severity, confirmed, unfixed

### Executor (`pkg/graphengine/executor/simple.go`) — all 11 confirmed

- **Apply loop aborts on first hard node error (~272):** later independent nodes AND the
  synthesized author-status patch node (appended last) are skipped. Fix: collect per-node
  hard errors, `continue`, `errors.Join` after the walk; keep dependent gating via `ready`.
- **Delete stops on first hard error (~493):** one RBAC-denied delete strands every
  remaining managed resource. Fix: accumulate + `errors.Join`, continue the inventory.
- **Collection CREATE permanent rejection is soft-forever (~751):** a 422/Forbidden create
  is wrapped `ErrNotReady` → IN_PROGRESS forever, unbounded requeue. Fix: classify
  `IsInvalid/IsBadRequest/IsForbidden` as hard.
- **recordUpdateRejected false-ready (~886):** a permanently rejected UPDATE is recorded
  *applied*, error dropped → instance can report ACTIVE/Ready with a stale live object.
  (`desired[i]=current` swap fixes the fictional-scope half only.) Fix: classify permanent
  rejections as `recordFailure` (soft not-ready) so the node never reports converged.
- **SSA contribution release can recreate a deleted target (~1489):** `client.Apply` with no
  existence precondition creates if absent between prune and release. Fix: precondition /
  merge-patch that can't create; treat NotFound as released.
- **Release fails when target CRD GVK no longer maps (~1492):** only NotFound tolerated;
  a removed CRD → NoMatch error blocks cleanup + finalizer. Fix: treat confirmed missing API
  as released, retry only transient discovery errors.

### Cache — `pkg/graph/schema/resolver/cached.go:128`

- **TOCTOU (see correction above).** High: silent stale-schema serving after a CRD change.

### Runtime/status

- **status.go:205 schema-wrapper lost:** condition overlay does `schemaVal.(map[string]any)`
  on a schema-aware `ref.Val` → assertion fails → fallback drops all spec/metadata and
  returns an untyped map, breaking float/date-time/bytes CEL types in author `schema.*`.
- **node.go:448 forEach data-pending → hard failure:** missing upstream data in a `forEach`
  returns a plain CEL error, not `ErrDataPending` (scalar path wraps it) → a normal pending
  field becomes a permanent graph failure. Fix: classify `IsCELDataPending` and wrap.
- **schemawatcher/watcher.go:457 schema events silently dropped:** non-blocking channel send
  drops on full buffer → missed CRD change = stale Program never invalidated; compounded by
  all-dynamic-Graph fanout. Fix: lossless coalescing (dirty-set + wakeup) + precise GVK deps.

### Controller / lifecycle

- **graph/controller.go:314 + instance path:266:** a patch lands before its contribution
  annotation is persisted; a crash leaves mutated fields with no release inventory (field leak).
- **instance controller_graph_engine.go:207 / executor:1627 duplicate identity:** two nodes of
  one Graph targeting one object are exempted from conflict detection and detected only
  *post-apply* → second write clobbers first; force-reclaim thrash while Graph stays Ready.
  Fix: reject duplicate rendered identities *before* the first write, executor-side.
- **deletion.go:139 privileged release from editable annotation:** finalizer reads the
  client-editable `PatchContributionsAnnotation` and executes release under controller identity.
  Fix: move contribution inventory to status subresource / validate against ApplySet scope.

### Startup / examples (security + correctness)

- **cmd/controller/main.go:350:** missing `--controller-namespace`/`--controller-service-account`
  silently disables the controller-self impersonation guard. With GraphKind on, fail startup.
- **examples/graph/rgd.yaml (16/17):** calls `plural()`, `simpleSchema.toOpenAPI()`, `.ready()`
  — **none registered** in the CEL env (`.ready()` explicitly deferred to KREP-006). They hide
  inside deferred `${"${…}"}` wrappers so the L0 compile test passes, but L1/L2 evaluation
  would fail at runtime. The flagship example is non-functional past L0.
- **cmd/controller/main.go:344:** the example RGD-as-Graph reconciles the same RGD lineage as the
  production RGD controller → dual ownership if ever applied.

## MEDIUM severity, confirmed (selected)

- **graph/controller.go:357** Graph-path prune/release gated on coarse `applyErr==nil` — the
  instance path got the `ownedUnresolved`/`pruneGate` narrowing; the **Graph path did not**
  (this is the still-open twin of my earlier E2).
- **graph/controller.go:421** release failure returns without flipping `ResourcesConverged` →
  stale contributed fields coexist with Ready=True (prune-fail path was fixed, release-fail wasn't).
- **graph/controller.go:149** teardown skips the namespace + controller-self + fail-closed checks
  that normal reconcile runs; falls back to controller creds if impersonation unwired.
- **graph/controller.go:344 / tracking.go:137,161** single `AppliedServiceAccount` can't represent
  a mixed-identity partial apply; write-ahead skips dependent + subgraph nodes; dynamic-node empty
  namespace never dedups → idle status rewrites every cycle.
- **tracking.go:34** `resourceKey` includes apiVersion → a version-only template change
  applies-then-prunes the same stored object.
- **instance controller_graph_engine.go:663** prune UID conflict returns success (no guaranteed retry).
- **compiler.go:651/730** includeWhen self-reference silently dropped (unsatisfiable node admitted);
  patch capture across ancestor frames unchecked.
- **context.go:237/256** static custom-Kind resolved before its CRD node applies (deferred-schema
  is only a *proposal*, unimplemented) → Singleton/RGD examples need a bootstrap CRD.
- **typecheck.go:302/325** dynamic-GVK collection leaves `each` undeclared; `optional<bool>`
  accepted but empty-optional is a runtime error in include/readyWhen.
- **validation.go:206** requires a no-CEL seed node instead of validating actual DAG cycles.
- **cel.go:31 / parser.go:285** scanner only handles double-quoted strings (rejects valid
  single-quoted CEL containing `${`); unterminated `${…` silently becomes literal data.
- **runtime.go:108/178** child runtime leaks ancestor value under a shadowed local ID; soft-dep
  collections seeded as `{}` (object) not a list.
- **status.go:396** author conditions compiled with `DefaultProgramOptions` (cost limit bypassed) —
  same class as my C5.
- **status.go:66** dead `ProjectInstanceStatus` projector (no production caller).
- **coordinator.go:199** empty old-label set skipped on update → `DoesNotExist` selector misses
  a match transition.
- **graph_types.go:258** `spec omitempty` + not Required lets an empty Graph pass admission.
- **helm cluster-role.yaml:155** cluster-wide `serviceaccounts:impersonate` granted even with
  GraphKind disabled, no ns/name scoping.
- **helm deployment.yaml:188** `--graph-concurrent-reconciles` (default 1) not wired through Helm
  (my F3 twin — confirms parallelism is unconfigurable).
- **docs graph.md:3/294/304** stale `PatchSpec` banner (API is `RawExtension`); "without force"
  claim wrong (status patches always force); nested-Graph "independent revisions" unimplemented.
- **singleton.yaml:188** status patches only `claims[0]`; other claimants + empty list never handled.

## FALSE-POSITIVE / PARTIAL

- **registry.go:135** — race genuinely closed (presence + monotonic counter). FP.
- **.golangci.yml:43** — the cited `docs/pr-1355-open-todos.md` **does** exist. FP.
- **user-cluster-role.yaml:19** — file is NEW in this PR, so "users lose *previous* access on
  upgrade" is inaccurate; the no-instance-access design gap is real but low. PARTIAL.
- **graph/controller.go:306 (double write)** — write-ahead is growth-gated, so not
  unconditionally double-written; the large per-item shape concern stands. PARTIAL.
- **instance:266 (contrib after Ready)** — `applyErr` is set on failure but `degraded:=hardErr`
  doesn't fold it in, so the mitigation is incomplete. PARTIAL.

## Reconciliation with my earlier ~30-finding doc

- **Overlap (~10):** executor recordUpdateRejected (B3), cached/registry (C3/C4 — **C4 verdict
  corrected to OPEN**), status cost-limit (C5), instance cross-node (B2-adjacent), Graph-path
  prune gate (E2 twin), concurrency (F3 twin).
- **Everything else in this doc (~60) is new** and was missed in the first pass.

## Fix sequencing (revised, whole set)

1. **Correctness/data-loss batch:** executor Apply-loop + Delete aggregation, permanent-rejection
   classification (create+update), SSA release preconditions + GVK-unmap tolerance, cached.go
   lock-fix, forEach data-pending, schema-wrapper preservation.
2. **Lifecycle/leak batch:** contribution write-ahead, duplicate-identity pre-write check,
   Graph-path prune narrowing, release-failure condition, UID-conflict requeue.
3. **Security batch:** startup fail-closed on impersonation flags, teardown fail-closed +
   namespace check, editable-annotation release hardening, helm impersonate scoping.
4. **Compiler/parser correctness:** self-ref reject, ancestor patch capture, dyn `each`,
   optional<bool>, DAG-cycle validation, single-quote scanner, unterminated-expr error.
5. **Docs/examples:** rewrite rgd.yaml to registered CEL (or label aspirational), graph.md
   banner/force/revisions, singleton fan-out, helm concurrency + user-role docs.
6. **Cleanup/observability:** dead projector, cache metrics, epoch-map GC, schema-event
   lossless coalescing, spec Required, lint cmd/kro.
