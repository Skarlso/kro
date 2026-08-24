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

package instance

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kubernetes-sigs/kro/api/v1alpha1"
	kroclient "github.com/kubernetes-sigs/kro/pkg/client"
	clientfake "github.com/kubernetes-sigs/kro/pkg/client/fake"
	"github.com/kubernetes-sigs/kro/pkg/graphengine/executor"
)

func rgdWithServiceAccount(name string) *v1alpha1.ResourceGraphDefinition {
	return &v1alpha1.ResourceGraphDefinition{
		Spec: v1alpha1.ResourceGraphDefinitionSpec{ServiceAccountName: name},
	}
}

// TestServiceAccountUsername pins the identity kro resolves for a graph's child
// resources: default ServiceAccount of the instance namespace by default, an
// explicit override when set, and no impersonation for cluster-scoped instances.
func TestServiceAccountUsername(t *testing.T) {
	tests := []struct {
		name      string
		rgd       *v1alpha1.ResourceGraphDefinition
		namespace string
		want      string
	}{
		{
			name:      "default service account confines to instance namespace",
			rgd:       rgdWithServiceAccount(""),
			namespace: "team-a",
			want:      "system:serviceaccount:team-a:default",
		},
		{
			name:      "explicit service account override",
			rgd:       rgdWithServiceAccount("deployer"),
			namespace: "team-a",
			want:      "system:serviceaccount:team-a:deployer",
		},
		{
			name:      "override is still resolved in the instance namespace, not elsewhere",
			rgd:       rgdWithServiceAccount("deployer"),
			namespace: "team-b",
			want:      "system:serviceaccount:team-b:deployer",
		},
		{
			name:      "cluster-scoped instance has no namespace to confine to -> no impersonation",
			rgd:       rgdWithServiceAccount("deployer"),
			namespace: "",
			want:      "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inst := &unstructured.Unstructured{}
			inst.SetName("demo")
			if tt.namespace != "" {
				inst.SetNamespace(tt.namespace)
			}
			got := serviceAccountUsername(tt.rgd, inst)
			assert.Equal(t, tt.want, got)
		})
	}
}

// spyCRClient captures the impersonated set handed to the controller-runtime
// client factory so a test can assert which identity was used.
type spyCRClient struct {
	client.Client
	set kroclient.SetInterface
}

// TestExecutorFor_ImpersonationOverride verifies that the ServiceAccount
// override drives the impersonated identity all the way through: the executor's
// controller-runtime client and the ApplySet's dynamic client are both built
// from the set impersonating system:serviceaccount:<ns>:<override>.
func TestExecutorFor_ImpersonationOverride(t *testing.T) {
	raw := newControllerTestDynamicClient(t)
	base := clientfake.NewFakeSet(raw)
	base.SetRESTMapper(buildControllerTestRESTMapper())

	c := &Controller{
		client:        base,
		impersonation: newImpersonationCache(),
	}

	// Capture the impersonated set that the CR-client factory receives.
	var captured *spyCRClient
	c.impersonation.newCRClient = func(impSet kroclient.SetInterface, _ meta.RESTMapper) (client.Client, error) {
		captured = &spyCRClient{set: impSet}
		return captured, nil
	}

	baseExec := executor.NewSimple(nil)
	inst := newInstanceObject("demo", "team-a")

	gotExec, gotDyn, err := c.executorFor(baseExec, rgdWithServiceAccount("deployer"), inst)
	require.NoError(t, err)

	// The executor is shadowed with the impersonated client, not the base.
	require.NotNil(t, captured)
	assert.Same(t, captured, gotExec.Client, "executor must use the impersonated controller-runtime client")
	assert.NotSame(t, baseExec, gotExec, "base executor must not be mutated")

	// The impersonated set carries the resolved override username, and its
	// dynamic client is the one returned for ApplySet prune/inventory.
	impSet, ok := captured.set.(*clientfake.FakeSet)
	require.True(t, ok)
	assert.Equal(t, "system:serviceaccount:team-a:deployer", impSet.ImpersonatedUser)
	assert.Same(t, impSet.Dynamic(), gotDyn, "ApplySet dynamic client must be the impersonated one")
}

// TestExecutorFor_DefaultServiceAccount verifies that without an override, kro
// impersonates the default ServiceAccount of the instance namespace.
func TestExecutorFor_DefaultServiceAccount(t *testing.T) {
	raw := newControllerTestDynamicClient(t)
	base := clientfake.NewFakeSet(raw)
	base.SetRESTMapper(buildControllerTestRESTMapper())

	c := &Controller{client: base, impersonation: newImpersonationCache()}
	var capturedUser string
	c.impersonation.newCRClient = func(impSet kroclient.SetInterface, _ meta.RESTMapper) (client.Client, error) {
		capturedUser = impSet.(*clientfake.FakeSet).ImpersonatedUser
		return &spyCRClient{set: impSet}, nil
	}

	_, _, err := c.executorFor(executor.NewSimple(nil), rgdWithServiceAccount(""), newInstanceObject("demo", "team-a"))
	require.NoError(t, err)
	assert.Equal(t, "system:serviceaccount:team-a:default", capturedUser)
}

// TestExecutorFor_ClusterScopedInstanceSkipsImpersonation verifies that a
// cluster-scoped instance (no namespace) applies children under kro's own
// identity: the base executor and the base dynamic client are returned
// unchanged, and no impersonated client is built.
func TestExecutorFor_ClusterScopedInstanceSkipsImpersonation(t *testing.T) {
	raw := newControllerTestDynamicClient(t)
	base := clientfake.NewFakeSet(raw)
	base.SetRESTMapper(buildControllerTestRESTMapper())

	c := &Controller{client: base, impersonation: newImpersonationCache()}
	factoryCalled := false
	c.impersonation.newCRClient = func(impSet kroclient.SetInterface, _ meta.RESTMapper) (client.Client, error) {
		factoryCalled = true
		return &spyCRClient{set: impSet}, nil
	}

	baseExec := executor.NewSimple(nil)
	inst := newInstanceObject("demo", "") // cluster-scoped: no namespace

	gotExec, gotDyn, err := c.executorFor(baseExec, rgdWithServiceAccount("deployer"), inst)
	require.NoError(t, err)
	assert.Same(t, baseExec, gotExec, "cluster-scoped instance must use the base executor unchanged")
	assert.Same(t, base.Dynamic(), gotDyn, "cluster-scoped instance must use kro's own dynamic client")
	assert.False(t, factoryCalled, "no impersonated client should be built for a cluster-scoped instance")
}

// TestImpersonationCache_ReusesPerUser verifies the cache builds one impersonated
// client set per distinct username and reuses it across reconciles.
func TestImpersonationCache_ReusesPerUser(t *testing.T) {
	raw := newControllerTestDynamicClient(t)
	base := clientfake.NewFakeSet(raw)
	base.SetRESTMapper(buildControllerTestRESTMapper())

	cache := newImpersonationCache()
	builds := 0
	cache.newCRClient = func(impSet kroclient.SetInterface, _ meta.RESTMapper) (client.Client, error) {
		builds++
		return &spyCRClient{set: impSet}, nil
	}

	first, err := cache.get(base, "system:serviceaccount:team-a:deployer")
	require.NoError(t, err)
	second, err := cache.get(base, "system:serviceaccount:team-a:deployer")
	require.NoError(t, err)
	assert.Same(t, first, second, "same username must return the cached clients")
	assert.Equal(t, 1, builds, "cached username must not rebuild the client")

	_, err = cache.get(base, "system:serviceaccount:team-b:deployer")
	require.NoError(t, err)
	assert.Equal(t, 2, builds, "a distinct username must build a new client")
}
