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
	"sync"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Requeue backoff bounds for the soft ErrNotReady path.
//
// A merely-not-ready instance (a node waiting on data/readiness the cluster
// hasn't surfaced yet) is requeued rather than failed. That data usually
// appears within a reconcile or two, so the first retries are fast. But kro
// cannot statically tell a genuinely-pending field from a permanent typo — a
// flat interval therefore polls a never-resolving reference forever, once per
// interval per instance, flooding reconcile metrics and the API server.
//
// Capped exponential backoff keeps the common converging case snappy (first
// retry near the configured base) while decaying a never-resolving reference
// to a slow poll (backoffMax). A clean reconcile resets the instance's attempt
// counter, so a fixed typo returns to fast requeues.
const (
	// backoffBase is the requeue delay for the first not-ready attempt when the
	// operator did not configure DefaultRequeueDuration. When configured, that
	// duration seeds the base instead (see requeueBackoff.base).
	backoffBase = 1 * time.Second
	// backoffMax caps the requeue delay for a persistently not-ready instance.
	backoffMax = 5 * time.Minute
	// backoffFactor is the per-attempt multiplier (base, 2×base, 4×base, …).
	backoffFactor = 2
)

// requeueBackoff tracks per-instance consecutive not-ready attempts so the
// requeue delay can grow with the number of consecutive soft failures. It is
// safe for concurrent use across reconcile workers.
//
// base is the first-attempt delay: it is seeded from the controller's
// configured DefaultRequeueDuration so the operator's interval is honored as
// the FIRST poll and grows ×backoffFactor from there, capped at backoffMax.
type requeueBackoff struct {
	base     time.Duration
	mu       sync.Mutex
	attempts map[client.ObjectKey]int
}

// newRequeueBackoff constructs a tracker whose first-attempt delay is base. A
// non-positive base falls back to backoffBase.
func newRequeueBackoff(base time.Duration) *requeueBackoff {
	if base <= 0 {
		base = backoffBase
	}
	return &requeueBackoff{base: base, attempts: make(map[client.ObjectKey]int)}
}

// next records another consecutive not-ready attempt for key and returns the
// capped exponential requeue delay to use for it. The first attempt returns
// base; each subsequent attempt multiplies by backoffFactor up to backoffMax.
func (b *requeueBackoff) next(key client.ObjectKey) time.Duration {
	if b == nil {
		return backoffBase
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	n := b.attempts[key]
	b.attempts[key] = n + 1

	delay := b.base
	for range n {
		delay *= backoffFactor
		if delay >= backoffMax {
			return backoffMax
		}
	}
	return delay
}

// reset clears the recorded attempts for key, so the next not-ready cycle
// starts again from base. Called on a clean reconcile and on delete.
func (b *requeueBackoff) reset(key client.ObjectKey) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.attempts, key)
}
