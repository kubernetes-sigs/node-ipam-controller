/*
Copyright 2014 The Kubernetes Authors.

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

package validation

import (
	"testing"

	"github.com/mneverov/cluster-cidr-controller/pkg/apis/clustercidr/v1"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func makeNodeSelector(key string, op corev1.NodeSelectorOperator, values []string) *corev1.NodeSelector {
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

func makeClusterCIDR(perNodeHostBits int32, ipv4, ipv6 string, nodeSelector *corev1.NodeSelector) *v1.ClusterCIDR {
	return &v1.ClusterCIDR{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "foo",
			ResourceVersion: "9",
		},
		Spec: v1.ClusterCIDRSpec{
			PerNodeHostBits: perNodeHostBits,
			IPv4:            ipv4,
			IPv6:            ipv6,
			NodeSelector:    nodeSelector,
		},
	}
}

func TestValidateClusterCIDR(t *testing.T) {
	testCases := []struct {
		name      string
		cc        *v1.ClusterCIDR
		expectErr bool
	}{
		{
			name:      "valid SingleStack IPv4 ClusterCIDR",
			cc:        makeClusterCIDR(8, "10.1.0.0/16", "", makeNodeSelector("foo", corev1.NodeSelectorOpIn, []string{"bar"})),
			expectErr: false,
		},
		{
			name:      "valid SingleStack IPv4 ClusterCIDR, perNodeHostBits = maxPerNodeHostBits",
			cc:        makeClusterCIDR(16, "10.1.0.0/16", "", makeNodeSelector("foo", corev1.NodeSelectorOpIn, []string{"bar"})),
			expectErr: false,
		},
		{
			name:      "valid SingleStack IPv4 ClusterCIDR, perNodeHostBits > minPerNodeHostBits",
			cc:        makeClusterCIDR(4, "10.1.0.0/16", "", makeNodeSelector("foo", corev1.NodeSelectorOpIn, []string{"bar"})),
			expectErr: false,
		},
		{
			name:      "valid SingleStack IPv6 ClusterCIDR",
			cc:        makeClusterCIDR(8, "", "fd00:1:1::/64", makeNodeSelector("foo", corev1.NodeSelectorOpIn, []string{"bar"})),
			expectErr: false,
		},
		{
			name:      "valid SingleStack IPv6 ClusterCIDR, perNodeHostBits = maxPerNodeHostBit",
			cc:        makeClusterCIDR(64, "", "fd00:1:1::/64", makeNodeSelector("foo", corev1.NodeSelectorOpIn, []string{"bar"})),
			expectErr: false,
		},
		{
			name:      "valid SingleStack IPv6 ClusterCIDR, perNodeHostBits > minPerNodeHostBit",
			cc:        makeClusterCIDR(4, "", "fd00:1:1::/64", makeNodeSelector("foo", corev1.NodeSelectorOpIn, []string{"bar"})),
			expectErr: false,
		},
		{
			name:      "valid SingleStack IPv6 ClusterCIDR perNodeHostBits=100",
			cc:        makeClusterCIDR(100, "", "fd00:1:1::/16", makeNodeSelector("foo", corev1.NodeSelectorOpIn, []string{"bar"})),
			expectErr: false,
		},
		{
			name:      "valid DualStack ClusterCIDR",
			cc:        makeClusterCIDR(8, "10.1.0.0/16", "fd00:1:1::/64", makeNodeSelector("foo", corev1.NodeSelectorOpIn, []string{"bar"})),
			expectErr: false,
		},
		{
			name:      "valid DualStack ClusterCIDR, no NodeSelector",
			cc:        makeClusterCIDR(8, "10.1.0.0/16", "fd00:1:1::/64", nil),
			expectErr: false,
		},
		// Failure cases.
		{
			name:      "invalid ClusterCIDR, no IPv4 or IPv6 CIDR",
			cc:        makeClusterCIDR(8, "", "", nil),
			expectErr: true,
		},
		{
			name:      "invalid ClusterCIDR, invalid nodeSelector",
			cc:        makeClusterCIDR(8, "10.1.0.0/16", "fd00:1:1::/64", makeNodeSelector("NoUppercaseOrSpecialCharsLike=Equals", corev1.NodeSelectorOpIn, []string{"bar"})),
			expectErr: true,
		},
		// IPv4 tests.
		{
			name:      "invalid SingleStack IPv4 ClusterCIDR, invalid spec.IPv4",
			cc:        makeClusterCIDR(8, "test", "", makeNodeSelector("foo", corev1.NodeSelectorOpIn, []string{"bar"})),
			expectErr: true,
		},
		{
			name:      "invalid Singlestack IPv4 ClusterCIDR, perNodeHostBits > maxPerNodeHostBits",
			cc:        makeClusterCIDR(100, "10.1.0.0/16", "", makeNodeSelector("foo", corev1.NodeSelectorOpIn, []string{"bar"})),
			expectErr: true,
		},
		{
			name:      "invalid SingleStack IPv4 ClusterCIDR, perNodeHostBits < minPerNodeHostBits",
			cc:        makeClusterCIDR(2, "10.1.0.0/16", "", makeNodeSelector("foo", corev1.NodeSelectorOpIn, []string{"bar"})),
			expectErr: true,
		},
		// IPv6 tests.
		{
			name:      "invalid SingleStack IPv6 ClusterCIDR, invalid spec.IPv6",
			cc:        makeClusterCIDR(8, "", "testv6", makeNodeSelector("foo", corev1.NodeSelectorOpIn, []string{"bar"})),
			expectErr: true,
		},
		{
			name:      "invalid SingleStack IPv6 ClusterCIDR, valid IPv4 CIDR in spec.IPv6",
			cc:        makeClusterCIDR(8, "", "10.2.0.0/16", makeNodeSelector("foo", corev1.NodeSelectorOpIn, []string{"bar"})),
			expectErr: true,
		},
		{
			name:      "invalid SingleStack IPv6 ClusterCIDR, invalid perNodeHostBits > maxPerNodeHostBits",
			cc:        makeClusterCIDR(12, "", "fd00::/120", makeNodeSelector("foo", corev1.NodeSelectorOpIn, []string{"bar"})),
			expectErr: true,
		},
		{
			name:      "invalid SingleStack IPv6 ClusterCIDR, invalid perNodeHostBits < minPerNodeHostBits",
			cc:        makeClusterCIDR(3, "", "fd00::/120", makeNodeSelector("foo", corev1.NodeSelectorOpIn, []string{"bar"})),
			expectErr: true,
		},
		// DualStack tests
		{
			name:      "invalid DualStack ClusterCIDR, valid spec.IPv4, invalid spec.IPv6",
			cc:        makeClusterCIDR(8, "10.1.0.0/16", "testv6", makeNodeSelector("foo", corev1.NodeSelectorOpIn, []string{"bar"})),
			expectErr: true,
		},
		{
			name:      "invalid DualStack ClusterCIDR, valid spec.IPv6, invalid spec.IPv4",
			cc:        makeClusterCIDR(8, "testv4", "fd00::/120", makeNodeSelector("foo", corev1.NodeSelectorOpIn, []string{"bar"})),
			expectErr: true,
		},
		{
			name:      "invalid DualStack ClusterCIDR, invalid perNodeHostBits > maxPerNodeHostBits",
			cc:        makeClusterCIDR(24, "10.1.0.0/16", "fd00:1:1::/64", makeNodeSelector("foo", corev1.NodeSelectorOpIn, []string{"bar"})),
			expectErr: true,
		},
		{
			name:      "invalid DualStack ClusterCIDR, valid IPv6 CIDR in spec.IPv4",
			cc:        makeClusterCIDR(8, "fd00::/120", "fd00:1:1::/64", makeNodeSelector("foo", corev1.NodeSelectorOpIn, []string{"bar"})),
			expectErr: true,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			err := ValidateClusterCIDR(testCase.cc)
			if !testCase.expectErr && err != nil {
				t.Errorf("ValidateClusterCIDR(%+v) must be successful for test '%s', got %v", testCase.cc, testCase.name, err)
			}
			if testCase.expectErr && err == nil {
				t.Errorf("ValidateClusterCIDR(%+v) must return an error for test: %s, but got nil", testCase.cc, testCase.name)
			}
		})
	}
}

func TestValidateClusterConfigUpdate(t *testing.T) {
	oldCCC := makeClusterCIDR(8, "10.1.0.0/16", "fd00:1:1::/64", makeNodeSelector("foo", corev1.NodeSelectorOpIn, []string{"bar"}))

	testCases := []struct {
		name      string
		cc        *v1.ClusterCIDR
		expectErr bool
	}{{
		name:      "Successful update, no changes to ClusterCIDR.Spec",
		cc:        makeClusterCIDR(8, "10.1.0.0/16", "fd00:1:1::/64", makeNodeSelector("foo", corev1.NodeSelectorOpIn, []string{"bar"})),
		expectErr: false,
	}, {
		name:      "Failed update, update spec.PerNodeHostBits",
		cc:        makeClusterCIDR(12, "10.1.0.0/16", "fd00:1:1::/64", makeNodeSelector("foo", corev1.NodeSelectorOpIn, []string{"bar"})),
		expectErr: true,
	}, {
		name:      "Failed update, update spec.IPv4",
		cc:        makeClusterCIDR(8, "10.2.0.0/16", "fd00:1:1::/64", makeNodeSelector("foo", corev1.NodeSelectorOpIn, []string{"bar"})),
		expectErr: true,
	}, {
		name:      "Failed update, update spec.IPv6",
		cc:        makeClusterCIDR(8, "10.1.0.0/16", "fd00:2:/112", makeNodeSelector("foo", corev1.NodeSelectorOpIn, []string{"bar"})),
		expectErr: true,
	}, {
		name:      "Failed update, update spec.NodeSelector",
		cc:        makeClusterCIDR(8, "10.1.0.0/16", "fd00:1:1::/64", makeNodeSelector("foo", corev1.NodeSelectorOpIn, []string{"bar2"})),
		expectErr: true,
	}}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			err := ValidateClusterCIDRUpdate(testCase.cc, oldCCC)
			if !testCase.expectErr && err != nil {
				t.Errorf("ValidateClusterCIDRUpdate(%+v) must be successful for test '%s', got %v", testCase.cc, testCase.name, err)
			}
			if testCase.expectErr && err == nil {
				t.Errorf("ValidateClusterCIDRUpdate(%+v) must return error for test: %s, but got nil", testCase.cc, testCase.name)
			}
		})
	}
}
