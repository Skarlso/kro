// Copyright 2026 The Kubernetes Authors.
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

package executor

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	krotruntime "github.com/kubernetes-sigs/kro/pkg/graphengine/runtime"
	"github.com/kubernetes-sigs/kro/pkg/graphengine/testutil/generator"
	"github.com/kubernetes-sigs/kro/pkg/graphengine/watchrouter"
)

// TestTemplateFieldManager_StableAndPerGraph verifies the per-Graph template
// manager is deterministic (stable across reconciles) and distinct across
// Graphs and across sibling subgraph nodes that reuse a local id.
func TestTemplateFieldManager_StableAndPerGraph(t *testing.T) {
	t.Parallel()

	a := templateFieldManager(types.UID("graph-a"), "cm")
	b := templateFieldManager(types.UID("graph-b"), "cm")

	assert.Equal(t, a, templateFieldManager(types.UID("graph-a"), "cm"), "same (uid,node) is stable")
	assert.NotEqual(t, a, b, "distinct Graph UIDs get distinct managers")
	assert.NotEqual(t,
		templateFieldManager(types.UID("g"), "subA/res"),
		templateFieldManager(types.UID("g"), "subB/res"),
		"sibling subgraph nodes reusing a local id stay distinct")
	assert.Contains(t, a, templateFieldManagerPrefix, "carries the tmpl prefix")
}

// TestOwnedByForeignGraphTemplate classifies a conflicting owner as a peer
// Graph (reject) vs external drift (force-reclaim).
func TestOwnedByForeignGraphTemplate(t *testing.T) {
	t.Parallel()

	self := templateFieldManager(types.UID("me"), "cm")
	peer := templateFieldManager(types.UID("peer"), "cm")

	withManagers := func(names ...string) *unstructured.Unstructured {
		o := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "v1", "kind": "ConfigMap",
			"metadata": map[string]any{"name": "cm", "namespace": "default"},
		}}
		mf := make([]metav1.ManagedFieldsEntry, 0, len(names))
		for _, n := range names {
			mf = append(mf, metav1.ManagedFieldsEntry{Manager: n, Operation: metav1.ManagedFieldsOperationApply})
		}
		o.SetManagedFields(mf)
		return o
	}

	assert.False(t, ownedByForeignGraphTemplate(nil, self), "nil current is never foreign-owned")
	assert.False(t, ownedByForeignGraphTemplate(withManagers(), self), "no managers is never foreign-owned")
	assert.False(t, ownedByForeignGraphTemplate(withManagers(self), self), "self ownership is not foreign")
	assert.False(t, ownedByForeignGraphTemplate(withManagers("kubectl-client-side-apply"), self),
		"a foreign non-kro manager is external drift, not a peer Graph")
	assert.False(t, ownedByForeignGraphTemplate(withManagers(FieldManager), self),
		"the shared RGD field manager is not a Graph template writer")
	assert.True(t, ownedByForeignGraphTemplate(withManagers(peer), self),
		"another Graph's template manager is foreign-owned")
	assert.True(t, ownedByForeignGraphTemplate(withManagers("kubectl", peer), self),
		"a peer Graph among other managers is still foreign-owned")
}

// contestedGraph builds and compiles a Graph that templates a single ConfigMap
// named "contested" in ns with data.owner=owner — Skarlso's repro shape.
func contestedGraph(t *testing.T, name, owner, ns string) *krotruntime.Runtime {
	t.Helper()
	g := generator.NewGraph(name,
		generator.WithNamespace(ns),
		generator.WithTemplate("cm", map[string]any{
			"apiVersion": "v1", "kind": "ConfigMap",
			"metadata": map[string]any{"name": "contested", "namespace": ns},
			"data":     map[string]any{"owner": owner},
		}),
	)
	g.SetUID(types.UID("uid-" + name))
	return compileAndBuildEnv(t, patchEnvCfg, g)
}

// TestTemplate_PeerGraphConflict_NoFlipFlop is the regression test for
// https://github.com/kubernetes-sigs/kro/pull/1355#issuecomment-5412875343:
// two Graphs that template the same ConfigMap with different data must NOT
// flip-flop it. With ConflictDetection on, the first Graph to apply owns the
// field; the second is held soft not-ready (ErrFieldManagerConflict) and never
// overwrites the value. Requires envtest for real SSA managed-field tracking.
func TestTemplate_PeerGraphConflict_NoFlipFlop(t *testing.T) {
	cl := patchEnvClient(t)
	ns := "default"
	ctx := context.Background()

	ownerA := contestedGraph(t, "owner-a", "a", ns)
	ownerB := contestedGraph(t, "owner-b", "b", ns)

	exec := NewSimple(cl).WithConflictDetection(true)

	// owner-a applies first and takes ownership of data.owner.
	resA, err := exec.Apply(ctx, ownerA, watchrouter.NoopWatcher{})
	require.NoError(t, err)
	require.Len(t, resA.Applied, 1)

	cm := getConfigMap(t, cl, ns, "contested")
	data, _, _ := unstructured.NestedStringMap(cm.Object, "data")
	require.Equal(t, "a", data["owner"], "owner-a owns the field after first apply")

	// owner-b now tries to claim the same field: it must be rejected as a soft
	// not-ready conflict, and the value must NOT change across repeated tries.
	for range 3 {
		_, err := exec.Apply(ctx, ownerB, watchrouter.NoopWatcher{})
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrNotReady), "peer conflict is soft not-ready")
		assert.True(t, errors.Is(err, ErrFieldManagerConflict), "peer conflict is a field-manager conflict")

		cm := getConfigMap(t, cl, ns, "contested")
		data, _, _ := unstructured.NestedStringMap(cm.Object, "data")
		assert.Equal(t, "a", data["owner"], "owner-a's value is stable — no flip-flop")
	}

	// owner-a re-applies idempotently: still owns, no conflict, no change.
	_, err = exec.Apply(ctx, ownerA, watchrouter.NoopWatcher{})
	require.NoError(t, err)
	cm = getConfigMap(t, cl, ns, "contested")
	data, _, _ = unstructured.NestedStringMap(cm.Object, "data")
	assert.Equal(t, "a", data["owner"])
}

// TestTemplate_ExternalDrift_ForceReclaimed verifies conflict detection does
// not break drift correction: a foreign (non-kro) manager editing a
// template-owned field is reclaimed by a forced re-apply, so a hand-edit still
// converges back — only a PEER GRAPH is refused.
func TestTemplate_ExternalDrift_ForceReclaimed(t *testing.T) {
	cl := patchEnvClient(t)
	ns := "default"
	ctx := context.Background()

	g := generator.NewGraph("drift",
		generator.WithNamespace(ns),
		generator.WithTemplate("cm", map[string]any{
			"apiVersion": "v1", "kind": "ConfigMap",
			"metadata": map[string]any{"name": "drifted", "namespace": ns},
			"data":     map[string]any{"owner": "kro"},
		}),
	)
	g.SetUID(types.UID("uid-drift"))
	rt := compileAndBuildEnv(t, patchEnvCfg, g)

	exec := NewSimple(cl).WithConflictDetection(true)
	_, err := exec.Apply(ctx, rt, watchrouter.NoopWatcher{})
	require.NoError(t, err)

	// A foreign actor (kubectl-style) force-applies a competing value under its
	// own field manager, taking ownership of data.owner away from kro.
	drift := &unstructured.Unstructured{}
	drift.SetGroupVersionKind(configMapGVK)
	drift.SetNamespace(ns)
	drift.SetName("drifted")
	require.NoError(t, unstructured.SetNestedStringMap(drift.Object, map[string]string{"owner": "human"}, "data"))
	require.NoError(t, cl.Patch(ctx, drift, client.Apply,
		client.FieldOwner("kubectl-edit"), client.ForceOwnership))

	// kro re-applies: the foreign manager is external drift, not a peer Graph,
	// so it is reclaimed by force and converges back to the desired value.
	_, err = exec.Apply(ctx, rt, watchrouter.NoopWatcher{})
	require.NoError(t, err)
	cm := getConfigMap(t, cl, ns, "drifted")
	data, _, _ := unstructured.NestedStringMap(cm.Object, "data")
	assert.Equal(t, "kro", data["owner"], "external drift is reclaimed, not refused")
}
