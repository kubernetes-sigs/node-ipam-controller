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

package validator

import (
	"testing"

	"sigs.k8s.io/node-ipam-controller/pkg/apis/clustercidr/v1"
	"sigs.k8s.io/node-ipam-controller/pkg/util/test"

	corev1 "k8s.io/api/core/v1"
)

func TestValidateClusterCIDR(t *testing.T) {
	testCases := []struct {
		name      string
		cc        *v1.ClusterCIDR
		expectErr bool
	}{
		{
			name:      "valid SingleStack IPv4 ClusterCIDR",
			cc:        test.MakeClusterCIDR(8, "test-clustercidr", "10.1.0.0/16", "", test.MakeNodeSelector("test-clustercidr", corev1.NodeSelectorOpIn, []string{"bar"})),
			expectErr: false,
		},
		{
			name:      "valid SingleStack IPv4 ClusterCIDR, perNodeHostBits = maxPerNodeHostBits",
			cc:        test.MakeClusterCIDR(16, "test-clustercidr", "10.1.0.0/16", "", test.MakeNodeSelector("test-clustercidr", corev1.NodeSelectorOpIn, []string{"bar"})),
			expectErr: false,
		},
		{
			name:      "valid SingleStack IPv4 ClusterCIDR, perNodeHostBits > minPerNodeHostBits",
			cc:        test.MakeClusterCIDR(4, "test-clustercidr", "10.1.0.0/16", "", test.MakeNodeSelector("test-clustercidr", corev1.NodeSelectorOpIn, []string{"bar"})),
			expectErr: false,
		},
		{
			name:      "valid SingleStack IPv6 ClusterCIDR",
			cc:        test.MakeClusterCIDR(8, "test-clustercidr", "", "fd00:1:1::/64", test.MakeNodeSelector("test-clustercidr", corev1.NodeSelectorOpIn, []string{"bar"})),
			expectErr: false,
		},
		{
			name:      "valid SingleStack IPv6 ClusterCIDR, perNodeHostBits = maxPerNodeHostBit",
			cc:        test.MakeClusterCIDR(64, "test-clustercidr", "", "fd00:1:1::/64", test.MakeNodeSelector("test-clustercidr", corev1.NodeSelectorOpIn, []string{"bar"})),
			expectErr: false,
		},
		{
			name:      "valid SingleStack IPv6 ClusterCIDR, perNodeHostBits > minPerNodeHostBit",
			cc:        test.MakeClusterCIDR(4, "test-clustercidr", "", "fd00:1:1::/64", test.MakeNodeSelector("test-clustercidr", corev1.NodeSelectorOpIn, []string{"bar"})),
			expectErr: false,
		},
		{
			name:      "valid SingleStack IPv6 ClusterCIDR perNodeHostBits=100",
			cc:        test.MakeClusterCIDR(100, "test-clustercidr", "", "fd00:1:1::/16", test.MakeNodeSelector("test-clustercidr", corev1.NodeSelectorOpIn, []string{"bar"})),
			expectErr: false,
		},
		{
			name:      "valid DualStack ClusterCIDR",
			cc:        test.MakeClusterCIDR(8, "test-clustercidr", "10.1.0.0/16", "fd00:1:1::/64", test.MakeNodeSelector("test-clustercidr", corev1.NodeSelectorOpIn, []string{"bar"})),
			expectErr: false,
		},
		{
			name:      "valid DualStack ClusterCIDR, no NodeSelector",
			cc:        test.MakeClusterCIDR(8, "test-clustercidr", "10.1.0.0/16", "fd00:1:1::/64", nil),
			expectErr: false,
		},
		// Failure cases.
		{
			name:      "invalid ClusterCIDR, no IPv4 or IPv6 CIDR",
			cc:        test.MakeClusterCIDR(8, "test-clustercidr", "", "", nil),
			expectErr: true,
		},
		{
			name:      "invalid ClusterCIDR, invalid nodeSelector",
			cc:        test.MakeClusterCIDR(8, "test-clustercidr", "10.1.0.0/16", "fd00:1:1::/64", test.MakeNodeSelector("NoUppercaseOrSpecialCharsLike=Equals", corev1.NodeSelectorOpIn, []string{"bar"})),
			expectErr: true,
		},
		// IPv4 tests.
		{
			name:      "invalid SingleStack IPv4 ClusterCIDR, invalid spec.IPv4",
			cc:        test.MakeClusterCIDR(8, "test-clustercidr", "test", "", test.MakeNodeSelector("test-clustercidr", corev1.NodeSelectorOpIn, []string{"bar"})),
			expectErr: true,
		},
		{
			name:      "invalid Singlestack IPv4 ClusterCIDR, perNodeHostBits > maxPerNodeHostBits",
			cc:        test.MakeClusterCIDR(100, "test-clustercidr", "10.1.0.0/16", "", test.MakeNodeSelector("test-clustercidr", corev1.NodeSelectorOpIn, []string{"bar"})),
			expectErr: true,
		},
		{
			name:      "invalid SingleStack IPv4 ClusterCIDR, perNodeHostBits < minPerNodeHostBits",
			cc:        test.MakeClusterCIDR(2, "test-clustercidr", "10.1.0.0/16", "", test.MakeNodeSelector("test-clustercidr", corev1.NodeSelectorOpIn, []string{"bar"})),
			expectErr: true,
		},
		// IPv6 tests.
		{
			name:      "invalid SingleStack IPv6 ClusterCIDR, invalid spec.IPv6",
			cc:        test.MakeClusterCIDR(8, "test-clustercidr", "", "testv6", test.MakeNodeSelector("test-clustercidr", corev1.NodeSelectorOpIn, []string{"bar"})),
			expectErr: true,
		},
		{
			name:      "invalid SingleStack IPv6 ClusterCIDR, valid IPv4 CIDR in spec.IPv6",
			cc:        test.MakeClusterCIDR(8, "test-clustercidr", "", "10.2.0.0/16", test.MakeNodeSelector("test-clustercidr", corev1.NodeSelectorOpIn, []string{"bar"})),
			expectErr: true,
		},
		{
			name:      "invalid SingleStack IPv6 ClusterCIDR, invalid perNodeHostBits > maxPerNodeHostBits",
			cc:        test.MakeClusterCIDR(12, "test-clustercidr", "", "fd00::/120", test.MakeNodeSelector("test-clustercidr", corev1.NodeSelectorOpIn, []string{"bar"})),
			expectErr: true,
		},
		{
			name:      "invalid SingleStack IPv6 ClusterCIDR, invalid perNodeHostBits < minPerNodeHostBits",
			cc:        test.MakeClusterCIDR(3, "test-clustercidr", "", "fd00::/120", test.MakeNodeSelector("test-clustercidr", corev1.NodeSelectorOpIn, []string{"bar"})),
			expectErr: true,
		},
		// DualStack tests
		{
			name:      "invalid DualStack ClusterCIDR, valid spec.IPv4, invalid spec.IPv6",
			cc:        test.MakeClusterCIDR(8, "test-clustercidr", "10.1.0.0/16", "testv6", test.MakeNodeSelector("test-clustercidr", corev1.NodeSelectorOpIn, []string{"bar"})),
			expectErr: true,
		},
		{
			name:      "invalid DualStack ClusterCIDR, valid spec.IPv6, invalid spec.IPv4",
			cc:        test.MakeClusterCIDR(8, "test-clustercidr", "testv4", "fd00::/120", test.MakeNodeSelector("test-clustercidr", corev1.NodeSelectorOpIn, []string{"bar"})),
			expectErr: true,
		},
		{
			name:      "invalid DualStack ClusterCIDR, invalid perNodeHostBits > maxPerNodeHostBits",
			cc:        test.MakeClusterCIDR(24, "test-clustercidr", "10.1.0.0/16", "fd00:1:1::/64", test.MakeNodeSelector("test-clustercidr", corev1.NodeSelectorOpIn, []string{"bar"})),
			expectErr: true,
		},
		{
			name:      "invalid DualStack ClusterCIDR, valid IPv6 CIDR in spec.IPv4",
			cc:        test.MakeClusterCIDR(8, "test-clustercidr", "fd00::/120", "fd00:1:1::/64", test.MakeNodeSelector("test-clustercidr", corev1.NodeSelectorOpIn, []string{"bar"})),
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
	oldCCC := test.MakeClusterCIDR(8, "test-clustercidr", "10.1.0.0/16", "fd00:1:1::/64", test.MakeNodeSelector("test-clustercidr", corev1.NodeSelectorOpIn, []string{"bar"}))

	clusterCIDRForUpdate := test.MakeClusterCIDR(8, "test-clustercidr", "10.1.0.0/16", "fd00:1:1::/64", test.MakeNodeSelector("test-clustercidr", corev1.NodeSelectorOpIn, []string{"bar"}))
	clusterCIDRForUpdate.ResourceVersion = "9"
	testCases := []struct {
		name      string
		cc        *v1.ClusterCIDR
		expectErr bool
	}{{
		name:      "Successful update, no changes to ClusterCIDR.Spec",
		cc:        clusterCIDRForUpdate,
		expectErr: false,
	}, {
		name:      "Failed update, update spec.PerNodeHostBits",
		cc:        test.MakeClusterCIDR(12, "test-clustercidr", "10.1.0.0/16", "fd00:1:1::/64", test.MakeNodeSelector("test-clustercidr", corev1.NodeSelectorOpIn, []string{"bar"})),
		expectErr: true,
	}, {
		name:      "Failed update, update spec.IPv4",
		cc:        test.MakeClusterCIDR(8, "test-clustercidr", "10.2.0.0/16", "fd00:1:1::/64", test.MakeNodeSelector("test-clustercidr", corev1.NodeSelectorOpIn, []string{"bar"})),
		expectErr: true,
	}, {
		name:      "Failed update, update spec.IPv6",
		cc:        test.MakeClusterCIDR(8, "test-clustercidr", "10.1.0.0/16", "fd00:2:/112", test.MakeNodeSelector("test-clustercidr", corev1.NodeSelectorOpIn, []string{"bar"})),
		expectErr: true,
	}, {
		name:      "Failed update, update spec.NodeSelector",
		cc:        test.MakeClusterCIDR(8, "test-clustercidr", "10.1.0.0/16", "fd00:1:1::/64", test.MakeNodeSelector("test-clustercidr", corev1.NodeSelectorOpIn, []string{"bar2"})),
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
