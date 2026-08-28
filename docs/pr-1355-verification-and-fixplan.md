# PR #1355 — Independent verification of review findings + fix plan

Round: re-verification of cheeseandcereal's ~30 review findings against HEAD
(`be8b370e`), **not** trusting the author's own `pr-1355-open-todos.md`. Each
finding was traced to current code by symbol (line numbers in the review are
stale).

**Basis of these verdicts:** they rest on reading the *current* code on HEAD
(presence/absence of `WithLiteralNode`, `ownedByGraphTemplate`, the registry
epoch guard, `nodePrefix`/`qualifiedPath`, `pruneGate`, etc.), **not** on a
commit timeline. A number of fixes are dated after the review (e.g. `f972b758`,
`d3de5ff9`, `346ec7b9`, `be8b370e`), but the branch was rebased (committer dates
clustered at `2026-08-26 14:20`), so commit dates are unreliable and at least one
cited fix (`27e08384`) was *authored* before the review. The review date itself
("2026-08-25") is taken from the author's tracking-doc header, not independently
confirmed from GitHub comment timestamps. Treat the "fixed after review" framing
as plausible context, not proof — the code-reading is the evidence.

Legend: 🔴 real & open · 🟡 minor/doc · ⚪ decision needed · ✅ verified-fixed/FP

## Still open (real)

### 🔴 B3 — collection update-rejection is silent (no signal)

`executor/simple.go recordUpdateRejected`: a rejected SSA *update* of an existing
collection item (e.g. immutable-field `Forbidden`) is tolerated with **no log, no
event, no condition** ("intentionally NOT surfaced … drops the error by design").

- **Correction to the review:** the "publishes a fictional desired render" half is
  **already mitigated** — `recordUpdateRejected` does `desired[i] = current`, so
  `SetObserved(desired,desired)` publishes the *live* object. Downstream scope is
  correct; only the *silence* remains.
- **Severity:** LOW–MEDIUM (operator has zero signal that a desired change never landed).
- **Fix:** in `recordUpdateRejected` emit `log.Info`/Warn with node id + object key +
  the rejection error; add a per-node soft signal (event or a `ResourcesReady=Unknown`
  reason `UpdateRejected`) without escalating to hard error (keep convergence). ~30 LOC + test.

### 🔴 C5 — no CEL eval cancellation; cost-limit off by default; no compile-depth cap

- `CostLimit`+`InterruptCheckFrequency(100)` *are* wired (`expression.go`,
  `--cel-cost-limit`) — but default `celCostLimit=0` ⇒ **cost limit off in prod**.
- All eval is `Program.Eval` (never `ContextEval`) ⇒ **uncancellable**; the interrupt
  frequency has no context to trip, so a runaway consumer comprehension pins a worker.
- `buildSubgraphNode`/`ctx.child()` recursion has **no depth counter** (bounded only by
  yaml nesting).
- Now that instance *data* reaches compilation, cost is consumer-controlled.
- **Severity:** MEDIUM. **Fix (3 parts):** (1) set a non-zero default `celCostLimit`
  for the RGD/instance path (or a floor); (2) thread `ctx` to eval sites and switch
  `Eval`→`ContextEval` (node.go, expression.go, status.go); (3) add a `depth` counter in
  `context.child()` with a max-nesting error. Also cache status-projection programs
  (`status.go compileCEL` recompiles per field per cycle). M-size.

### 🔴 E2 — prune withheld while an owning node blocks on readiness (real, narrower than reported)

Independent trace **corrects the reported mechanism**: a template node that applies
but fails its own `readyWhen` returns `ErrWaitingForReadiness` → `isSoftRuntimeErr` →
`recordSoft`+continue **without** landing in `result.Unresolved`, so `pruneGate(false,[])`
= true and pruning of removed nodes *does* run for that case. The reviewer's and the
first agent's "readyWhen-false vetoes prune" is **not** how HEAD behaves.

- **What DOES still leak:** with `GateReadiness=true` (the RGD path), a node blocked on
  an unready **dependency** (`firstUnreadyDep`) *is* appended to `Unresolved`, and
  `pruneGate` tests `len(unresolved)==0` **globally** — so one owning node stuck behind a
  never-ready dependency withholds pruning of *every* removed node in the graph, silently.
- **Severity:** MEDIUM (was reported HIGH/unbounded). **Fix:** scope the prune veto to the
  removed node's own dependency sub-tree, or distinguish "unresolved identity" (must veto)
  from "resolved-but-gated-on-dependency-readiness" (should not veto unrelated prunes).
  Add a condition telling the user pruning is deferred and why. Needs a small semantics
  decision + test.

### 🔴 E4 — raw eval errors leak into instance-visible conditions

`controller_graph_engine.go`: `GraphResolutionFailed("graph-engine build failed: %v", err)`
and `ResourcesNotReady("resource reconciliation failed: %v", applyErr)` interpolate raw
CEL/type-conversion errors — which can echo author-scope values — into the consumer-readable
instance status (cross-trust-boundary info leak).

- **Severity:** MEDIUM. **Fix:** a sanitizer between executor error and `mark.*`: stable
  reason + node/path in the condition; keep the raw wrapped error at controller log level. ~40 LOC.

### 🔴 F3 — Graph controller head-of-line blocking (default config)

Knob `MaxConcurrentReconciles` now exists but `--graph-concurrent-reconciles` **defaults to 1**,
and `watchrouter.Config{}` ⇒ `SyncTimeout` 30s fallback, with `EnsureWatch`→`WaitForCacheSync`
blocking *inside* reconcile. One Graph on an unwatchable (RBAC-forbidden) GVR starves all Graphs.
Contradicts KREP-024 "across Graphs reconciliation is fully parallel."

- **Severity:** MEDIUM. **Fix:** raise the default worker count (≥ RGD's), set a short
  `watchrouter.Config.SyncTimeout` (5–10s) in `setupGraphController`, and/or move `EnsureWatch`
  cache-sync off the reconcile hot path (register async + requeue). S–M.

### 🔴 F4 — `status.managedResources` unbounded

`api/v1alpha1/graph_types.go:77` has only `Optional`, **no `MaxItems`**; per-node forEach cap is
`DefaultMaxCollectionSize=1000` with **no aggregate** object-wide cap; the write-ahead phase
transiently holds previous∪next. Deletion depends solely on this list, so a status write that
fails on an oversized object jeopardizes teardown.

- **Severity:** MEDIUM. **Fix:** add `+kubebuilder:validation:MaxItems=<N>` on `ManagedResources`
  - a compile-time aggregate-expansion cap across all nodes (reject compile past the cap). Longer
  term, move the inventory out of inline status. `make manifests generate`. S.

### 🔴 F5 — namespace confinement is RBAC-only; KREP-024 overclaims

No code compares a template's namespace to the Graph's own namespace; `defaultNamespace` only
*fills* an empty namespace. A `Namespaced` Graph with a privileged SA can still write cross-namespace
/ cluster-scoped objects. Impersonation commits (9c93295e, a13b3557) harden *who* applies, not *where*.
KREP-024 Security Posture asserts scope-based confinement that does not exist in code.

- **Severity:** MEDIUM (defense-in-depth gap; not a bypass under correct RBAC). **Fix (⚪ decision):**
  either (a) correct graph.md to state confinement is RBAC-only, or (b) add an optional
  executor/admission guard rejecting `metadata.namespace != Graph.namespace` with an explicit opt-out.

## Minor / docs

- 🟡 **C1** — default RGD path has a ≤5-min stale-schema window: the 5-min resolver TTL exists,
  but the only push-invalidator (SchemaWatcher) is behind the `GraphKind` gate, so `geCmp` gets no
  push. Bounded, not indefinite. Optional: wire an ungated CRD-informer invalidation for `geCmp`.
- 🟡 **B4a** — `applyset.Apply`/`ApplyMode`/`ApplyResult`/`ApplySetConflictError` have no production
  caller after the rewire; ~900 lines of `applyset_test.go` exercise dead code. Delete the dead
  surface (keep `Union`/`Project`/`ListOrphans`/`DeleteOrphan`). *Note:* `ErrDuplicateResource` is
  **still live** (`validateAppliedIdentities`) — the review's "lost" claim is false.
- 🟡 **G3** — 14 metrics dropped the `kro_` prefix (rename, not removal). Add a release-note old→new
  table or restore via a shared `Namespace:"kro"`. Alpha disclaimer already covers docs obligation.
- 🟡 **F1 doc rot** — `getAtPath`/`setAtPath` comments still say "dot-only"; code uses the fieldpath
  resolver (array/quoted paths work). Fix the comments.

## Verified fixed or false-positive (reviewer repros predate the fix commits)

> **CORRECTION (see also the 08-28 unresolved set below):** two verdicts here were
> WRONG. **C3** (`registry.go`) and **C4** (`cached.go`) were marked "fixed by the
> epoch guard" — but the epoch guard has a residual **TOCTOU**: `cached.go`
> ResolveSchema reads `curEpoch` under `RLock`, releases the lock, then calls
> `cache.Add` OUTSIDE any lock, so `InvalidateGroupKind` can bump the epoch + clear
> the cache in the check→Add window and a stale schema repopulates. The reviewer's
> still-open 08-28 comment (`cached.go:128`) names exactly this. Same shape at
> `registry.go:135` (absent-epoch snapshot races `Delete`). Both are CONFIRMED-OPEN,
> not fixed. The guard narrows the race; it does not close it.

- **A1** literal-node (`WithLiteralNode(SchemaNodeID)`) — instance data not CEL-parsed. FP/fixed.
- **A2** `kubernetesVersionRegex` has zero call sites on the engine path — never runs. FP.
- **A3** CRD `apiextensions.k8s.io` templates routed through `ParseSchemalessResource`. Fixed.
- **A4** schema Def node typed via `WithNodeSchemaOverride`; cross-node refs type-checked. FP.
- **B1** RGD path still applies under `kro.run/applyset` (unchanged); no dual-owner drop. FP.
- **B2** cross-engine conflict detection present (`ownedByGraphTemplate`, commit f972b758). Fixed.
- **B4b** `ErrDuplicateResource` still enforced. FP.
- **C2** production uses `BuildRuntimeForInstanceCached` + `registry.Registry`; compile once/revision. FP.
- **C3** registry epoch guard present (`7272a1ce`). Fixed.
- **C4** per-GK epoch check before `cache.Add`. Fixed.
- **D1/D3** `nodePrefix`/`qualifiedPath` qualify watch key + node-id label (commit 45c59b95). Fixed.
- **D2** eviction path releases orphaned GVR informer (commit 27e08384). Fixed.
- **D4** schema deps tracked pre-compile; CRD-Add enqueue scoped to that GK + dynamic set. FP.
- **E1** all three revision branches set conditions; `Failed`→`requeue.None`. FP.
- **E3** inventory grown pre-apply (`preApplyApplySetInventory`); prune error propagated. Fixed.
- **E5** `DefaultRequeueDuration==0` honored at every site (`delayedRequeue`/`notReadyRequeue`). FP.
- **E6** capped exponential backoff (commit d3de5ff9). Fixed.
- **F2** `ManagedResources` always persisted; only conditions gated on generation (commit 346ec7b9). Fixed.
- **G1** soft deps DO exist (`Dependency.Soft`, `WithSoftDependencies`); KREP updated to match. FP.
- **G2** `helm/values.yaml` ships `featureGates: {}` (CELOmitFunction only in a comment). FP.
- **G4** `watch:` appears only in comments. FP. **G5** dynamic-GVK `ref:` supported. FP.

## Suggested fix sequencing

1. **Quick, low-risk (one PR):** E4 (sanitize), B3 (log+signal), F1/doc + B4a (dead-code delete),
   G3 (metrics note).
2. **Config/safety (one PR):** F3 (worker default + sync timeout), F4 (MaxItems + aggregate cap),
   C5 part-1 (default cost limit).
3. **Semantics (needs decision + tests):** E2 (scoped prune veto + deferred-prune condition),
   C5 part-2/3 (ContextEval + compile-depth), F5 (namespace guard vs KREP correction).
4. **Optional:** C1 (ungated invalidation for `geCmp`).
