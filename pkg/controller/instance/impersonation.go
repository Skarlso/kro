// Copyright 2025 The Kubernetes Authors.
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

// impersonation.go — resolves the identity kro uses to apply a graph's child
// resources. By default kro impersonates the default ServiceAccount of the
// instance's namespace so a namespaced graph is confined to that namespace's
// RBAC; an author may override the ServiceAccount via
// ResourceGraphDefinitionSpec.ServiceAccountName. The ServiceAccount is always
// resolved in the instance's own namespace, so a namespaced graph can never
// escalate beyond a ServiceAccount it could already use.

package instance

import (
	"context"
	"fmt"
	"sync"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kubernetes-sigs/kro/api/v1alpha1"
	kroclient "github.com/kubernetes-sigs/kro/pkg/client"
	"github.com/kubernetes-sigs/kro/pkg/graphengine/executor"
)

// defaultServiceAccountName is impersonated when a graph does not set
// spec.serviceAccountName, confining child-resource access to the instance's
// namespace by default.
const defaultServiceAccountName = "default"

// impersonatedClients bundles the two client surfaces that touch a graph's
// child resources: the controller-runtime client the executor uses for SSA
// apply/get/list/delete, and the dynamic client the ApplySet uses for prune and
// inventory. Both must share one identity so apply and prune never run as
// different users.
type impersonatedClients struct {
	crClient client.Client
	dynamic  dynamic.Interface
}

// serviceAccountUsername returns the impersonation username for the
// ServiceAccount that should apply inst's child resources, or "" when kro
// should fall back to its own controller identity.
//
// Cluster-scoped instances have no namespace to confine to; they are the
// deliberate escalation path (a cluster-scoped graph requires cluster-level
// RBAC to author), so kro applies their children under its own identity and
// returns "".
func serviceAccountUsername(rgd *v1alpha1.ResourceGraphDefinition, inst *unstructured.Unstructured) string {
	ns := inst.GetNamespace()
	if ns == "" {
		return ""
	}
	name := defaultServiceAccountName
	if sa := rgd.Spec.ServiceAccountName; sa != "" {
		name = sa
	}
	return fmt.Sprintf("system:serviceaccount:%s:%s", ns, name)
}

// impersonationCache memoizes the impersonated client surfaces per username so
// a reconcile does not rebuild a REST client + typed clients on every loop.
// One entry per distinct ServiceAccount, so the cache stays small.
type impersonationCache struct {
	mu     sync.Mutex
	byUser map[string]*impersonatedClients

	// newCRClient builds a controller-runtime client for an impersonated REST
	// config. Overridable so tests can bypass the real REST stack (a fake
	// client set has no usable rest.Config) and reuse an in-memory client.
	newCRClient func(impSet kroclient.SetInterface, mapper meta.RESTMapper) (client.Client, error)
}

func newImpersonationCache() *impersonationCache {
	return &impersonationCache{
		byUser: map[string]*impersonatedClients{},
		newCRClient: func(impSet kroclient.SetInterface, mapper meta.RESTMapper) (client.Client, error) {
			return client.New(impSet.RESTConfig(), client.Options{Mapper: mapper})
		},
	}
}

// get returns cached impersonated clients for user, building them from base on
// first use. base is the kro controller's own client set; its RESTMapper is
// shared with the impersonated controller-runtime client so discovery is not
// repeated per ServiceAccount.
func (c *impersonationCache) get(base kroclient.SetInterface, user string) (*impersonatedClients, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if ic, ok := c.byUser[user]; ok {
		return ic, nil
	}

	impSet, err := base.WithImpersonation(user)
	if err != nil {
		return nil, fmt.Errorf("build impersonated client set for %q: %w", user, err)
	}

	crClient, err := c.newCRClient(impSet, base.RESTMapper())
	if err != nil {
		return nil, fmt.Errorf("build impersonated controller-runtime client for %q: %w", user, err)
	}

	ic := &impersonatedClients{crClient: crClient, dynamic: impSet.Dynamic()}
	c.byUser[user] = ic
	return ic, nil
}

// executorFor returns an executor bound to the impersonated identity for inst,
// derived from the RGD-level base executor so it inherits its configuration
// (concurrency, gating, label injector). When no impersonation applies (e.g. a
// cluster-scoped instance) it returns the base executor unchanged.
func (c *Controller) executorFor(
	base *executor.Simple,
	rgd *v1alpha1.ResourceGraphDefinition,
	inst *unstructured.Unstructured,
) (*executor.Simple, dynamic.Interface, error) {
	user := serviceAccountUsername(rgd, inst)
	if user == "" {
		return base, c.client.Dynamic(), nil
	}

	ic, err := c.impersonation.get(c.client, user)
	if err != nil {
		return nil, nil, err
	}

	// Shadow the base executor with the impersonated client. Copying the value
	// leaves the shared base untouched, matching the existing shadow pattern in
	// ApplyWithLabeler and keeping concurrent instance reconciles safe.
	shadow := *base
	shadow.Client = ic.crClient
	return &shadow, ic.dynamic, nil
}

// resolveChildIdentity resolves the impersonated executor and dynamic client
// for inst's child resources. On failure it marks the instance conditions and
// returns a requeue error, so the caller can simply propagate it.
func (c *Controller) resolveChildIdentity(
	ctx context.Context,
	log logr.Logger,
	rgd *v1alpha1.ResourceGraphDefinition,
	inst *unstructured.Unstructured,
	mark *ConditionsMarker,
) (*executor.Simple, dynamic.Interface, error) {
	childExecutor, childDynamic, err := c.executorFor(c.graphEngineExecutor, rgd, inst)
	if err != nil {
		log.Error(err, "graph-engine: failed to build impersonated client")
		mark.ResourcesNotReady("failed to resolve service account identity: %v", err)
		if updateErr := c.updateConditionsStatus(ctx, inst); updateErr != nil {
			log.V(1).Info("graph-engine: failed to update conditions status", "error", updateErr)
		}
		return nil, nil, c.delayedRequeue(fmt.Errorf("resolve impersonation identity: %w", err))
	}
	return childExecutor, childDynamic, nil
}
