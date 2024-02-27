package test

import (
	"sigs.k8s.io/node-ipam-controller/pkg/apis/clustercidr/v1"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// MakeClusterCIDR creates a ClusterCIDR with given params.
func MakeClusterCIDR(perNodeHostBits int32, name, ipv4, ipv6 string, nodeSelector *corev1.NodeSelector) *v1.ClusterCIDR {
	return &v1.ClusterCIDR{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: v1.ClusterCIDRSpec{
			PerNodeHostBits: perNodeHostBits,
			IPv4:            ipv4,
			IPv6:            ipv6,
			NodeSelector:    nodeSelector,
		},
	}
}

// MakeNodeSelector creates a NodeSelector with given params.
func MakeNodeSelector(key string, op corev1.NodeSelectorOperator, values []string) *corev1.NodeSelector {
	return &corev1.NodeSelector{
		NodeSelectorTerms: []corev1.NodeSelectorTerm{{
			MatchExpressions: []corev1.NodeSelectorRequirement{{
				Key:      key,
				Operator: op,
				Values:   values,
			}},
		}},
	}
}
