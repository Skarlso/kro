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

package instance

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kubernetes-sigs/kro/pkg/graphengine/executor"
	"github.com/kubernetes-sigs/kro/pkg/requeue"
)

func backoffKey(ns, name string) client.ObjectKey {
	return client.ObjectKey{Namespace: ns, Name: name}
}

// TestRequeueBackoffProgression asserts the delay doubles per consecutive
// attempt starting at the seeded base and saturates at backoffMax.
func TestRequeueBackoffProgression(t *testing.T) {
	b := newRequeueBackoff(1 * time.Second)
	k := backoffKey("itest", "typo-loop")

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
		backoffMax,        // 512s would exceed 5m -> capped
		backoffMax,        // stays capped
		backoffMax,
	}
	for i, w := range want {
		got := b.next(k)
		assert.Equalf(t, w, got, "attempt %d", i)
	}
}

// TestRequeueBackoffSeedsFromConfiguredBase asserts the operator's configured
// interval is honored as the FIRST delay and grows x2 from there.
func TestRequeueBackoffSeedsFromConfiguredBase(t *testing.T) {
	b := newRequeueBackoff(3 * time.Second)
	k := backoffKey("itest", "seeded")

	assert.Equal(t, 3*time.Second, b.next(k), "first attempt is the configured base")
	assert.Equal(t, 6*time.Second, b.next(k))
	assert.Equal(t, 12*time.Second, b.next(k))
}

// TestRequeueBackoffNonPositiveBaseFallsBack asserts a zero/negative base
// falls back to backoffBase rather than degenerating to a 0s hammer.
func TestRequeueBackoffNonPositiveBaseFallsBack(t *testing.T) {
	k := backoffKey("itest", "zero")
	assert.Equal(t, backoffBase, newRequeueBackoff(0).next(k))
	assert.Equal(t, backoffBase, newRequeueBackoff(-1*time.Second).next(k))
}

// TestRequeueBackoffResetRestartsStreak asserts reset() returns the key to the
// base delay, modeling a clean reconcile after a fixed reference.
func TestRequeueBackoffResetRestartsStreak(t *testing.T) {
	b := newRequeueBackoff(1 * time.Second)
	k := backoffKey("itest", "i")

	assert.Equal(t, 1*time.Second, b.next(k))
	assert.Equal(t, 2*time.Second, b.next(k))
	assert.Equal(t, 4*time.Second, b.next(k))

	b.reset(k)

	assert.Equal(t, 1*time.Second, b.next(k), "reset must restart the streak at base")
	assert.Equal(t, 2*time.Second, b.next(k))
}

// TestRequeueBackoffPerKeyIsolation asserts one instance's streak does not
// affect another's.
func TestRequeueBackoffPerKeyIsolation(t *testing.T) {
	b := newRequeueBackoff(1 * time.Second)
	a := backoffKey("itest", "a")
	c := backoffKey("itest", "b")

	assert.Equal(t, 1*time.Second, b.next(a))
	assert.Equal(t, 2*time.Second, b.next(a))
	assert.Equal(t, 4*time.Second, b.next(a))

	// c is independent -- first attempt is still base.
	assert.Equal(t, 1*time.Second, b.next(c))
	// a keeps climbing from where it was.
	assert.Equal(t, 8*time.Second, b.next(a))
}

// TestRequeueBackoffResetUnknownKeyNoop asserts resetting an untracked key is a
// no-op and a subsequent next() starts fresh.
func TestRequeueBackoffResetUnknownKeyNoop(t *testing.T) {
	b := newRequeueBackoff(1 * time.Second)
	k := backoffKey("itest", "never-seen")
	assert.NotPanics(t, func() { b.reset(k) })
	assert.Equal(t, 1*time.Second, b.next(k))
}

// TestRequeueBackoffNilSafe asserts the nil receiver degrades to a flat
// backoffBase delay without panicking.
func TestRequeueBackoffNilSafe(t *testing.T) {
	var b *requeueBackoff
	k := backoffKey("itest", "i")
	assert.NotPanics(t, func() { b.reset(k) })
	assert.Equal(t, backoffBase, b.next(k))
	assert.Equal(t, backoffBase, b.next(k), "nil tracker keeps no state, always backoffBase")
}

// TestRequeueBackoffConcurrent exercises the tracker from many goroutines to
// surface data races under -race.
func TestRequeueBackoffConcurrent(t *testing.T) {
	b := newRequeueBackoff(1 * time.Second)
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			k := backoffKey("itest", string(rune('a'+n%5)))
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

// notReadyErr wraps executor.ErrNotReady the way the executor apply path does.
func notReadyErr() error {
	return fmt.Errorf("apply %q: %w", "node", executor.ErrNotReady)
}

// TestNotReadyRequeueBacksOffThenResets drives the soft not-ready return site
// (notReadyRequeue) through several consecutive ErrNotReady cycles for the same
// instance and asserts the RequeueAfter delay grows (capped exponential), then
// that a reset (clean reconcile) restarts the streak at the base. This is the
// unit guard for the metric-flood defect: a never-resolving reference must not
// requeue at a flat interval forever.
func TestNotReadyRequeueBacksOffThenResets(t *testing.T) {
	c := &Controller{
		reconcileConfig: ReconcileConfig{DefaultRequeueDuration: 3 * time.Second},
	}
	c.ensureBackoff()
	k := backoffKey("default", "demo")

	// Consecutive not-ready reconciles back off from the configured base: 3s, 6s, 12s, 24s.
	want := []time.Duration{3 * time.Second, 6 * time.Second, 12 * time.Second, 24 * time.Second}
	for i, w := range want {
		err := c.notReadyRequeue(k, notReadyErr())
		require.Truef(t, requeue.IsRequeueError(err), "attempt %d must be a soft requeue", i)
		ra, ok := err.(*requeue.RequeueNeededAfter)
		require.Truef(t, ok, "attempt %d must be RequeueNeededAfter, got %T", i, err)
		assert.Equalf(t, w, ra.Duration(), "attempt %d", i)
	}

	// The reference resolves: a clean reconcile resets the streak.
	c.backoff.reset(k)

	// A subsequent stall starts over at the base, not where it left off.
	err := c.notReadyRequeue(k, notReadyErr())
	ra, ok := err.(*requeue.RequeueNeededAfter)
	require.True(t, ok)
	assert.Equal(t, 3*time.Second, ra.Duration(), "backoff must restart at base after a clean reconcile")
}

// TestNotReadyRequeueCapsAtMax asserts a persistently not-ready instance decays
// to backoffMax rather than growing without bound.
func TestNotReadyRequeueCapsAtMax(t *testing.T) {
	c := &Controller{
		reconcileConfig: ReconcileConfig{DefaultRequeueDuration: 1 * time.Second},
	}
	c.ensureBackoff()
	k := backoffKey("default", "capped")

	var last time.Duration
	for range 40 {
		err := c.notReadyRequeue(k, notReadyErr())
		ra, ok := err.(*requeue.RequeueNeededAfter)
		require.True(t, ok)
		last = ra.Duration()
	}
	assert.Equal(t, backoffMax, last, "delay must saturate at backoffMax")
}

// TestNotReadyRequeueDisabledHonorsNone asserts that when the operator disabled
// delayed requeues (DefaultRequeueDuration==0), the soft not-ready path emits
// requeue.None and does not force a timer.
func TestNotReadyRequeueDisabledHonorsNone(t *testing.T) {
	c := &Controller{
		reconcileConfig: ReconcileConfig{DefaultRequeueDuration: 0},
	}
	c.ensureBackoff()
	k := backoffKey("default", "disabled")

	err := c.notReadyRequeue(k, notReadyErr())
	_, isAfter := err.(*requeue.RequeueNeededAfter)
	assert.False(t, isAfter, "disabled requeues must not force a timed requeue")
	_, isNone := err.(*requeue.NoRequeue)
	assert.True(t, isNone, "disabled requeues must emit requeue.None")
}
