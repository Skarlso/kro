// Copyright 2026 The Kubernetes Authors.
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

package metrics

import "github.com/prometheus/client_golang/prometheus"

// Graph-engine watch-router metrics. These parallel the dynamic controller's
// Dyn* watch metrics but describe the graph engine's watch coordinator. The
// owner gauge is intentionally unlabeled: graph keys are high cardinality, so
// it counts the number of graphs the router tracks rather than labeling by key.
var (
	GraphWatchOwnerCount = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "graphengine_watch_owner_count",
			Help: "Number of graphs currently tracked by the graph-engine watch router",
		},
	)
	GraphWatchRequestCount = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "graphengine_watch_request_count",
			Help: "Number of active graph-engine watch requests by GVR and kind (scalar/collection)",
		},
		[]string{"gvr", "kind"},
	)
	GraphRouteTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "graphengine_route_total",
			Help: "Total events routed through the graph-engine watch router by GVR",
		},
		[]string{"gvr"},
	)
	GraphRouteMatchTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "graphengine_route_match_total",
			Help: "Total events that matched at least one graph by GVR",
		},
		[]string{"gvr"},
	)
	// GraphItemUpdateRejectedTotal counts collection items whose UPDATE was
	// rejected and tolerated, leaving them stale while the node still converges.
	GraphItemUpdateRejectedTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "graphengine_item_update_rejected_total",
			Help: "Total collection items whose update was rejected and tolerated, leaving the item stale, by GVR",
		},
		[]string{"gvr"},
	)
)
