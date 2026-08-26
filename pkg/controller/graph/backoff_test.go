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
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kubernetes-sigs/kro/pkg/graphengine/compiler"
	"github.com/kubernetes-sigs/kro/pkg/graphengine/executor"
	"github.com/kubernetes-sigs/kro/pkg/graphengine/registry"
)

func key(ns, name string) client.ObjectKey {
	return client.ObjectKey{Namespace: ns, Name: name}
}

// TestRequeueBackoffProgression asserts the delay doubles per consecutive
// attempt starting at backoffBase and saturates at backoffMax.
func TestRequeueBackoffProgression(t *testing.T) {
	b := newRequeueBackoff()
	k := key("gtest", "typo-loop")

	// 1s, 2s, 4s, 8s, ... capping at backoffMax (5m).
	want := []time.Duration{
		1 * time.Second,
		2 * time.Second,
		4 * time.Second,
		8 * time.Second,
		16 * time.Second,
		32 * time.Second,
		64 * time.Second,
		128 * time.Second,
		256 * time.Second, // 4m16s, still < 5m
		backoffMax,        // 512s would exceed 5m → capped
		backoffMax,        // stays capped
		backoffMax,
	}
	for i, w := range want {
		got := b.next(k)
		assert.Equalf(t, w, got, "attempt %d", i)
	}
}

// TestRequeueBackoffResetRestartsStreak asserts reset() returns the key to
// backoffBase, modeling a clean reconcile after a fixed typo.
func TestRequeueBackoffResetRestartsStreak(t *testing.T) {
	b := newRequeueBackoff()
	k := key("gtest", "g")

	assert.Equal(t, 1*time.Second, b.next(k))
	assert.Equal(t, 2*time.Second, b.next(k))
	assert.Equal(t, 4*time.Second, b.next(k))

	b.reset(k)

	assert.Equal(t, 1*time.Second, b.next(k), "reset must restart the streak at backoffBase")
	assert.Equal(t, 2*time.Second, b.next(k))
}

// TestRequeueBackoffPerKeyIsolation asserts one Graph's streak does not
// affect another's — a typo in graph A must not back off graph B.
func TestRequeueBackoffPerKeyIsolation(t *testing.T) {
	b := newRequeueBackoff()
	a := key("gtest", "a")
	c := key("gtest", "b")

	assert.Equal(t, 1*time.Second, b.next(a))
	assert.Equal(t, 2*time.Second, b.next(a))
	assert.Equal(t, 4*time.Second, b.next(a))

	// b is independent — first attempt is still backoffBase.
	assert.Equal(t, 1*time.Second, b.next(c))
	// a keeps climbing from where it was.
	assert.Equal(t, 8*time.Second, b.next(a))
}

// TestRequeueBackoffResetUnknownKeyNoop asserts resetting an untracked key
// is a no-op and a subsequent next() starts fresh.
func TestRequeueBackoffResetUnknownKeyNoop(t *testing.T) {
	b := newRequeueBackoff()
	k := key("gtest", "never-seen")
	assert.NotPanics(t, func() { b.reset(k) })
	assert.Equal(t, 1*time.Second, b.next(k))
}

// TestRequeueBackoffNilSafe asserts the nil receiver degrades to a flat
// backoffBase delay without panicking, so a Reconciler that never
// initialized the tracker still requeues.
func TestRequeueBackoffNilSafe(t *testing.T) {
	var b *requeueBackoff
	k := key("gtest", "g")
	assert.NotPanics(t, func() { b.reset(k) })
	assert.Equal(t, backoffBase, b.next(k))
	assert.Equal(t, backoffBase, b.next(k), "nil tracker keeps no state, always backoffBase")
}

// TestRequeueBackoffConcurrent exercises the tracker from many goroutines to
// surface data races under -race.
func TestRequeueBackoffConcurrent(t *testing.T) {
	b := newRequeueBackoff()
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			k := key("gtest", string(rune('a'+n%5)))
			for range 100 {
				_ = b.next(k)
				if n%7 == 0 {
					b.reset(k)
				}
			}
		}(i)
	}
	wg.Wait()
}

// TestReconcileNotReadyBacksOffThenResets drives the reconciler through
// several consecutive ErrNotReady cycles and asserts the RequeueAfter delay
// grows (capped exponential), then that a clean reconcile resets the streak
// back to backoffBase. This is the end-to-end guard for the typo-loop metric
// spike: a never-resolving reference must not requeue at a flat 1s forever.
func TestReconcileNotReadyBacksOffThenResets(t *testing.T) {
	g := graph("g", withFinalizer)
	cl := newClient(t, g)
	exec := &fakeExecutor{applyErr: fmt.Errorf("apply %q: %w", "n", executor.ErrNotReady)}
	r := &Reconciler{
		Client:   cl,
		Compiler: &fakeCompiler{program: &compiler.Program{Nodes: map[string]*compiler.Node{"a": {}}}},
		Registry: registry.New(),
		Executor: exec,
	}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "g"}}

	// Consecutive not-ready reconciles back off: 1s, 2s, 4s, 8s.
	want := []time.Duration{1 * time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second}
	for i, w := range want {
		res, err := r.Reconcile(context.Background(), req)
		require.NoErrorf(t, err, "attempt %d must be a soft requeue, not a hard error", i)
		assert.Equalf(t, w, res.RequeueAfter, "attempt %d", i)
	}

	// The typo is fixed: apply now succeeds. The streak resets.
	exec.applyErr = nil
	res, err := r.Reconcile(context.Background(), req)
	require.NoError(t, err)
	assert.Zero(t, res.RequeueAfter, "a clean reconcile requeues on watch/resync, not a timer")

	// A subsequent stall starts over at backoffBase, not where it left off.
	exec.applyErr = fmt.Errorf("apply %q: %w", "n", executor.ErrNotReady)
	res, err = r.Reconcile(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, backoffBase, res.RequeueAfter, "backoff must restart at base after a clean reconcile")
}
