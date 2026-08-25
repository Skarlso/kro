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
	"sync"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Requeue backoff bounds for the soft ErrNotReady path.
//
// A node whose CEL expression references data the cluster hasn't surfaced
// yet (ErrDataPending, e.g. a pending status field) is requeued rather than
// failed. That data usually appears within a reconcile or two, so the first
// retries are fast. But kro cannot statically tell a genuinely-pending field
// from a permanent typo (`cm.data.nosuchkey`) — both surface as "no such
// key". A flat 1s requeue therefore polls a typo forever, once per second
// per Graph, flooding reconcile metrics and the API server.
//
// Capped exponential backoff keeps the common converging case snappy (first
// retries near backoffBase) while decaying a never-resolving reference to a
// slow poll (backoffMax) instead of a 1/sec hammer. A clean reconcile resets
// the Graph's attempt counter, so a fixed typo returns to fast requeues.
const (
	// backoffBase is the requeue delay for the first not-ready attempt.
	backoffBase = 1 * time.Second
	// backoffMax caps the requeue delay for a persistently not-ready Graph.
	backoffMax = 5 * time.Minute
	// backoffFactor is the per-attempt multiplier (1s, 2s, 4s, … capped).
	backoffFactor = 2
)

// requeueBackoff tracks per-Graph consecutive not-ready attempts so the
// requeue delay can grow with the number of consecutive soft failures. It is
// safe for concurrent use across reconcile workers.
type requeueBackoff struct {
	mu       sync.Mutex
	attempts map[client.ObjectKey]int
}

func newRequeueBackoff() *requeueBackoff {
	return &requeueBackoff{attempts: make(map[client.ObjectKey]int)}
}

// next records another consecutive not-ready attempt for key and returns the
// capped exponential requeue delay to use for it. The first attempt returns
// backoffBase; each subsequent attempt multiplies by backoffFactor up to
// backoffMax.
func (b *requeueBackoff) next(key client.ObjectKey) time.Duration {
	if b == nil {
		return backoffBase
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	n := b.attempts[key]
	b.attempts[key] = n + 1

	delay := backoffBase
	for range n {
		delay *= backoffFactor
		if delay >= backoffMax {
			return backoffMax
		}
	}
	return delay
}

// reset clears the recorded attempts for key, so the next not-ready cycle
// starts again from backoffBase. Called on a clean reconcile and on delete.
func (b *requeueBackoff) reset(key client.ObjectKey) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.attempts, key)
}
