/*
Copyright 2023 The Kubernetes Authors.

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

package v1

import (
	api "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ClusterCIDR represents a single configuration for per-Node Pod CIDR
// allocations when the MultiCIDRRangeAllocator is enabled (see the config for
// kube-controller-manager).  A cluster may have any number of ClusterCIDR
// resources, all of which will be considered when allocating a CIDR for a
// Node.  A ClusterCIDR is eligible to be used for a given Node when the node
// selector matches the node in question and has free CIDRs to allocate.  In
// case of multiple matching ClusterCIDR resources, the allocator will attempt
// to break ties using internal heuristics, but any ClusterCIDR whose node
// selector matches the Node may be used.
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
// +genclient
// +genclient:noStatus
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type ClusterCIDR struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec ClusterCIDRSpec `json:"spec,omitempty"`
}

// Default implements webhook.Defaulter so a webhook will be registered for the type.
func (in *ClusterCIDR) Default() {}

// ClusterCIDRSpec defines the desired state of ClusterCIDR.
type ClusterCIDRSpec struct {
	// nodeSelector defines which nodes the config is applicable to.
	// An empty or nil nodeSelector selects all nodes.
	// This field is optional and immutable.
	// +optional
	NodeSelector *api.NodeSelector `json:"nodeSelector,omitempty"`

	// perNodeHostBits4 defines the number of host bits to be configured per node when IPv4 family is used.
	// A subnet mask determines how much of the address is used for network bits
	// and host bits. For example an IPv4 address of 192.168.0.0/24, splits the
	// address into 24 bits for the network portion and 8 bits for the host portion.
	// To allocate 256 IPs, set this field to 8 (a /24 mask).
	// Minimum value is 4 (16 IPs).
	// This field is required when IPv4 family is used and immutable.
	// +optional
	PerNodeHostBits4 int32 `json:"perNodeHostBits4"`

	// perNodeHostBits6 defines the number of host bits to be configured per node when IPv6 family is used.
	// A subnet mask determines how much of the address is used for network bits
	// and host bits. For example an IPv4 address of 192.168.0.0/24, splits the
	// address into 24 bits for the network portion and 8 bits for the host portion.
	// To allocate 256 IPs, set this field to 8 (a /120 mask).
	// Minimum value is 4 (16 IPs).
	// This field is required when IPv6 family is used and immutable.
	// +optional
	PerNodeHostBits6 int32 `json:"perNodeHostBits6"`

	// ipv4 defines an IPv4 IP block in CIDR notation(e.g. "10.0.0.0/8").
	// At least one of ipv4 and ipv6 must be specified.
	// This field is optional and immutable.
	// +optional
	IPv4 string `json:"ipv4,omitempty"`

	// ipv6 defines an IPv6 IP block in CIDR notation(e.g. "2001:db8::/64").
	// At least one of ipv4 and ipv6 must be specified.
	// This field is optional and immutable.
	// +optional
	IPv6 string `json:"ipv6,omitempty"`
}

// ClusterCIDRList contains a list of ClusterCIDRs.
// +kubebuilder:object:root=true
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type ClusterCIDRList struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ListMeta `json:"metadata,omitempty"`

	// items is the list of ClusterCIDRs.
	Items []ClusterCIDR `json:"items"`
}
