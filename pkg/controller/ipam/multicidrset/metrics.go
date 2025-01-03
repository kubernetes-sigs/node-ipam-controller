/*
Copyright 2022 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package multicidrset

import (
	"github.com/prometheus/client_golang/prometheus"
)

const nodeIpamSubsystem = "node_ipam_controller"

var (
	cidrSetAllocations = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: nodeIpamSubsystem,
			Name:      "multicidrset_cidrs_allocations_total",
			Help:      "Counter measuring total number of CIDR allocations.",
		},
		[]string{"clusterCIDR", "clusterCIDRName"},
	)
	cidrSetReleases = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: nodeIpamSubsystem,
			Name:      "multicidrset_cidrs_releases_total",
			Help:      "Counter measuring total number of CIDR releases.",
		},
		[]string{"clusterCIDR", "clusterCIDRName"},
	)
	// This is a gauge, as in theory, a limit can increase or decrease.
	cidrSetMaxCidrs = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Subsystem: nodeIpamSubsystem,
			Name:      "multicidrset_max_cidrs",
			Help:      "Maximum number of CIDRs that can be allocated.",
		},
		[]string{"clusterCIDR", "clusterCIDRName"},
	)
	cidrSetUsage = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Subsystem: nodeIpamSubsystem,
			Name:      "multicidrset_usage_cidrs",
			Help:      "Gauge measuring percentage of allocated CIDRs.",
		},
		[]string{"clusterCIDR", "clusterCIDRName"},
	)
	cidrSetAllocationTriesPerRequest = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Subsystem: nodeIpamSubsystem,
			Name:      "multicidrset_allocation_tries_per_request",
			Help:      "Histogram measuring CIDR allocation tries per request.",
			Buckets:   prometheus.ExponentialBuckets(1, 5, 5),
		},
		[]string{"clusterCIDR", "clusterCIDRName"},
	)
)

func init() {
	prometheus.MustRegister(cidrSetAllocations)
	prometheus.MustRegister(cidrSetReleases)
	prometheus.MustRegister(cidrSetMaxCidrs)
	prometheus.MustRegister(cidrSetUsage)
	prometheus.MustRegister(cidrSetAllocationTriesPerRequest)
}
