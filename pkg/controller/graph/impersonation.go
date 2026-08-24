// Copyright 2026 The Kubernetes Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License"). You may
// not use this file except in compliance with the License. A copy of the
// License is located at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// or in the "license" file accompanying this file. This file is distributed
// on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either
// express or implied. See the License for the specific language governing
// permissions and limitations under the License.

// impersonation.go — resolves the identity kro uses to apply a Graph's
// resources. Unlike ResourceGraphDefinition instances (which apply children
// with the kro controller identity), a namespaced Graph applies its resources
// while impersonating a ServiceAccount in the Graph's own namespace. By default
// this is the namespace's default ServiceAccount, confining resource access to
// that namespace; GraphSpec.ServiceAccountName overrides which ServiceAccount
// is used. This keeps a namespaced Graph from escalating beyond a ServiceAccount
// it could already use.

package graph

import (
	"fmt"
	"sync"

	"sigs.k8s.io/controller-runtime/pkg/client"

	expv1alpha1 "github.com/kubernetes-sigs/kro/api/v1alpha1"
	"github.com/kubernetes-sigs/kro/pkg/graphengine/executor"
)

// defaultServiceAccountName is impersonated when a Graph does not set
// spec.serviceAccountName, confining resource access to the Graph's namespace
// by default.
const defaultServiceAccountName = "default"

// serviceAccountUsername returns the impersonation username for the
// ServiceAccount that should apply g's resources. The ServiceAccount is always
// resolved in the Graph's own namespace, so a Graph can only ever impersonate a
// ServiceAccount it could already use.
func serviceAccountUsername(g *expv1alpha1.Graph) string {
	name := defaultServiceAccountName
	if sa := g.Spec.ServiceAccountName; sa != "" {
		name = sa
	}
	return fmt.Sprintf("system:serviceaccount:%s:%s", g.GetNamespace(), name)
}

// impersonationCache memoizes an impersonated executor per username so a
// reconcile does not rebuild a REST client on every loop. One entry per
// distinct ServiceAccount, so the cache stays small.
type impersonationCache struct {
	mu      sync.Mutex
	byUser  map[string]executor.Interface
	newExec func(user string) (executor.Interface, error)
}

// executorFor returns an executor bound to the impersonated identity for g. It
// is derived from the base executor via the reconciler's client factory so it
// inherits the base configuration (apply concurrency). Results are cached per
// impersonation username.
func (r *Reconciler) executorFor(g *expv1alpha1.Graph) (executor.Interface, error) {
	if r.Impersonation == nil || r.Impersonation.newExec == nil {
		// Impersonation not wired (e.g. unit tests): fall back to the base
		// executor and the kro controller identity.
		return r.Executor, nil
	}

	user := serviceAccountUsername(g)

	r.Impersonation.mu.Lock()
	defer r.Impersonation.mu.Unlock()
	if r.Impersonation.byUser == nil {
		r.Impersonation.byUser = map[string]executor.Interface{}
	}
	if ex, ok := r.Impersonation.byUser[user]; ok {
		return ex, nil
	}
	ex, err := r.Impersonation.newExec(user)
	if err != nil {
		return nil, fmt.Errorf("build impersonated executor for %q: %w", user, err)
	}
	r.Impersonation.byUser[user] = ex
	return ex, nil
}

// NewImpersonation builds an impersonation cache whose executors are shadows of
// base with an impersonated controller-runtime client. newClient builds a
// controller-runtime client for the given impersonation username (typically via
// client.New over a rest.Config carrying rest.ImpersonationConfig); it is a
// parameter so tests can supply an in-memory client instead of a real REST
// stack.
func NewImpersonation(base *executor.Simple, newClient func(user string) (client.Client, error)) *impersonationCache {
	return &impersonationCache{
		byUser: map[string]executor.Interface{},
		newExec: func(user string) (executor.Interface, error) {
			cl, err := newClient(user)
			if err != nil {
				return nil, err
			}
			// Copy the base executor so the shared instance is untouched, then
			// point the copy at the impersonated client.
			shadow := *base
			shadow.Client = cl
			return &shadow, nil
		},
	}
}
