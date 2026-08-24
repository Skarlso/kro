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

package graph

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	expv1alpha1 "github.com/kubernetes-sigs/kro/api/v1alpha1"
	"github.com/kubernetes-sigs/kro/pkg/graphengine/executor"
)

func graphWithSA(namespace, sa string) *expv1alpha1.Graph {
	return &expv1alpha1.Graph{
		ObjectMeta: metav1.ObjectMeta{Name: "g", Namespace: namespace},
		Spec:       expv1alpha1.GraphSpec{ServiceAccountName: sa},
	}
}

// TestServiceAccountUsername pins the identity kro resolves for a Graph's
// resources: the default ServiceAccount of the Graph's namespace by default, an
// explicit override when set, always resolved in the Graph's own namespace.
func TestServiceAccountUsername(t *testing.T) {
	tests := []struct {
		name string
		g    *expv1alpha1.Graph
		want string
	}{
		{
			name: "default service account confines to graph namespace",
			g:    graphWithSA("team-a", ""),
			want: "system:serviceaccount:team-a:default",
		},
		{
			name: "explicit service account override",
			g:    graphWithSA("team-a", "deployer"),
			want: "system:serviceaccount:team-a:deployer",
		},
		{
			name: "override is resolved in the graph namespace, not elsewhere",
			g:    graphWithSA("team-b", "deployer"),
			want: "system:serviceaccount:team-b:deployer",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, serviceAccountUsername(tt.g))
		})
	}
}

// TestExecutorFor_ImpersonationOverride verifies that the ServiceAccount
// override drives which impersonated executor a Graph resolves, that the
// executor is cached per username, and that a distinct namespace/SA builds a
// distinct executor.
func TestExecutorFor_ImpersonationOverride(t *testing.T) {
	base := executor.NewSimple(fake.NewClientBuilder().Build())

	var builtFor []string
	r := &Reconciler{
		Executor: base,
		Impersonation: NewImpersonation(base, func(user string) (client.Client, error) {
			builtFor = append(builtFor, user)
			return fake.NewClientBuilder().Build(), nil
		}),
	}

	// Override ServiceAccount → impersonate system:serviceaccount:team-a:deployer.
	ex1, err := r.executorFor(graphWithSA("team-a", "deployer"))
	require.NoError(t, err)
	assert.NotSame(t, base, ex1, "impersonated executor must not be the base executor")
	require.Equal(t, []string{"system:serviceaccount:team-a:deployer"}, builtFor)

	// Same Graph identity → cached, no new client built.
	ex1b, err := r.executorFor(graphWithSA("team-a", "deployer"))
	require.NoError(t, err)
	assert.Same(t, ex1, ex1b, "same username must return the cached executor")
	require.Len(t, builtFor, 1, "cached username must not rebuild the client")

	// Different namespace → distinct impersonated executor.
	ex2, err := r.executorFor(graphWithSA("team-b", "deployer"))
	require.NoError(t, err)
	assert.NotSame(t, ex1, ex2)
	require.Equal(t, []string{
		"system:serviceaccount:team-a:deployer",
		"system:serviceaccount:team-b:deployer",
	}, builtFor)
}

// TestExecutorFor_DefaultServiceAccount verifies that without an override, a
// Graph impersonates the default ServiceAccount of its namespace.
func TestExecutorFor_DefaultServiceAccount(t *testing.T) {
	base := executor.NewSimple(fake.NewClientBuilder().Build())
	var capturedUser string
	r := &Reconciler{
		Executor: base,
		Impersonation: NewImpersonation(base, func(user string) (client.Client, error) {
			capturedUser = user
			return fake.NewClientBuilder().Build(), nil
		}),
	}

	_, err := r.executorFor(graphWithSA("team-a", ""))
	require.NoError(t, err)
	assert.Equal(t, "system:serviceaccount:team-a:default", capturedUser)
}

// TestExecutorFor_NoImpersonationFallsBackToBase verifies that when
// impersonation is not wired (e.g. unit tests, or a build that leaves it off),
// the Graph's resources are applied with the base executor / kro identity.
func TestExecutorFor_NoImpersonationFallsBackToBase(t *testing.T) {
	base := executor.NewSimple(fake.NewClientBuilder().Build())
	r := &Reconciler{Executor: base} // Impersonation nil

	ex, err := r.executorFor(graphWithSA("team-a", "deployer"))
	require.NoError(t, err)
	assert.Same(t, base, ex, "without impersonation wired, must use the base executor unchanged")
}
