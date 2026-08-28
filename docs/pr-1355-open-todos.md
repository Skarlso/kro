# PR #1355 — Open review TODOs (round of 2026-08-25, cheeseandcereal)

Status legend: 🔴 needs code + decision · 🟠 code fix (scoped) · 🟡 docs/cleanup · ⚪ needs maintainer decision

14 findings from this review round are **fixed and resolved** (commits 7129d31d, 458e9220,
5ee924da, d8086ccb, 444a4be3, d24aa30c, ef8ccc3d, 1a2f7842), plus finding #8 (def+forEach row
collapse, commit e184948a) and the three docs/cleanup items #14/#15/#16 (commits 9108368a,
107dc529). The 13 below remain open.

> **CI note:** commit `97b9476b` fixed the integration failures (Graph Tracking / Drift /
> Schema Watch `managedResources` length churn) — the write-ahead intent now namespace-defaults
> to match applied entries. A separate flake in `crd_test.go` (CRD Watch Reconciliation / CRD
> deletion, 3 specs) reproduces identically on the base commit `7dd5dbc0` under parallel run and
> is **pre-existing shared-CRD-state flakiness**, not a regression from this work.

---

## Security / auth model — needs a design decision first (⚪🔴)

These are best landed as a cluster with a documented auth model (`helm/templates/cluster-role.yaml:146`
asks for docs of "whatever we land").

1. **✅ DONE (commit 9c93295e)** — ~~Self-SA escalation → cluster-admin~~ — chose guard **A**:
   the reconciler refuses a Graph whose resolved impersonation username equals the controller's
   OWN SA (via `--controller-namespace`/`--controller-service-account`, wired from downward API +
   Helm SA name), marking `Accepted=False(InvalidGraph)` before compile/apply. Only ever matches a
   Graph in the controller namespace. Other privileged SAs = operator RBAC concern. Unit test + docs.

2. **✅ DONE (commit a13b3557)** — ~~Watch/informer read path not impersonated~~ — SAR gate:
   the executor's optional `CanWatch` hook issues a `SelfSubjectAccessReview` (verb=watch) under the
   Graph's SA before registering a drift watch, skipping it (degraded, logged) when denied. Wired via
   `NewImpersonation` from the same impersonated config; nil/inert on the RGD path.

3. **✅ DONE (commit 572a61ee)** — ~~Impersonation fail-open + zero E2E coverage~~ — added
   `RequireImpersonation` (production + integration set it true → refuse rather than fall back to
   controller identity when unwired; unit tests leave it false to keep the fallback). Integration
   harness now wires the real impersonated client + authz path (fixing the false "match production
   wiring" comment); a new self-contained RBAC-enforcing envtest suite proves confinement (forbidden
   write refused + resource never created).

4. **🟢 `Release()` trusts user-writable annotation** — `pkg/controller/graph/tracking.go:231`
   **Decision: declined a code guard; replied on-thread (left unresolved for reviewer).** Release runs
   under the impersonated SA (forged annotation is SA-confined — no escalation, esp. after the
   fail-closed change #3), is non-destructive (relinquishes a field-manager ownership claim, never
   deletes / never changes values — unlike `Delete()`, so the UID-precondition asymmetry is
   intentional), and self-heals on clean-apply. Offered the cheap per-Graph-segment guard as optional
   belt-and-suspenders if the reviewer wants it.

5. **✅ DONE (commit 6dde3aef)** — ~~Teardown uses current `spec.serviceAccountName`~~ — the
   applying identity is now persisted in `GraphStatus.AppliedServiceAccount` on any apply that reaches
   the cluster (never on a hard failure); teardown resolves the executor from that persisted identity
   (`teardownExecutorFor`), falling back to the current spec when unset. Unit tests cover
   persisted-over-edited-spec, fallback, and no-impersonation.

6. **✅ DONE (commit 9c93295e)** — ~~Auth-model docs~~ — the Security Posture section in graph.md now
   documents the impersonation model and the controller self-impersonation guard from item 1.

---

## Behavior / correctness — scoped fixes (🟠) some need a semantics decision (⚪)

1. **✅ DONE (commit e184948a)** — ~~`def`+`forEach` publishes N copies of the last row~~ — `pkg/graphengine/runtime/collection.go`
orderedIntersection now falls back to positional alignment when identityKey can't distinguish
rows; identity-bearing collections unchanged. Regression tests added.

2. **✅ DONE (commit 4bebe142)** — ~~`TolerateDataPending` not applied to array elements~~ — Option A:
   a data-pending array element now omits the WHOLE enclosing array field for that render
   (`enclosingArrayFieldPath` + `RemoveNestedField` post-resolve), so the array is absent this cycle
   (not partial/index-shifted) and reappears complete once the upstream resolves. Every other status
   field still renders → instance reaches ACTIVE, prune gate no longer wedged. Subtests cover single +
   nested array omission and the non-tolerant whole-node data-pend.

3. **🟠 EnsureWatch failure hard-aborts the whole instance apply** — ✅ DONE (commit a13b3557)
   Watch registration is now soft-failed on both the Graph and RGD/instance paths (see security #2/#17):
   a can't-watch GVR degrades drift detection instead of aborting Apply. Only watch registration is
   soft; GET/LIST/APPLY errors unchanged.

4. **✅ DONE (commit a5f7f57c)** — ~~RGD-path flat requeue (backoff only on Graph path)~~ — the
    RGD/instance graph-engine path now uses a per-instance capped exponential requeue backoff for the
    soft not-ready case (seeded from `DefaultRequeueDuration`, capped at 5m, resets on converge,
    honors `==0` disable). Unit tests cover progression/cap/reset/isolation/disabled/concurrency.

5. **✅ DONE (commit b89452a8)** — ~~`applySetConflict` asymmetry — silent foreign-field adoption~~ —
    Option A (cross-engine detection): the RGD path (ConflictDetection off) now refuses any live object
    carrying a kro Graph template field manager (soft not-ready), mirroring the Graph path's
    peer-conflict refusal. No invalid applyset label stamped — the field-manager signal is used
    instead; the `!ConflictDetection` gate ensures it never fires on a Graph's own object. Tests cover
    scalar + collection refusal, RGD own/unowned still applies, Graph still refuses RGD-owned.

6. **✅ DONE (commit a3b24f71)** — ~~`managedResources` uid: CRD says optional, code skips on delete~~ —
    fixed the DOC (code behavior is correct): the uid field comment + published CRD now state a
    UID-less entry is a not-yet-observed intent, intentionally skipped on delete (deleting by name
    alone could remove an object kro doesn't own), and becomes deletable only once a successful apply
    records its UID. No behavior change.

7. **⚪ Dynamic GVKs only for template/patch, not `ref:`** — `pkg/graphengine/compiler/context.go:213`
    `graph.md` documents dynamic GVK for `ref:`; `examples/graph/rgd.yaml` (the KREP's headline proof)
    cannot compile. **Decision needed:** implement dynamic-GVK `ref:` nodes, or descope the example/docs.

8. **✅ DONE** — ~~Cross-namespace apply leaks the successfully-applied object on teardown~~ — Skarlso's
    `crossns` repro (a Graph with one in-namespace ConfigMap + one foreign-namespace ConfigMap the
    impersonated SA can't write): the local object was created in the cluster with a real UID, yet its
    `status.managedResources` entry had an **empty UID**, so `Simple.Delete` (simple.go:466, refuses
    UID-less entries) skipped it on Graph delete → permanent orphan. Root cause: `unionManagedResources`
    (tracking.go) was first-wins on `keyOf` (UID-excluded), and **every** call site lists the UID-free
    write-ahead/`previous` set FIRST — so when a post-apply status write took the stale-generation branch
    (`updateStatus`:664, hit because the reviewer edited the Graph to "fix" it) the UID-free intent entry
    masked the authoritative SSA UID from `result.Applied`, stranding it in status. Fix: made
    `unionManagedResources` UID-aware — a duplicate key adopts a non-empty UID from either side (and a
    later UID refreshes an earlier one for delete+recreate), so the SSA UID always survives regardless of
    argument order. Regression tests added in `tracking_test.go` (intent-adopts-applied-UID,
    known-UID-survives-UID-free, later-UID-refreshes-on-recreate). NOTE: the separate
    `defaultNamespace`-only-fills-empty scope concern (foreign object should arguably be refused, not
    applied cross-namespace) is the security/scope finding tracked elsewhere and is unchanged by this fix.

---

## Docs / cleanup (🟡)

 1. **✅ DONE (commit 107dc529)** — ~~apply-order wave values shifted~~ — documented the
    `ApplyOrderAnnotation` one-based semantics (leaf = wave 1) at its definition; value is advisory,
    only relative ordering is contractual. The leaves=1 shift matches the documented scheme.

 2. **✅ DONE (commit 107dc529)** — ~~stale template force/FM docs~~ — `graph.md:291`/`:116` now
    describe the per-Graph field-manager + conflict-detection model (RGD path = kro.run/applyset+force;
    Graph path = per-Graph managers without force, reject peer / reclaim drift / exempt same-Graph).

 3. **✅ DONE (commit 9108368a)** — ~~Apache header divergence~~ — normalized all 23 AWS-body files to
    the canonical header. Verified `make fmt` (nwa) already stamps the canonical variant, so the
    reviewer's premise (config stamps the new variant) did not hold; config + tree now agree.

 4. **⚪ `.golangci.yml` disables all of staticcheck** — `.golangci.yml:73`
    `checks: [-QF1008]` → 0 active staticcheck checks (0 issues w/ repo config vs 24 w/ defaults). This
    PR adds 9 of the 24, incl. 3 uses of deprecated `client.Apply`. Pre-existing misconfig. **Decision
    needed:** fix config + all 24 now, fix only this PR's 9, or leave it (risk: cascades beyond PR scope).
