// Copyright 2025 The Kube Resource Orchestrator Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package graph

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	expv1alpha1 "github.com/kubernetes-sigs/kro/api/v1alpha1"
	"github.com/kubernetes-sigs/kro/pkg/graphengine/compiler"
	"github.com/kubernetes-sigs/kro/pkg/graphengine/executor"
	"github.com/kubernetes-sigs/kro/pkg/graphengine/registry"
	krotruntime "github.com/kubernetes-sigs/kro/pkg/graphengine/runtime"
	"github.com/kubernetes-sigs/kro/pkg/graphengine/watchrouter"
	"github.com/kubernetes-sigs/kro/pkg/metadata"
)

// templateProgram builds a minimal compiled Program with a single static
// Template node whose rendered identity is (apiVersion, kind, namespace,
// name). No Variables/ForEach/IncludeWhen, so the node resolves in memory to
// exactly that object — enough for intendedManagedResources to project it.
func templateProgram(nodeID, apiVersion, kind, namespace, name string) *compiler.Program {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": apiVersion,
		"kind":       kind,
		"metadata": map[string]any{
			"name":      name,
			"namespace": namespace,
		},
	}}
	gv, _ := schema.ParseGroupVersion(apiVersion)
	node := &compiler.Node{
		ID:         nodeID,
		Kind:       compiler.NodeKindTemplate,
		GVR:        gv.WithKind(kind).GroupVersion().WithResource(kind),
		Namespaced: namespace != "",
		Object:     obj,
	}
	return &compiler.Program{
		Nodes:            map[string]*compiler.Node{nodeID: node},
		TopologicalOrder: []string{nodeID},
	}
}

// emptyNodeProgram builds a Program whose single node has no payload, so
// intendedManagedResources projects nothing and the pre-apply write-ahead is
// skipped. Used to isolate reconcile paths from the Finding A write-ahead.
func emptyNodeProgram(nodeID string) *compiler.Program {
	node := &compiler.Node{ID: nodeID, Kind: compiler.NodeKindTemplate}
	return &compiler.Program{
		Nodes:            map[string]*compiler.Node{nodeID: node},
		TopologicalOrder: []string{nodeID},
	}
}

// applyObservingExecutor records the ManagedResources PERSISTED on the API
// server at the moment Apply is entered, by reading them back through a
// captured client. This is what lets a test assert the pre-apply write-ahead
// (Finding A) landed on the server before any resource was applied.
type applyObservingExecutor struct {
	fakeExecutor
	cl  client.Client
	key types.NamespacedName
	// persistedAtApply is the server-side inventory observed when Apply ran.
	persistedAtApply []expv1alpha1.ManagedResource
	observed         bool
}

func (e *applyObservingExecutor) Apply(ctx context.Context, rt *krotruntime.Runtime, w watchrouter.Watcher) (executor.ApplyResult, error) {
	got := &expv1alpha1.Graph{}
	if err := e.cl.Get(ctx, e.key, got); err == nil {
		e.persistedAtApply = got.Status.ManagedResources
		e.observed = true
	}
	return e.fakeExecutor.Apply(ctx, rt, w)
}

// TestReconcile_WriteAheadIntentPersistedBeforeApply is the Finding A
// regression: the inventory teardown depends on must be durable on the API
// server BEFORE Apply creates any child. Before the fix the reconciler applied
// first and persisted the inventory only afterwards, so a lost status write
// after apply orphaned children (delete would see 0 entries). The fix
// write-aheads the union of previous + intended identities before Apply.
func TestReconcile_WriteAheadIntentPersistedBeforeApply(t *testing.T) {
	t.Parallel()
	key := types.NamespacedName{Namespace: "default", Name: "g"}

	g := graph("g", withFinalizer)
	cl := newClient(t, g)

	obs := &applyObservingExecutor{cl: cl, key: key}
	fc := &fakeCompiler{program: templateProgram("widget", "example.com/v1", "Widget", "default", "w")}
	r := &Reconciler{Client: cl, Compiler: fc, Registry: registry.New(), Executor: obs}

	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key})
	require.NoError(t, err)

	require.True(t, obs.observed, "Apply must have been called")
	// The server-side inventory observed AT apply time must already contain
	// the intended Widget identity. Pre-fix this slice is empty.
	require.Len(t, obs.persistedAtApply, 1,
		"pre-apply intent must be persisted to the API server before Apply runs")
	mr := obs.persistedAtApply[0]
	assert.Equal(t, "example.com/v1", mr.APIVersion)
	assert.Equal(t, "Widget", mr.Kind)
	assert.Equal(t, "w", mr.Name)
	assert.Equal(t, "widget", mr.NodeID)
}

// TestIntendedManagedResources_ProjectsTemplateIdentities is a focused unit
// test for the projection helper Finding A relies on.
func TestIntendedManagedResources_ProjectsTemplateIdentities(t *testing.T) {
	t.Parallel()
	g := graph("g")
	prog := templateProgram("widget", "example.com/v1", "Widget", "default", "w")
	rt := krotruntime.New(prog, g)

	got := intendedManagedResources(rt)
	require.Len(t, got, 1)
	assert.Equal(t, "example.com/v1", got[0].APIVersion)
	assert.Equal(t, "Widget", got[0].Kind)
	assert.Equal(t, "w", got[0].Name)
	assert.Empty(t, got[0].UID, "pre-apply intent carries no UID")
}

// TestIntendedManagedResources_SkipsDynamicGVKWithoutNamespace pins the
// tracking.go:161 fix: a dynamic-GVK node has no compile-time REST scope
// (Namespaced()==false), so a rendered object with NO explicit namespace can't
// be namespace-defaulted in the projection the way the executor will at apply
// time. Emitting a ns="" intent entry would never dedup against the applied
// entry (ns=graph), churning status every cycle — so it must be skipped. A
// dynamic node that DOES set an explicit namespace keeps its intent entry.
func TestIntendedManagedResources_SkipsDynamicGVKWithoutNamespace(t *testing.T) {
	t.Parallel()
	g := graph("g") // namespace "default"

	dynNoNS := func(nodeID, name, namespace string) *compiler.Node {
		obj := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "example.com/v1",
			"kind":       "Widget",
			"metadata":   map[string]any{"name": name},
		}}
		if namespace != "" {
			_ = unstructured.SetNestedField(obj.Object, namespace, "metadata", "namespace")
		}
		return &compiler.Node{
			ID:         nodeID,
			Kind:       compiler.NodeKindTemplate,
			DynamicGVK: true,
			Namespaced: false, // dynamic: unknown at compile time
			Object:     obj,
		}
	}

	t.Run("dynamic node without explicit namespace is skipped", func(t *testing.T) {
		n := dynNoNS("dyn", "w", "")
		prog := &compiler.Program{
			Nodes:            map[string]*compiler.Node{"dyn": n},
			TopologicalOrder: []string{"dyn"},
		}
		got := intendedManagedResources(krotruntime.New(prog, g))
		assert.Empty(t, got, "a dynamic-GVK node with no explicit namespace must not emit a ns=\"\" intent entry")
	})

	t.Run("dynamic node with explicit namespace is kept", func(t *testing.T) {
		n := dynNoNS("dyn", "w", "other-ns")
		prog := &compiler.Program{
			Nodes:            map[string]*compiler.Node{"dyn": n},
			TopologicalOrder: []string{"dyn"},
		}
		got := intendedManagedResources(krotruntime.New(prog, g))
		require.Len(t, got, 1, "an explicit namespace is a stable identity and must be tracked")
		assert.Equal(t, "other-ns", got[0].Namespace)
		assert.Equal(t, "w", got[0].Name)
	})
}

// lostStatusWriteClient drops the FIRST N status Patch calls (returning nil so
// the reconciler believes they succeeded) then delegates. It simulates a lost
// status write — the exact crash window Finding A guards.
//
// (Retained as documentation of the crash model; Finding B's regression uses
// patchErrClient to fail the terminal status write directly.)

// TestReconcile_StatusWriteErrorNotDiscardedOnNotReady is the Finding B
// regression: when Apply returns a soft ErrNotReady AND updateStatus fails,
// the joined error still matched errors.Is(ErrNotReady) and the reconcile
// returned nil, silently discarding the status-write failure. The fix keeps
// the status-write error separate and surfaces it regardless of the not-ready
// branch.
func TestReconcile_StatusWriteErrorNotDiscardedOnNotReady(t *testing.T) {
	t.Parallel()
	key := types.NamespacedName{Namespace: "default", Name: "g"}

	g := graph("g", withFinalizer)
	cl := newClient(t, g)
	// updateStatus is the reconcile's terminal status Patch. Fail it.
	wrapped := &patchErrClient{Client: cl, statusErr: errors.New("status boom")}

	exec := &fakeExecutor{applyErr: fmt.Errorf("apply %q: %w", "n", executor.ErrNotReady)}
	// Use an empty (payload-less) node so the pre-apply write-ahead projects
	// nothing and does NOT fire — this isolates the failing write to the
	// TERMINAL updateStatus, which is exactly the path Finding B guards.
	r := &Reconciler{Client: wrapped, Compiler: &fakeCompiler{program: emptyNodeProgram("n")}, Registry: registry.New(), Executor: exec}

	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key})
	// Pre-fix: err == nil (status failure swallowed by the ErrNotReady branch).
	require.Error(t, err, "a failed status write must never be discarded, even on soft not-ready")
	assert.Contains(t, err.Error(), "status boom")
}

// TestReconcile_ReleaseReachableOnSoftNotReady is the reachability half of
// Finding C: when Apply returns a soft ErrNotReady, the early apply-error
// TestReconcile_NoReleaseOnSoftNotReady pins the corrected Finding C contract:
// release of patch contributions runs on the CLEAN-apply path ONLY. On a soft
// ErrNotReady, a patch node's contribution is absent from result.Contributions
// whether it was genuinely removed OR is merely data-pending this cycle, and
// executor.Contribution carries no NodeID to tell those apart — so releasing
// here would drop fields a still-wanted patch node set (a transient flap). The
// field-manager-identity-change deadlock this path once tried to break is now
// fixed at the source in the executor (contributeApply force-reclaims a
// same-Graph stale patch identity), so no controller-side release on soft
// errors is needed.
func TestReconcile_NoReleaseOnSoftNotReady(t *testing.T) {
	t.Parallel()
	key := types.NamespacedName{Namespace: "default", Name: "g"}

	// Prior contribution recorded on the Graph; this cycle Apply reports NO
	// contributions and a soft ErrNotReady with the patch node Unresolved
	// (data-pending, still wanted).
	prior := []executor.Contribution{{
		APIVersion:   "v1",
		Kind:         "ConfigMap",
		Namespace:    "default",
		Name:         "target",
		FieldManager: "kro-graphengine.patch.oldidentity",
	}}
	raw, err := MarshalContributions(prior)
	require.NoError(t, err)

	g := graph("g", withFinalizer, func(g *expv1alpha1.Graph) {
		if g.Annotations == nil {
			g.Annotations = map[string]string{}
		}
		g.Annotations[metadata.PatchContributionsAnnotation] = raw
	})
	cl := newClient(t, g)

	exec := &fakeExecutor{
		applyErr:    fmt.Errorf("apply %q (patch): %w", "p", executor.ErrNotReady),
		applyResult: executor.ApplyResult{Unresolved: []string{"p"}},
	}
	r := &Reconciler{Client: cl, Compiler: &fakeCompiler{program: emptyNodeProgram("n")}, Registry: registry.New(), Executor: exec}

	_, _ = r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key})

	// The prior contribution must NOT be released on a soft not-ready cycle:
	// a data-pending patch node is still wanted, and releasing its fields would
	// flap them until the node resolves next cycle.
	assert.Empty(t, exec.releaseCalls, "release must not fire on soft not-ready (would flap a data-pending patch's fields)")
}

// TestReconcile_SoftNotReadyStillPrunesRetiredNode pins finding 357: a node
// that is soft not-ready this cycle must NOT veto pruning of an UNRELATED
// resource whose owning node was removed from the spec. Previously all pruning
// was gated on a fully clean apply, so one never-ready node leaked every
// retired resource until it resolved. diffManagedResources keeps unresolved
// nodes' entries, so a prune candidate on a soft cycle is genuinely retired and
// safe to delete.
func TestReconcile_SoftNotReadyStillPrunesRetiredNode(t *testing.T) {
	t.Parallel()
	key := types.NamespacedName{Namespace: "default", Name: "g"}

	// Previously-tracked resource owned by node "gone", which is no longer in
	// the graph. A separate node "widget" is not-ready this cycle.
	g := graph("g", withFinalizer, func(g *expv1alpha1.Graph) {
		g.Status.ManagedResources = []expv1alpha1.ManagedResource{{
			NodeID:     "gone",
			APIVersion: "v1",
			Kind:       "ConfigMap",
			Namespace:  "default",
			Name:       "retired-cm",
			UID:        "uid-retired",
		}}
	})
	cl := newClient(t, g)

	// Apply: soft not-ready, node "widget" Unresolved, nothing applied. The
	// "gone" resource is neither Applied nor Unresolved -> a prune candidate.
	exec := &fakeExecutor{
		applyErr:    fmt.Errorf("apply %q: %w", "widget", executor.ErrNotReady),
		applyResult: executor.ApplyResult{Unresolved: []string{"widget"}},
	}
	r := &Reconciler{Client: cl, Compiler: &fakeCompiler{program: emptyNodeProgram("widget")}, Registry: registry.New(), Executor: exec}

	_, _ = r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key})

	// The retired resource must have been pruned despite the soft not-ready.
	require.Len(t, exec.deleteCalls, 1, "prune must run on a soft not-ready cycle for a retired node")
	require.Len(t, exec.deleteCalls[0], 1)
	assert.Equal(t, "retired-cm", exec.deleteCalls[0][0].Name,
		"the retired node's resource is the prune candidate")

	// Persisted status must no longer track the pruned resource.
	got := &expv1alpha1.Graph{}
	require.NoError(t, cl.Get(context.Background(), key, got))
	for _, mr := range got.Status.ManagedResources {
		assert.NotEqual(t, "retired-cm", mr.Name, "a successfully pruned resource must drop from status")
	}
}

// TestReconcile_ErrorPathKeepsIntentSuperset guards the Finding A hardening:
// on a soft apply error the in-memory status (which the terminal updateStatus
// overwrites onto the server) must not shrink below the written-ahead intent,
// so a partially-applied resource still has a durable inventory entry.
func TestReconcile_ErrorPathKeepsIntentSuperset(t *testing.T) {
	t.Parallel()
	key := types.NamespacedName{Namespace: "default", Name: "g"}

	g := graph("g", withFinalizer)
	cl := newClient(t, g)

	// Apply reports a soft not-ready and an EMPTY Applied set (nothing observed
	// this cycle), simulating a crash/partial apply. The intent projected from
	// the template must still land in persisted status.
	exec := &fakeExecutor{
		applyErr:    fmt.Errorf("apply %q: %w", "widget", executor.ErrNotReady),
		applyResult: executor.ApplyResult{Unresolved: []string{"widget"}},
	}
	fc := &fakeCompiler{program: templateProgram("widget", "example.com/v1", "Widget", "default", "w")}
	r := &Reconciler{Client: cl, Compiler: fc, Registry: registry.New(), Executor: exec}

	_, _ = r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key})

	got := &expv1alpha1.Graph{}
	require.NoError(t, cl.Get(context.Background(), key, got))
	require.Len(t, got.Status.ManagedResources, 1,
		"intent superset must survive a soft-error cycle in persisted status")
	assert.Equal(t, "Widget", got.Status.ManagedResources[0].Kind)
	assert.Equal(t, "w", got.Status.ManagedResources[0].Name)
}
