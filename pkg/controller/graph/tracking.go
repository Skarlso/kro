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
	"encoding/json"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	expv1alpha1 "github.com/kubernetes-sigs/kro/api/v1alpha1"
	"github.com/kubernetes-sigs/kro/pkg/graphengine/compiler"
	"github.com/kubernetes-sigs/kro/pkg/graphengine/executor"
	krotruntime "github.com/kubernetes-sigs/kro/pkg/graphengine/runtime"
	"github.com/kubernetes-sigs/kro/pkg/metadata"
)

// resourceKey is the identity tuple used to dedup ManagedResource
// entries. UID is excluded because pre-apply entries (write-ahead)
// and post-apply entries (with UID) describe the same resource.
//
// Identity keys on GROUP + Kind, not the full apiVersion: a CRD's multiple
// served versions all address the SAME stored object, so a version-only
// template change (e.g. apps/v1 -> apps/v2 for the same Kind/namespace/name)
// must NOT make the old-version entry look like a different resource. If it
// did, the old entry would become a prune candidate and Delete — which keys on
// the stable object UID — would delete the very object just applied under the
// new version (a destructive apply-then-prune churn on one object). Keying on
// Group+Kind makes the two versions dedup to one identity.
type resourceKey struct {
	Group     string
	Kind      string
	Namespace string
	Name      string
}

func keyOf(r expv1alpha1.ManagedResource) resourceKey {
	// APIVersion is "group/version" (or just "version" for core); take the group
	// so the version segment does not participate in identity. A parse error
	// (malformed apiVersion) falls back to an empty group, which still yields a
	// stable key for that (malformed) entry.
	group := schema.FromAPIVersionAndKind(r.APIVersion, r.Kind).Group
	return resourceKey{
		Group:     group,
		Kind:      r.Kind,
		Namespace: r.Namespace,
		Name:      r.Name,
	}
}

// diffManagedResources compares the previously-tracked set against the
// just-applied set, accounting for nodes whose identities couldn't be
// resolved this cycle.
//
// Returned newSet is the set the controller wants to advertise after a
// fully-successful Apply (Applied entries + entries preserved from
// previous because their NodeID hit data-pending). pruneCandidates are
// previous entries we're confident are no longer wanted — node dropped
// from spec, forEach shrunk, rename, or includeWhen flipped to false.
//
// Order: newSet preserves the executor's topological apply order from
// result.Applied; preserved entries are appended after, in their
// previous-cycle order. Reverse iteration over newSet still gives a
// reasonable reverse-apply order for delete.
func diffManagedResources(
	previous []expv1alpha1.ManagedResource,
	result executor.ApplyResult,
) ([]expv1alpha1.ManagedResource, []expv1alpha1.ManagedResource) {
	unresolved := make(map[string]struct{}, len(result.Unresolved))
	for _, nodeID := range result.Unresolved {
		unresolved[nodeID] = struct{}{}
	}

	applied := make(map[resourceKey]expv1alpha1.ManagedResource, len(result.Applied))
	for _, r := range result.Applied {
		applied[keyOf(r)] = r
	}

	newSet := make([]expv1alpha1.ManagedResource, 0, len(result.Applied)+len(previous))
	newSet = append(newSet, result.Applied...)

	var pruneCandidates []expv1alpha1.ManagedResource

	for _, prev := range previous {
		if _, alreadyApplied := applied[keyOf(prev)]; alreadyApplied {
			continue
		}
		if _, isUnresolved := unresolved[prev.NodeID]; isUnresolved {
			newSet = append(newSet, prev)
			continue
		}
		pruneCandidates = append(pruneCandidates, prev)
	}
	return newSet, pruneCandidates
}

// intendedManagedResources projects the resource identities a runtime is about
// to apply this cycle, best-effort and without cluster writes — mirroring the
// instance ApplySet controller's candidateMetadata. Def nodes are seeded into
// scope first so template/subgraph nodes referencing `schema.spec...` render in
// memory; then every cleanly-resolving template node contributes its
// identities. It is intentionally lossy (data-pending/ignored/unresolvable
// nodes are skipped) and UID-free: the pre-apply intent superset the reconciler
// write-aheads so a lost status write after Apply still leaves teardown
// something to delete. keyOf excludes UID so post-apply entries dedup against
// their intent entry.
func intendedManagedResources(rt *krotruntime.Runtime) []expv1alpha1.ManagedResource {
	if rt == nil {
		return nil
	}

	// Seed Def nodes into scope first so downstream template expressions that
	// reference them (e.g. `schema.spec...`) can resolve in memory.
	for _, n := range rt.Nodes() {
		if n.Kind() != compiler.NodeKindDef {
			continue
		}
		desired, err := n.Resolve()
		if err != nil || len(desired) == 0 {
			continue
		}
		n.SetObserved(desired, desired)
		if n.IsCollection() {
			list := make([]any, 0, len(desired))
			for _, obj := range desired {
				list = append(list, obj.Object)
			}
			rt.Set(n.ID(), list)
		} else {
			rt.Set(n.ID(), desired[0].Object)
		}
	}

	var out []expv1alpha1.ManagedResource
	seen := make(map[resourceKey]struct{})
	for _, n := range rt.Nodes() {
		// Only template nodes produce owned/torn-down resources (ref = read-only,
		// patch = tracked as contributions, def = no I/O).
		if n.Kind() != compiler.NodeKindTemplate {
			continue
		}
		// A node excluded this cycle (includeWhen:false) won't be applied. An
		// IsIgnored error means we can't decide yet — keep it out (safe: worst
		// case its post-apply Applied entry records it instead).
		if ignored, err := n.IsIgnored(); err == nil && ignored {
			continue
		}
		desired, err := n.Resolve()
		if err != nil {
			continue
		}
		for _, obj := range desired {
			gvk := obj.GroupVersionKind()
			if gvk.Kind == "" || obj.GetName() == "" {
				continue
			}
			// Namespace-default exactly as the executor does before apply
			// (defaultNamespace): a namespaced object with no explicit namespace
			// lands in the Graph's namespace. Without this the intent entry
			// (ns="") would not dedup against the applied entry (ns=graph) and
			// the write-ahead would rewrite status every cycle.
			ns := obj.GetNamespace()
			if ns == "" && n.Namespaced() {
				ns = rt.Graph().GetNamespace()
			}
			// A dynamic-GVK node has no compile-time REST scope (Namespaced() is
			// false), so a rendered object with no explicit namespace can't be
			// namespace-defaulted here the way the executor will at apply time
			// (which resolves the scope from the live RESTMapper). Emitting a
			// ns="" intent entry would never dedup against the applied entry
			// (ns=graph), rewriting status every cycle. Skip it from the
			// write-ahead: its identity is uncertain until apply, and its
			// post-apply Applied entry records it correctly. (A dynamic node that
			// DOES set an explicit namespace keeps its intent entry and dedups.)
			if ns == "" && n.DynamicGVK() {
				continue
			}
			mr := expv1alpha1.ManagedResource{
				NodeID:     n.ID(),
				APIVersion: gvk.GroupVersion().String(),
				Kind:       gvk.Kind,
				Namespace:  ns,
				Name:       obj.GetName(),
			}
			k := keyOf(mr)
			if _, dup := seen[k]; dup {
				continue
			}
			seen[k] = struct{}{}
			out = append(out, mr)
		}
	}
	return out
}

// intendedContributions projects the patch-contribution identities a runtime is
// about to apply this cycle, best-effort and without cluster writes — the patch
// twin of intendedManagedResources. The graph reconciler write-aheads this so a
// crash between Apply (which mutates a patch target) and persistContributions
// (which records the release inventory) still leaves teardown/Release a
// superset to release from, instead of a contributed field stranded on its
// target with no ledger entry.
//
// The projected FieldManager MUST equal what the executor will apply under, or
// the write-ahead entry would never correlate with the contribution Release
// later looks for. Both derive it from executor.PatchFieldManager(graphUID,
// nodeID) — a single shared, pure helper — so they cannot drift. nodeID here is
// the top-level node ID; the executor qualifies with the subgraph frame prefix
// (qualifiedPath), which for a top-level node is the bare ID, matching this
// projection (which, like intendedManagedResources, does not recurse subgraphs).
//
// It is intentionally lossy: a patch node whose target can't be resolved in
// memory yet (references an not-yet-applied resource), is ignored
// (includeWhen:false), or is a dynamic-GVK target with no explicit namespace is
// skipped — its post-apply Contribution records it correctly.
func intendedContributions(rt *krotruntime.Runtime) []executor.Contribution {
	if rt == nil {
		return nil
	}

	// Seed Def nodes into scope so patch targets referencing them (e.g.
	// `schema.spec...`) resolve in memory. Idempotent with any prior seeding.
	for _, n := range rt.Nodes() {
		if n.Kind() != compiler.NodeKindDef {
			continue
		}
		desired, err := n.Resolve()
		if err != nil || len(desired) == 0 {
			continue
		}
		n.SetObserved(desired, desired)
		if n.IsCollection() {
			list := make([]any, 0, len(desired))
			for _, obj := range desired {
				list = append(list, obj.Object)
			}
			rt.Set(n.ID(), list)
		} else {
			rt.Set(n.ID(), desired[0].Object)
		}
	}

	graphUID := rt.Graph().GetUID()
	var out []executor.Contribution
	seen := make(map[contribKey]struct{})
	for _, n := range rt.Nodes() {
		if n.Kind() != compiler.NodeKindPatch {
			continue
		}
		if ignored, err := n.IsIgnored(); err == nil && ignored {
			continue
		}
		desired, err := n.Resolve()
		if err != nil || len(desired) != 1 {
			continue
		}
		obj := desired[0]
		gvk := obj.GroupVersionKind()
		if gvk.Kind == "" || obj.GetName() == "" {
			continue
		}
		ns := obj.GetNamespace()
		if ns == "" && n.Namespaced() {
			ns = rt.Graph().GetNamespace()
		}
		// Same dynamic-GVK-no-namespace ambiguity as intendedManagedResources:
		// the scope isn't known until apply resolves it from the RESTMapper, so a
		// ns="" entry would never correlate. Skip it.
		if ns == "" && n.DynamicGVK() {
			continue
		}
		c := executor.Contribution{
			APIVersion:   gvk.GroupVersion().String(),
			Kind:         gvk.Kind,
			Namespace:    ns,
			Name:         obj.GetName(),
			Subresource:  n.Subresource(),
			FieldManager: executor.PatchFieldManager(graphUID, n.ID()),
		}
		k := contribKeyOf(c)
		if _, dup := seen[k]; dup {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, c)
	}
	return out
}

// unionManagedResources merges previous and applied, deduping on identity.
// Used after a soft or hard Apply error, where the diff isn't trustworthy
// enough to prune, so status is widened to cover everything we know about.
// Order keeps previous first, then applied entries previous didn't have.
//
// Dedup is UID-aware. keyOf excludes UID (so a UID-free write-ahead intent
// entry dedups against its applied counterpart), but on a key collision the
// surviving entry keeps whichever side's UID is set. This matters because
// every caller passes the UID-free previous/intent set first: plain first-wins
// would let it mask the real UID from the SSA response, leaving the resource
// stranded in status with no UID — which Simple.Delete then refuses to delete,
// leaking it on teardown. A later UID also overrides an earlier one, so a
// delete+recreate picks up the fresh UID.
func unionManagedResources(
	previous []expv1alpha1.ManagedResource,
	applied []expv1alpha1.ManagedResource,
) []expv1alpha1.ManagedResource {
	idx := make(map[resourceKey]int, len(previous)+len(applied))
	out := make([]expv1alpha1.ManagedResource, 0, len(previous)+len(applied))
	add := func(r expv1alpha1.ManagedResource) {
		k := keyOf(r)
		if i, dup := idx[k]; dup {
			// Already tracked. Take this entry's UID when set, so a UID-free
			// entry adopts a real UID and a later UID wins on recreate.
			if r.UID != "" {
				out[i].UID = r.UID
			}
			return
		}
		idx[k] = len(out)
		out = append(out, r)
	}
	for _, r := range previous {
		add(r)
	}
	for _, r := range applied {
		add(r)
	}
	return out
}

// contribKey is the identity tuple for a patch contribution. The field
// manager alone is stable per patch node, but the target identity is included
// so a patch whose target name changed releases the old target's fields.
type contribKey struct {
	FieldManager string
	APIVersion   string
	Kind         string
	Namespace    string
	Name         string
	Subresource  string
}

func contribKeyOf(c executor.Contribution) contribKey {
	return contribKey{
		FieldManager: c.FieldManager,
		APIVersion:   c.APIVersion,
		Kind:         c.Kind,
		Namespace:    c.Namespace,
		Name:         c.Name,
		Subresource:  c.Subresource,
	}
}

// ReadContributions parses the persisted patch-contribution inventory off an
// object's annotation. A missing or empty annotation is an empty inventory.
func ReadContributions(obj metav1.Object) ([]executor.Contribution, error) {
	if obj == nil || obj.GetAnnotations() == nil {
		return nil, nil
	}
	raw := obj.GetAnnotations()[metadata.PatchContributionsAnnotation]
	if raw == "" {
		return nil, nil
	}
	var out []executor.Contribution
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf(
			"unmarshal patch contributions from annotation %q: %w",
			metadata.PatchContributionsAnnotation,
			err,
		)
	}
	return out, nil
}

// MarshalContributions renders a contribution inventory as its annotation
// value. An empty inventory renders as "" so the annotation can be dropped.
func MarshalContributions(contribs []executor.Contribution) (string, error) {
	if len(contribs) == 0 {
		return "", nil
	}
	raw, err := json.Marshal(contribs)
	if err != nil {
		return "", fmt.Errorf("marshal patch contributions: %w", err)
	}
	return string(raw), nil
}

// DiffContributions returns the entries present in prior but absent from
// current — the contributions to release because their patch node was removed
// or its target identity changed.
func DiffContributions(prior, current []executor.Contribution) []executor.Contribution {
	cur := make(map[contribKey]struct{}, len(current))
	for _, c := range current {
		cur[contribKeyOf(c)] = struct{}{}
	}
	released := make([]executor.Contribution, 0, len(prior))
	for _, p := range prior {
		if _, ok := cur[contribKeyOf(p)]; !ok {
			released = append(released, p)
		}
	}
	return released
}

// UnionContributions concatenates prior and current, deduping on identity.
// Used when an Apply hit a soft or hard error — the diff isn't trustworthy,
// so the persisted inventory widens to cover everything known.
func UnionContributions(prior, current []executor.Contribution) []executor.Contribution {
	seen := make(map[contribKey]struct{}, len(prior)+len(current))
	out := make([]executor.Contribution, 0, len(prior)+len(current))
	for _, c := range prior {
		k := contribKeyOf(c)
		if _, dup := seen[k]; dup {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, c)
	}
	for _, c := range current {
		k := contribKeyOf(c)
		if _, dup := seen[k]; dup {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, c)
	}
	return out
}
