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

	expv1alpha1 "github.com/kubernetes-sigs/kro/api/v1alpha1"
	"github.com/kubernetes-sigs/kro/pkg/graphengine/compiler"
	"github.com/kubernetes-sigs/kro/pkg/graphengine/executor"
	krotruntime "github.com/kubernetes-sigs/kro/pkg/graphengine/runtime"
	"github.com/kubernetes-sigs/kro/pkg/metadata"
)

// resourceKey is the identity tuple used to dedup ManagedResource
// entries. UID is excluded because pre-apply entries (write-ahead)
// and post-apply entries (with UID) describe the same resource.
type resourceKey struct {
	APIVersion string
	Kind       string
	Namespace  string
	Name       string
}

func keyOf(r expv1alpha1.ManagedResource) resourceKey {
	return resourceKey{
		APIVersion: r.APIVersion,
		Kind:       r.Kind,
		Namespace:  r.Namespace,
		Name:       r.Name,
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

// unionManagedResources concatenates previous and applied, deduping on
// identity. Used when an Apply hit a soft or hard error — we don't
// trust the diff result enough to prune, so we widen status to cover
// everything we know about. Order preserves previous first, then
// newly-applied entries that previous didn't already have.
func unionManagedResources(
	previous []expv1alpha1.ManagedResource,
	applied []expv1alpha1.ManagedResource,
) []expv1alpha1.ManagedResource {
	seen := make(map[resourceKey]struct{}, len(previous)+len(applied))
	out := make([]expv1alpha1.ManagedResource, 0, len(previous)+len(applied))
	for _, r := range previous {
		k := keyOf(r)
		if _, dup := seen[k]; dup {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, r)
	}
	for _, r := range applied {
		k := keyOf(r)
		if _, dup := seen[k]; dup {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, r)
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
