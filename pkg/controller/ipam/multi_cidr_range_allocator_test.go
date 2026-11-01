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

package ipam

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "sigs.k8s.io/node-ipam-controller/pkg/apis/clustercidr/v1"
	clustercidrfake "sigs.k8s.io/node-ipam-controller/pkg/client/clientset/versioned/fake"
	clustercidrinformer "sigs.k8s.io/node-ipam-controller/pkg/client/informers/externalversions"
	"sigs.k8s.io/node-ipam-controller/pkg/controller/ipam/multicidrset"
	"sigs.k8s.io/node-ipam-controller/pkg/controller/ipam/test"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2/ktesting"
	utilnet "k8s.io/utils/net"
)

type testCaseMultiCIDR struct {
	description     string
	fakeNodeHandler *test.FakeNodeHandler
	allocatorParams CIDRAllocatorParams
	testCIDRMap     map[string]*clusterCIDREntry
	// key is index of the cidr allocated.
	expectedAllocatedCIDR map[int]string
	allocatedCIDRs        map[int][]string
	// should controller creation fail?
	ctrlCreateFail bool
}

type testClusterCIDR struct {
	perNodeHostBits int32
	ipv4CIDR        string
	ipv6CIDR        string
	name            string
}

type testNodeSelectorRequirement struct {
	key      string
	operator corev1.NodeSelectorOperator
	values   []string
}

// makeTestNodeSelector builds a nodeSelector with a single term, so all the
// requirements are ANDed. Requirements are sorted by key to keep the serialized
// selector stable.
func makeTestNodeSelector(requirements []testNodeSelectorRequirement) *corev1.NodeSelector {
	term := corev1.NodeSelectorTerm{}
	for _, nsr := range requirements {
		term.MatchExpressions = append(term.MatchExpressions, corev1.NodeSelectorRequirement{
			Key:      nsr.key,
			Operator: nsr.operator,
			Values:   nsr.values,
		})
	}
	slices.SortFunc(term.MatchExpressions, func(a, b corev1.NodeSelectorRequirement) int {
		return strings.Compare(a.Key, b.Key)
	})

	return &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{term}}
}

func getTestNodeSelector(requirements []testNodeSelectorRequirement) string {
	return getTestNodeSelectorKey(makeTestNodeSelector(requirements))
}

// getTestNodeSelectorKey serializes a nodeSelector, so getTestCidrMap can
// group test ClusterCIDRs by nodeSelector. A nil nodeSelector serializes to
// "null".
func getTestNodeSelectorKey(nodeSelector *corev1.NodeSelector) string {
	key, err := json.Marshal(nodeSelector)
	if err != nil {
		panic(err)
	}
	return string(key)
}

// getTestCidrMap builds a cidrMap from test ClusterCIDRs grouped by serialized
// nodeSelector.
func getTestCidrMap(testClusterCIDRMap map[string][]*testClusterCIDR) map[string]*clusterCIDREntry {
	cidrMap := make(map[string]*clusterCIDREntry)
	for key, testClusterCIDRList := range testClusterCIDRMap {
		var nodeSelector *corev1.NodeSelector
		if err := json.Unmarshal([]byte(key), &nodeSelector); err != nil {
			panic(err)
		}
		selector, err := newNodeSelectorMatcher(nodeSelector)
		if err != nil {
			panic(err)
		}

		for _, testClusterCIDR := range testClusterCIDRList {
			clusterCIDR := &multicidrset.ClusterCIDR{
				Name:            testClusterCIDR.name,
				AssociatedNodes: make(map[string]bool, 0),
			}

			if testClusterCIDR.ipv4CIDR != "" {
				_, testCIDR, _ := utilnet.ParseCIDRSloppy(testClusterCIDR.ipv4CIDR)
				testCIDRSet, _ := multicidrset.NewMultiCIDRSet(clusterCIDR.Name, testCIDR, int(testClusterCIDR.perNodeHostBits))
				clusterCIDR.IPv4CIDRSet = testCIDRSet
			}
			if testClusterCIDR.ipv6CIDR != "" {
				_, testCIDR, _ := utilnet.ParseCIDRSloppy(testClusterCIDR.ipv6CIDR)
				testCIDRSet, _ := multicidrset.NewMultiCIDRSet(clusterCIDR.Name, testCIDR, int(testClusterCIDR.perNodeHostBits))
				clusterCIDR.IPv6CIDRSet = testCIDRSet
			}
			cidrMap[clusterCIDR.Name] = &clusterCIDREntry{clusterCIDR: clusterCIDR, selector: selector}
		}
	}
	return cidrMap
}

// getClusterCIDRList returns the ClusterCIDRs, ordered by name, whose nodeSelector
// matches a node labeled testLabel-0=nodeName.
func getClusterCIDRList(nodeName string, cidrMap map[string]*clusterCIDREntry) ([]*multicidrset.ClusterCIDR, error) {
	node := testNode(nodeName, map[string]string{"testLabel-0": nodeName})
	var clusterCIDRList []*multicidrset.ClusterCIDR
	for _, name := range slices.Sorted(maps.Keys(cidrMap)) {
		entry := cidrMap[name]
		if entry.selector == nil {
			continue
		}
		if matches, _ := entry.selector.match(node); matches {
			clusterCIDRList = append(clusterCIDRList, entry.clusterCIDR)
		}
	}
	if len(clusterCIDRList) == 0 {
		return nil, fmt.Errorf("unable to get clusterCIDR for node: %s", nodeName)
	}
	return clusterCIDRList, nil
}

func TestMultiCIDROccupyPreExistingCIDR(t *testing.T) {
	// all tests operate on a single node.
	testCaseMultiCIDRs := []testCaseMultiCIDR{
		{
			description: "success, single stack no node allocation",
			fakeNodeHandler: &test.FakeNodeHandler{
				Existing: []*corev1.Node{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name: "node0",
							Labels: map[string]string{
								"testLabel-0": "node0",
							},
						},
					},
				},
				Clientset: fake.NewClientset(),
			},
			allocatorParams: CIDRAllocatorParams{
				ServiceCIDR:          nil,
				SecondaryServiceCIDR: nil,
			},
			testCIDRMap: getTestCidrMap(
				map[string][]*testClusterCIDR{
					getTestNodeSelector([]testNodeSelectorRequirement{
						{
							key:      "testLabel-0",
							operator: corev1.NodeSelectorOpIn,
							values:   []string{"node0"},
						},
					}): {
						{
							name:            "single-stack-cidr",
							perNodeHostBits: 8,
							ipv4CIDR:        "10.10.0.0/16",
						},
					},
				}),
			allocatedCIDRs:        nil,
			expectedAllocatedCIDR: nil,
			ctrlCreateFail:        false,
		},
		{
			description: "success, dual stack no node allocation",
			fakeNodeHandler: &test.FakeNodeHandler{
				Existing: []*corev1.Node{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name: "node0",
							Labels: map[string]string{
								"testLabel-0": "node0",
							},
						},
					},
				},
				Clientset: fake.NewClientset(),
			},
			allocatorParams: CIDRAllocatorParams{
				ServiceCIDR:          nil,
				SecondaryServiceCIDR: nil,
			},
			testCIDRMap: getTestCidrMap(
				map[string][]*testClusterCIDR{
					getTestNodeSelector([]testNodeSelectorRequirement{
						{
							key:      "testLabel-0",
							operator: corev1.NodeSelectorOpIn,
							values:   []string{"node0"},
						},
					}): {
						{
							name:            "dual-stack-cidr",
							perNodeHostBits: 8,
							ipv4CIDR:        "10.10.0.0/16",
							ipv6CIDR:        "ace:cab:deca::/112",
						},
					},
				}),
			allocatedCIDRs:        nil,
			expectedAllocatedCIDR: nil,
			ctrlCreateFail:        false,
		},
		{
			description: "success, single stack correct node allocation",
			fakeNodeHandler: &test.FakeNodeHandler{
				Existing: []*corev1.Node{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name: "node0",
							Labels: map[string]string{
								"testLabel-0": "node0",
							},
						},
						Spec: corev1.NodeSpec{
							PodCIDRs: []string{"10.10.0.1/24"},
						},
					},
				},
				Clientset: fake.NewClientset(),
			},
			allocatorParams: CIDRAllocatorParams{
				ServiceCIDR:          nil,
				SecondaryServiceCIDR: nil,
			},
			testCIDRMap: getTestCidrMap(
				map[string][]*testClusterCIDR{
					getTestNodeSelector([]testNodeSelectorRequirement{
						{
							key:      "testLabel-0",
							operator: corev1.NodeSelectorOpIn,
							values:   []string{"node0"},
						},
					}): {
						{
							name:            "single-stack-cidr-allocated",
							perNodeHostBits: 8,
							ipv4CIDR:        "10.10.0.0/16",
						},
					},
				}),
			allocatedCIDRs:        nil,
			expectedAllocatedCIDR: nil,
			ctrlCreateFail:        false,
		},
		{
			description: "success, dual stack both allocated correctly",
			fakeNodeHandler: &test.FakeNodeHandler{
				Existing: []*corev1.Node{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name: "node0",
							Labels: map[string]string{
								"testLabel-0": "node0",
							},
						},
						Spec: corev1.NodeSpec{
							PodCIDRs: []string{"10.10.0.1/24", "ace:cab:deca::1/120"},
						},
					},
				},
				Clientset: fake.NewClientset(),
			},
			allocatorParams: CIDRAllocatorParams{
				ServiceCIDR:          nil,
				SecondaryServiceCIDR: nil,
			},
			testCIDRMap: getTestCidrMap(
				map[string][]*testClusterCIDR{
					getTestNodeSelector([]testNodeSelectorRequirement{
						{
							key:      "testLabel-0",
							operator: corev1.NodeSelectorOpIn,
							values:   []string{"node0"},
						},
					}): {
						{
							name:            "dual-stack-cidr-allocated",
							perNodeHostBits: 8,
							ipv4CIDR:        "10.10.0.0/16",
							ipv6CIDR:        "ace:cab:deca::/112",
						},
					},
				}),
			allocatedCIDRs:        nil,
			expectedAllocatedCIDR: nil,
			ctrlCreateFail:        false,
		},
	}

	// test function
	_, ctx := ktesting.NewTestContext(t)
	for _, tc := range testCaseMultiCIDRs {
		t.Run(tc.description, func(t *testing.T) {
			// Initialize the range allocator.
			fakeNodeInformer := test.FakeNodeInformer(tc.fakeNodeHandler)
			fakeClient := &clustercidrfake.Clientset{}
			fakeInformerFactory := clustercidrinformer.NewSharedInformerFactory(fakeClient, NoResyncPeriodFunc())
			fakeClusterCIDRInformer := fakeInformerFactory.Networking().V1().ClusterCIDRs()
			nodeList, _ := tc.fakeNodeHandler.List(context.TODO(), metav1.ListOptions{})
			fakeCIDRClient := clustercidrfake.NewSimpleClientset().NetworkingV1().ClusterCIDRs()
			_, err := NewMultiCIDRRangeAllocator(ctx, tc.fakeNodeHandler, fakeCIDRClient, fakeNodeInformer, fakeClusterCIDRInformer, tc.allocatorParams, nodeList, tc.testCIDRMap)
			if err == nil && tc.ctrlCreateFail {
				t.Fatalf("creating range allocator was expected to fail, but it did not")
			}
			if err != nil && !tc.ctrlCreateFail {
				t.Fatalf("creating range allocator was expected to succeed, but it did not")
			}
		})
	}
}

func TestMultiCIDROccupyWithClusterCIDRMissingAnIPFamily(t *testing.T) {
	catchAllSelector := getTestNodeSelectorKey(nil)
	nodeName := "testNode"
	labelKey := "testLabel"
	testNodeSelector := getTestNodeSelector([]testNodeSelectorRequirement{
		{
			key:      labelKey,
			operator: corev1.NodeSelectorOpIn,
			values:   []string{nodeName},
		},
	})

	testCases := []struct {
		description string
		podCIDRs    []string
		catchAll    *testClusterCIDR
		selecting   *testClusterCIDR
	}{
		{
			description: "original single stack node, new IPv6 only ClusterCIDR",
			podCIDRs:    []string{"10.10.0.0/24"},
			catchAll:    &testClusterCIDR{name: "catch-all", perNodeHostBits: 8, ipv4CIDR: "10.10.0.0/16"},
			selecting:   &testClusterCIDR{name: "ipv6-only", perNodeHostBits: 8, ipv6CIDR: "fd00:10::/112"},
		},
		{
			description: "original dual stack node, new IPv6 only ClusterCIDR",
			podCIDRs:    []string{"10.10.0.0/24", "ace:cab:deca::/120"},
			catchAll: &testClusterCIDR{
				name: "catch-all", perNodeHostBits: 8, ipv4CIDR: "10.10.0.0/16", ipv6CIDR: "ace:cab:deca::/112",
			},
			selecting: &testClusterCIDR{name: "ipv6-only", perNodeHostBits: 8, ipv6CIDR: "fd00:10::/112"},
		},
		{
			description: "original dual stack node, new IPv4 only ClusterCIDR containing the IPv4 Pod CIDR",
			podCIDRs:    []string{"10.10.0.0/24", "ace:cab:deca::/120"},
			catchAll: &testClusterCIDR{
				name: "catch-all", perNodeHostBits: 8, ipv4CIDR: "10.10.0.0/16", ipv6CIDR: "ace:cab:deca::/112",
			},
			selecting: &testClusterCIDR{name: "ipv4-only", perNodeHostBits: 8, ipv4CIDR: "10.0.0.0/8"},
		},
		{
			description: "original dual stack node, new IPv4 only ClusterCIDR not containing the IPv4 Pod CIDR",
			podCIDRs:    []string{"10.10.0.0/24", "ace:cab:deca::/120"},
			catchAll: &testClusterCIDR{
				name: "catch-all", perNodeHostBits: 8, ipv4CIDR: "10.10.0.0/16", ipv6CIDR: "ace:cab:deca::/112",
			},
			selecting: &testClusterCIDR{name: "ipv4-only", perNodeHostBits: 8, ipv4CIDR: "10.20.0.0/16"},
		},
	}

	for _, tc := range testCases {
		node := &corev1.Node{
			Name: nodeName,
			Labels: map[string]string{
				labelKey: nodeName,
			},
			Spec: corev1.NodeSpec{
				PodCIDRs: tc.podCIDRs,
			},
		}
		newCIDRMap := func() map[string]*clusterCIDREntry {
			return getTestCidrMap(map[string][]*testClusterCIDR{
				catchAllSelector: {tc.catchAll},
				testNodeSelector: {tc.selecting},
			})
		}
		newAllocator := func(
			ctx context.Context, nodeList *corev1.NodeList, cidrMap map[string]*clusterCIDREntry,
		) (CIDRAllocator, error) {
			fakeNodeHandler := &test.FakeNodeHandler{
				Existing:  []*corev1.Node{node},
				Clientset: fake.NewClientset(),
			}
			fakeNodeInformer := test.FakeNodeInformer(fakeNodeHandler)
			fakeInformerFactory := clustercidrinformer.NewSharedInformerFactory(
				&clustercidrfake.Clientset{}, NoResyncPeriodFunc(),
			)
			fakeClusterCIDRInformer := fakeInformerFactory.Networking().V1().ClusterCIDRs()
			fakeCIDRClient := clustercidrfake.NewSimpleClientset().NetworkingV1().ClusterCIDRs()
			return NewMultiCIDRRangeAllocator(
				ctx, fakeNodeHandler, fakeCIDRClient, fakeNodeInformer,
				fakeClusterCIDRInformer, CIDRAllocatorParams{}, nodeList, cidrMap,
			)
		}

		t.Run(tc.description+", at startup", func(t *testing.T) {
			_, ctx := ktesting.NewTestContext(t)
			cidrMap := newCIDRMap()

			assert.NotPanics(t, func() {
				_, err := newAllocator(ctx, &corev1.NodeList{Items: []corev1.Node{*node}}, cidrMap)
				assert.NoError(t, err)
			})
			assert.True(t, cidrMap[tc.catchAll.name].clusterCIDR.AssociatedNodes[node.Name],
				"node is not associated with the catch all ClusterCIDR")
		})

		t.Run(tc.description+", on node sync", func(t *testing.T) {
			logger, ctx := ktesting.NewTestContext(t)
			cidrMap := newCIDRMap()
			allocator, err := newAllocator(ctx, &corev1.NodeList{}, cidrMap)
			require.NoError(t, err)

			assert.NotPanics(t, func() {
				assert.NoError(t, allocator.AllocateOrOccupyCIDR(logger, node))
			})
			assert.True(t, cidrMap[tc.catchAll.name].clusterCIDR.AssociatedNodes[node.Name],
				"node is not associated with the catch all ClusterCIDR")
		})
	}
}

// todo(mneverov): copied from cidr_allocator

// nodePollInterval is used in listing node
// This is a variable instead of a const to enable testing.
var nodePollInterval = 10 * time.Second

func TestMultiCIDRAllocateOrOccupyCIDRSuccess(t *testing.T) {
	// Non-parallel test (overrides global var).
	oldNodePollInterval := nodePollInterval
	nodePollInterval = test.NodePollInterval
	defer func() {
		nodePollInterval = oldNodePollInterval
	}()

	// all tests operate on a single node.
	testCaseMultiCIDRs := []testCaseMultiCIDR{
		{
			description: "When there's no ServiceCIDR return first CIDR in range",
			fakeNodeHandler: &test.FakeNodeHandler{
				Existing: []*corev1.Node{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name: "node0",
							Labels: map[string]string{
								"testLabel-0": "node0",
							},
						},
					},
				},
				Clientset: fake.NewClientset(),
			},
			allocatorParams: CIDRAllocatorParams{
				ServiceCIDR:          nil,
				SecondaryServiceCIDR: nil,
			},
			testCIDRMap: getTestCidrMap(
				map[string][]*testClusterCIDR{
					getTestNodeSelector([]testNodeSelectorRequirement{
						{
							key:      "testLabel-0",
							operator: corev1.NodeSelectorOpIn,
							values:   []string{"node0"},
						},
					}): {
						{
							name:            "single-stack-cidr",
							perNodeHostBits: 2,
							ipv4CIDR:        "127.123.234.0/24",
						},
					},
				}),
			expectedAllocatedCIDR: map[int]string{
				0: "127.123.234.0/30",
			},
		},
		{
			description: "Correctly filter out ServiceCIDR",
			fakeNodeHandler: &test.FakeNodeHandler{
				Existing: []*corev1.Node{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name: "node0",
							Labels: map[string]string{
								"testLabel-0": "node0",
							},
						},
					},
				},
				Clientset: fake.NewClientset(),
			},
			allocatorParams: CIDRAllocatorParams{
				ServiceCIDR: func() *net.IPNet {
					_, serviceCIDR, _ := utilnet.ParseCIDRSloppy("127.123.234.0/26")
					return serviceCIDR
				}(),
				SecondaryServiceCIDR: nil,
				NodeCIDRMaskSizes:    []int{30},
			},
			testCIDRMap: getTestCidrMap(
				map[string][]*testClusterCIDR{
					getTestNodeSelector([]testNodeSelectorRequirement{
						{
							key:      "testLabel-0",
							operator: corev1.NodeSelectorOpIn,
							values:   []string{"node0"},
						},
					}): {
						{
							name:            "single-stack-cidr",
							perNodeHostBits: 2,
							ipv4CIDR:        "127.123.234.0/24",
						},
					},
				}),
			// it should return first /30 CIDR after service range.
			expectedAllocatedCIDR: map[int]string{
				0: "127.123.234.64/30",
			},
		},
		{
			description: "Correctly ignore already allocated CIDRs",
			fakeNodeHandler: &test.FakeNodeHandler{
				Existing: []*corev1.Node{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name: "node0",
							Labels: map[string]string{
								"testLabel-0": "node0",
							},
						},
					},
				},
				Clientset: fake.NewClientset(),
			},
			allocatorParams: CIDRAllocatorParams{
				ServiceCIDR: func() *net.IPNet {
					_, serviceCIDR, _ := utilnet.ParseCIDRSloppy("127.123.234.0/26")
					return serviceCIDR
				}(),
				SecondaryServiceCIDR: nil,
			},
			testCIDRMap: getTestCidrMap(
				map[string][]*testClusterCIDR{
					getTestNodeSelector([]testNodeSelectorRequirement{
						{
							key:      "testLabel-0",
							operator: corev1.NodeSelectorOpIn,
							values:   []string{"node0"},
						},
					}): {
						{
							name:            "single-stack-cidr",
							perNodeHostBits: 2,
							ipv4CIDR:        "127.123.234.0/24",
						},
					},
				}),
			allocatedCIDRs: map[int][]string{
				0: {"127.123.234.64/30", "127.123.234.68/30", "127.123.234.72/30", "127.123.234.80/30"},
			},
			expectedAllocatedCIDR: map[int]string{
				0: "127.123.234.76/30",
			},
		},
		{
			description: "Dualstack CIDRs, prioritize clusterCIDR with higher label match count",
			fakeNodeHandler: &test.FakeNodeHandler{
				Existing: []*corev1.Node{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name: "node0",
							Labels: map[string]string{
								"testLabel-0": "node0",
								"testLabel-1": "label1",
								"testLabel-2": "label2",
							},
						},
					},
				},
				Clientset: fake.NewClientset(),
			},
			allocatorParams: CIDRAllocatorParams{
				ServiceCIDR: func() *net.IPNet {
					_, serviceCIDR, _ := utilnet.ParseCIDRSloppy("127.123.234.0/26")
					return serviceCIDR
				}(),
				SecondaryServiceCIDR: nil,
			},
			testCIDRMap: getTestCidrMap(
				map[string][]*testClusterCIDR{
					getTestNodeSelector([]testNodeSelectorRequirement{
						{
							key:      "testLabel-0",
							operator: corev1.NodeSelectorOpIn,
							values:   []string{"node0"},
						},
					}): {
						{
							name:            "dual-stack-cidr-1",
							perNodeHostBits: 8,
							ipv4CIDR:        "10.0.0.0/8",
							ipv6CIDR:        "ace:cab:deca::/112",
						},
					},
					getTestNodeSelector([]testNodeSelectorRequirement{
						{
							key:      "testLabel-0",
							operator: corev1.NodeSelectorOpIn,
							values:   []string{"node0"},
						},
						{
							key:      "testLabel-1",
							operator: corev1.NodeSelectorOpIn,
							values:   []string{"label1"},
						},
					}): {
						{
							name:            "dual-stack-cidr-2",
							perNodeHostBits: 8,
							ipv4CIDR:        "127.123.234.0/8",
							ipv6CIDR:        "abc:def:deca::/112",
						},
					},
				}),
			expectedAllocatedCIDR: map[int]string{
				0: "127.0.0.0/24",
				1: "abc:def:deca::/120",
			},
		},
		{
			description: "Dualstack CIDRs, prioritize clusterCIDR with higher label match count, overlapping CIDRs",
			fakeNodeHandler: &test.FakeNodeHandler{
				Existing: []*corev1.Node{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name: "node0",
							Labels: map[string]string{
								"testLabel-0": "node0",
								"testLabel-1": "label1",
								"testLabel-2": "label2",
							},
						},
					},
				},
				Clientset: fake.NewClientset(),
			},
			allocatorParams: CIDRAllocatorParams{
				ServiceCIDR: func() *net.IPNet {
					_, serviceCIDR, _ := utilnet.ParseCIDRSloppy("127.123.234.0/26")
					return serviceCIDR
				}(),
				SecondaryServiceCIDR: nil,
			},
			testCIDRMap: getTestCidrMap(
				map[string][]*testClusterCIDR{
					getTestNodeSelector([]testNodeSelectorRequirement{
						{
							key:      "testLabel-0",
							operator: corev1.NodeSelectorOpIn,
							values:   []string{"node0"},
						},
					}): {
						{
							name:            "dual-stack-cidr-1",
							perNodeHostBits: 8,
							ipv4CIDR:        "10.0.0.0/8",
							ipv6CIDR:        "ace:cab:deca::/112",
						},
					},
					getTestNodeSelector([]testNodeSelectorRequirement{
						{
							key:      "testLabel-0",
							operator: corev1.NodeSelectorOpIn,
							values:   []string{"node0"},
						},
						{
							key:      "testLabel-1",
							operator: corev1.NodeSelectorOpIn,
							values:   []string{"label1"},
						},
					}): {
						{
							name:            "dual-stack-cidr-2",
							perNodeHostBits: 8,
							ipv4CIDR:        "10.0.0.0/16",
							ipv6CIDR:        "ace:cab:deca::/112",
						},
					},
				}),
			allocatedCIDRs: map[int][]string{
				0: {"10.0.0.0/24", "10.0.1.0/24", "10.0.2.0/24", "10.0.4.0/24"},
				1: {"ace:cab:deca::/120"},
			},
			expectedAllocatedCIDR: map[int]string{
				0: "10.0.3.0/24",
				1: "ace:cab:deca::100/120",
			},
		},
		{
			description: "Dualstack CIDRs, clusterCIDR with equal label match count, prioritize clusterCIDR with fewer allocatable pod CIDRs",
			fakeNodeHandler: &test.FakeNodeHandler{
				Existing: []*corev1.Node{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name: "node0",
							Labels: map[string]string{
								"testLabel-0": "node0",
								"testLabel-1": "label1",
								"testLabel-2": "label2",
							},
						},
					},
				},
				Clientset: fake.NewClientset(),
			},
			allocatorParams: CIDRAllocatorParams{
				ServiceCIDR: func() *net.IPNet {
					_, serviceCIDR, _ := utilnet.ParseCIDRSloppy("127.123.234.0/26")
					return serviceCIDR
				}(),
				SecondaryServiceCIDR: nil,
			},
			testCIDRMap: getTestCidrMap(
				map[string][]*testClusterCIDR{
					getTestNodeSelector([]testNodeSelectorRequirement{
						{
							key:      "testLabel-0",
							operator: corev1.NodeSelectorOpIn,
							values:   []string{"node0"},
						},
						{
							key:      "testLabel-1",
							operator: corev1.NodeSelectorOpIn,
							values:   []string{"label1"},
						},
					}): {
						{
							name:            "dual-stack-cidr-1",
							perNodeHostBits: 8,
							ipv4CIDR:        "127.123.234.0/8",
							ipv6CIDR:        "abc:def:deca::/112",
						},
					},
					getTestNodeSelector([]testNodeSelectorRequirement{
						{
							key:      "testLabel-0",
							operator: corev1.NodeSelectorOpIn,
							values:   []string{"node0"},
						},
						{
							key:      "testLabel-2",
							operator: corev1.NodeSelectorOpIn,
							values:   []string{"label2"},
						},
					}): {
						{
							name:            "dual-stack-cidr-2",
							perNodeHostBits: 8,
							ipv4CIDR:        "10.0.0.0/24",
							ipv6CIDR:        "ace:cab:deca::/120",
						},
					},
				}),
			expectedAllocatedCIDR: map[int]string{
				0: "10.0.0.0/24",
				1: "ace:cab:deca::/120",
			},
		},
		{
			description: "Dualstack CIDRs, clusterCIDR with equal label count, non comparable allocatable pod CIDRs, prioritize clusterCIDR with lower perNodeMaskSize",
			fakeNodeHandler: &test.FakeNodeHandler{
				Existing: []*corev1.Node{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name: "node0",
							Labels: map[string]string{
								"testLabel-0": "node0",
								"testLabel-1": "label1",
								"testLabel-2": "label2",
							},
						},
					},
				},
				Clientset: fake.NewClientset(),
			},
			allocatorParams: CIDRAllocatorParams{
				ServiceCIDR: func() *net.IPNet {
					_, serviceCIDR, _ := utilnet.ParseCIDRSloppy("127.123.234.0/26")
					return serviceCIDR
				}(),
				SecondaryServiceCIDR: nil,
			},
			testCIDRMap: getTestCidrMap(
				map[string][]*testClusterCIDR{
					getTestNodeSelector([]testNodeSelectorRequirement{
						{
							key:      "testLabel-0",
							operator: corev1.NodeSelectorOpIn,
							values:   []string{"node0"},
						},
						{
							key:      "testLabel-1",
							operator: corev1.NodeSelectorOpIn,
							values:   []string{"label1"},
						},
					}): {
						{
							name:            "dual-stack-cidr-1",
							perNodeHostBits: 8,
							ipv4CIDR:        "127.123.234.0/23",
						},
					},
					getTestNodeSelector([]testNodeSelectorRequirement{
						{
							key:      "testLabel-0",
							operator: corev1.NodeSelectorOpIn,
							values:   []string{"node0"},
						},
						{
							key:      "testLabel-2",
							operator: corev1.NodeSelectorOpIn,
							values:   []string{"label2"},
						},
					}): {
						{
							name:            "dual-stack-cidr-2",
							perNodeHostBits: 8,
							ipv4CIDR:        "10.0.0.0/16",
							ipv6CIDR:        "ace:cab:deca::/120",
						},
					},
				}),
			expectedAllocatedCIDR: map[int]string{
				0: "10.0.0.0/24",
				1: "ace:cab:deca::/120",
			},
		},
		{
			description: "Dualstack CIDRs, clusterCIDR with equal label count and allocatable pod CIDRs, prioritize clusterCIDR with lower perNodeMaskSize",
			fakeNodeHandler: &test.FakeNodeHandler{
				Existing: []*corev1.Node{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name: "node0",
							Labels: map[string]string{
								"testLabel-0": "node0",
								"testLabel-1": "label1",
								"testLabel-2": "label2",
							},
						},
					},
				},
				Clientset: fake.NewClientset(),
			},
			allocatorParams: CIDRAllocatorParams{
				ServiceCIDR: func() *net.IPNet {
					_, serviceCIDR, _ := utilnet.ParseCIDRSloppy("127.123.234.0/26")
					return serviceCIDR
				}(),
				SecondaryServiceCIDR: nil,
			},
			testCIDRMap: getTestCidrMap(
				map[string][]*testClusterCIDR{
					getTestNodeSelector([]testNodeSelectorRequirement{
						{
							key:      "testLabel-0",
							operator: corev1.NodeSelectorOpIn,
							values:   []string{"node0"},
						},
						{
							key:      "testLabel-1",
							operator: corev1.NodeSelectorOpIn,
							values:   []string{"label1"},
						},
					}): {
						{
							name:            "dual-stack-cidr-1",
							perNodeHostBits: 8,
							ipv4CIDR:        "127.123.234.0/24",
							ipv6CIDR:        "abc:def:deca::/120",
						},
					},
					getTestNodeSelector([]testNodeSelectorRequirement{
						{
							key:      "testLabel-0",
							operator: corev1.NodeSelectorOpIn,
							values:   []string{"node0"},
						},
						{
							key:      "testLabel-2",
							operator: corev1.NodeSelectorOpIn,
							values:   []string{"label2"},
						},
					}): {
						{
							name:            "dual-stack-cidr-2",
							perNodeHostBits: 0,
							ipv4CIDR:        "10.0.0.0/32",
							ipv6CIDR:        "ace:cab:deca::/128",
						},
					},
				}),
			expectedAllocatedCIDR: map[int]string{
				0: "10.0.0.0/32",
				1: "ace:cab:deca::/128",
			},
		},
		{
			description: "Dualstack CIDRs, clusterCIDR with equal label count, allocatable pod CIDRs and allocatable IPs, prioritize clusterCIDR with smaller IP over lower alphanumeric label",
			fakeNodeHandler: &test.FakeNodeHandler{
				Existing: []*corev1.Node{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name: "node0",
							Labels: map[string]string{
								"testLabel-0": "node0",
								"testLabel-1": "label1",
								"testLabel-2": "label2",
							},
						},
					},
				},
				Clientset: fake.NewClientset(),
			},
			allocatorParams: CIDRAllocatorParams{
				ServiceCIDR: func() *net.IPNet {
					_, serviceCIDR, _ := utilnet.ParseCIDRSloppy("127.123.234.0/26")
					return serviceCIDR
				}(),
				SecondaryServiceCIDR: nil,
			},
			testCIDRMap: getTestCidrMap(
				map[string][]*testClusterCIDR{
					getTestNodeSelector([]testNodeSelectorRequirement{
						{
							key:      "testLabel-0",
							operator: corev1.NodeSelectorOpIn,
							values:   []string{"node0"},
						},
						{
							key:      "testLabel-1",
							operator: corev1.NodeSelectorOpIn,
							values:   []string{"label1"},
						},
					}): {
						{
							name:            "dual-stack-cidr-1",
							perNodeHostBits: 8,
							ipv4CIDR:        "127.123.234.0/16",
							ipv6CIDR:        "abc:def:deca::/112",
						},
					},
					getTestNodeSelector([]testNodeSelectorRequirement{
						{
							key:      "testLabel-0",
							operator: corev1.NodeSelectorOpIn,
							values:   []string{"node0"},
						},
						{
							key:      "testLabel-2",
							operator: corev1.NodeSelectorOpIn,
							values:   []string{"label2"},
						},
					}): {
						{
							name:            "dual-stack-cidr-2",
							perNodeHostBits: 8,
							ipv4CIDR:        "10.0.0.0/16",
							ipv6CIDR:        "ace:cab:deca::/112",
						},
					},
				}),
			expectedAllocatedCIDR: map[int]string{
				0: "10.0.0.0/24",
				1: "ace:cab:deca::/120",
			},
		},
		{
			description: "Dualstack CIDRs, clusterCIDR with equal label count, allocatable pod CIDRs, allocatable IPs and labels, prioritize clusterCIDR with smaller IP",
			fakeNodeHandler: &test.FakeNodeHandler{
				Existing: []*corev1.Node{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name: "node0",
							Labels: map[string]string{
								"testLabel-0": "node0",
								"testLabel-1": "label1",
								"testLabel-2": "label2",
							},
						},
					},
				},
				Clientset: fake.NewClientset(),
			},
			allocatorParams: CIDRAllocatorParams{
				ServiceCIDR: func() *net.IPNet {
					_, serviceCIDR, _ := utilnet.ParseCIDRSloppy("127.123.234.0/26")
					return serviceCIDR
				}(),
				SecondaryServiceCIDR: nil,
			},
			testCIDRMap: getTestCidrMap(
				map[string][]*testClusterCIDR{
					getTestNodeSelector([]testNodeSelectorRequirement{
						{
							key:      "testLabel-0",
							operator: corev1.NodeSelectorOpIn,
							values:   []string{"node0"},
						},
						{
							key:      "testLabel-1",
							operator: corev1.NodeSelectorOpIn,
							values:   []string{"label1"},
						},
					}): {
						{
							name:            "dual-stack-cidr-1",
							perNodeHostBits: 8,
							ipv4CIDR:        "127.123.234.0/16",
							ipv6CIDR:        "abc:def:deca::/112",
						},
					},
					getTestNodeSelector([]testNodeSelectorRequirement{
						{
							key:      "testLabel-0",
							operator: corev1.NodeSelectorOpIn,
							values:   []string{"node0"},
						},
						{
							key:      "testLabel-1",
							operator: corev1.NodeSelectorOpIn,
							values:   []string{"label1"},
						},
					}): {
						{
							name:            "dual-stack-cidr-2",
							perNodeHostBits: 8,
							ipv4CIDR:        "10.0.0.0/16",
							ipv6CIDR:        "ace:cab:deca::/112",
						},
					},
				}),
			expectedAllocatedCIDR: map[int]string{
				0: "10.0.0.0/24",
				1: "ace:cab:deca::/120",
			},
		},
		{
			description: "no double counting",
			fakeNodeHandler: &test.FakeNodeHandler{
				Existing: []*corev1.Node{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name: "node0",
							Labels: map[string]string{
								"testLabel-0": "nodepool1",
							},
						},
						Spec: corev1.NodeSpec{
							PodCIDRs: []string{"10.10.0.0/24"},
						},
					},
					{
						ObjectMeta: metav1.ObjectMeta{
							Name: "node1",
							Labels: map[string]string{
								"testLabel-0": "nodepool1",
							},
						},
						Spec: corev1.NodeSpec{
							PodCIDRs: []string{"10.10.2.0/24"},
						},
					},
					{
						ObjectMeta: metav1.ObjectMeta{
							Name: "node2",
							Labels: map[string]string{
								"testLabel-0": "nodepool1",
							},
						},
					},
				},
				Clientset: fake.NewClientset(),
			},
			allocatorParams: CIDRAllocatorParams{
				ServiceCIDR:          nil,
				SecondaryServiceCIDR: nil,
			},
			testCIDRMap: getTestCidrMap(
				map[string][]*testClusterCIDR{
					getTestNodeSelector([]testNodeSelectorRequirement{
						{
							key:      "testLabel-0",
							operator: corev1.NodeSelectorOpIn,
							values:   []string{"nodepool1"},
						},
					}): {
						{
							name:            "no-double-counting",
							perNodeHostBits: 8,
							ipv4CIDR:        "10.10.0.0/22",
						},
					},
				}),
			expectedAllocatedCIDR: map[int]string{
				0: "10.10.1.0/24",
			},
		},
	}

	logger, ctx := ktesting.NewTestContext(t)

	// test function
	testFunc := func(tc testCaseMultiCIDR) {
		nodeList, _ := tc.fakeNodeHandler.List(context.TODO(), metav1.ListOptions{})
		// Initialize the range allocator.

		fakeClient := &clustercidrfake.Clientset{}
		fakeInformerFactory := clustercidrinformer.NewSharedInformerFactory(fakeClient, NoResyncPeriodFunc())
		fakeClusterCIDRInformer := fakeInformerFactory.Networking().V1().ClusterCIDRs()
		fakeCIDRClient := clustercidrfake.NewSimpleClientset().NetworkingV1().ClusterCIDRs()
		allocator, err := NewMultiCIDRRangeAllocator(ctx, tc.fakeNodeHandler, fakeCIDRClient, test.FakeNodeInformer(tc.fakeNodeHandler), fakeClusterCIDRInformer, tc.allocatorParams, nodeList, tc.testCIDRMap)
		if err != nil {
			t.Errorf("%v: failed to create CIDRRangeAllocator with error %v", tc.description, err)
			return
		}
		rangeAllocator, ok := allocator.(*multiCIDRRangeAllocator)
		if !ok {
			t.Logf("%v: found non-default implementation of CIDRAllocator, skipping white-box test...", tc.description)
			return
		}
		rangeAllocator.nodesSynced = test.AlwaysReady
		// todo(mneverov)
		// rangeAllocator.recorder = test.NewFakeRecorder()
		rangeAllocator.recorder = &record.FakeRecorder{}

		// this is a bit of white box testing
		// pre allocate the CIDRs as per the test
		for _, allocatedList := range tc.allocatedCIDRs {
			for _, allocated := range allocatedList {
				_, cidr, err := utilnet.ParseCIDRSloppy(allocated)
				if err != nil {
					t.Fatalf("%v: unexpected error when parsing CIDR %v: %v", tc.description, allocated, err)
				}

				rangeAllocator.lock.Lock()
				clusterCIDRList, err := getClusterCIDRList("node0", rangeAllocator.cidrMap)
				rangeAllocator.lock.Unlock()
				if err != nil {
					t.Fatalf("%v: unexpected error when getting associated clusterCIDR for node %v %v", tc.description, "node0", err)
				}

				occupied := false
				for _, clusterCIDR := range clusterCIDRList {
					if err := rangeAllocator.Occupy(clusterCIDR, cidr); err == nil {
						occupied = true
						break
					}
				}
				if !occupied {
					t.Fatalf("%v: unable to occupy CIDR %v", tc.description, allocated)
				}
			}
		}

		updateCount := 0
		for _, node := range tc.fakeNodeHandler.Existing {
			if node.Spec.PodCIDRs == nil {
				updateCount++
			}
			if err := allocator.AllocateOrOccupyCIDR(logger, node); err != nil {
				t.Errorf("%v: unexpected error in AllocateOrOccupyCIDR: %v", tc.description, err)
			}
		}
		if updateCount != 1 {
			t.Fatalf("test error: all tests must update exactly one node")
		}
		if err := test.WaitForUpdatedNodeWithTimeout(tc.fakeNodeHandler, updateCount, wait.ForeverTestTimeout); err != nil {
			t.Fatalf("%v: timeout while waiting for Node update: %v", tc.description, err)
		}

		if len(tc.expectedAllocatedCIDR) == 0 {
			// nothing further expected
			return
		}
		for _, updatedNode := range tc.fakeNodeHandler.GetUpdatedNodesCopy() {
			if len(updatedNode.Spec.PodCIDRs) == 0 {
				continue // not assigned yet
			}
			// match
			for podCIDRIdx, expectedPodCIDR := range tc.expectedAllocatedCIDR {
				if updatedNode.Spec.PodCIDRs[podCIDRIdx] != expectedPodCIDR {
					t.Errorf("%v: Unable to find allocated CIDR %v, found updated Nodes with CIDRs: %v", tc.description, expectedPodCIDR, updatedNode.Spec.PodCIDRs)
					break
				}
			}
		}
	}

	// run the test cases
	for _, tc := range testCaseMultiCIDRs {
		testFunc(tc)
	}
}

func TestMultiCIDRAllocateOrOccupyCIDRFailure(t *testing.T) {
	testCaseMultiCIDRs := []testCaseMultiCIDR{
		{
			description: "When there's no ServiceCIDR return first CIDR in range",
			fakeNodeHandler: &test.FakeNodeHandler{
				Existing: []*corev1.Node{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name: "node0",
							Labels: map[string]string{
								"testLabel-0": "node0",
							},
						},
					},
				},
				Clientset: fake.NewClientset(),
			},
			allocatorParams: CIDRAllocatorParams{
				ServiceCIDR:          nil,
				SecondaryServiceCIDR: nil,
			},
			testCIDRMap: getTestCidrMap(
				map[string][]*testClusterCIDR{
					getTestNodeSelector([]testNodeSelectorRequirement{
						{
							key:      "testLabel-0",
							operator: corev1.NodeSelectorOpIn,
							values:   []string{"node0"},
						},
					}): {
						{
							name:            "allocate-fail",
							perNodeHostBits: 2,
							ipv4CIDR:        "127.123.234.0/28",
						},
					},
				}),
			allocatedCIDRs: map[int][]string{
				0: {"127.123.234.0/30", "127.123.234.4/30", "127.123.234.8/30", "127.123.234.12/30"},
			},
		},
		{
			description: "fail, single stack incorrect node allocation",
			fakeNodeHandler: &test.FakeNodeHandler{
				Existing: []*corev1.Node{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name: "node0",
							Labels: map[string]string{
								"testLabel-0": "node0",
							},
						},
						Spec: corev1.NodeSpec{
							PodCIDRs: []string{"172.10.0.1/24"},
						},
					},
				},
				Clientset: fake.NewClientset(),
			},
			allocatorParams: CIDRAllocatorParams{
				ServiceCIDR:          nil,
				SecondaryServiceCIDR: nil,
			},
			testCIDRMap: getTestCidrMap(
				map[string][]*testClusterCIDR{
					getTestNodeSelector([]testNodeSelectorRequirement{
						{
							key:      "testLabel-0",
							operator: corev1.NodeSelectorOpIn,
							values:   []string{"node0"},
						},
					}): {
						{
							name:            "single-stack-cidr-allocate-fail",
							perNodeHostBits: 8,
							ipv4CIDR:        "10.10.0.0/16",
						},
					},
				}),
			allocatedCIDRs:        nil,
			expectedAllocatedCIDR: nil,
		},
		{
			description: "fail, dualstack node allocating from non existing cidr",

			fakeNodeHandler: &test.FakeNodeHandler{
				Existing: []*corev1.Node{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name: "node0",
							Labels: map[string]string{
								"testLabel-0": "node0",
							},
						},
						Spec: corev1.NodeSpec{
							PodCIDRs: []string{"10.10.0.1/24", "a00::/86"},
						},
					},
				},
				Clientset: fake.NewClientset(),
			},
			allocatorParams: CIDRAllocatorParams{
				ServiceCIDR:          nil,
				SecondaryServiceCIDR: nil,
			},
			testCIDRMap: getTestCidrMap(
				map[string][]*testClusterCIDR{
					getTestNodeSelector([]testNodeSelectorRequirement{
						{
							key:      "testLabel-0",
							operator: corev1.NodeSelectorOpIn,
							values:   []string{"node0"},
						},
					}): {
						{
							name:            "dual-stack-cidr-allocate-fail",
							perNodeHostBits: 8,
							ipv4CIDR:        "10.10.0.0/16",
							ipv6CIDR:        "ace:cab:deca::/112",
						},
					},
				}),
			allocatedCIDRs:        nil,
			expectedAllocatedCIDR: nil,
		},
		{
			description: "fail, dualstack node allocating bad v4",

			fakeNodeHandler: &test.FakeNodeHandler{
				Existing: []*corev1.Node{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name: "node0",
							Labels: map[string]string{
								"testLabel-0": "node0",
							},
						},
						Spec: corev1.NodeSpec{
							PodCIDRs: []string{"172.10.0.1/24", "ace:cab:deca::1/120"},
						},
					},
				},
				Clientset: fake.NewClientset(),
			},
			allocatorParams: CIDRAllocatorParams{
				ServiceCIDR:          nil,
				SecondaryServiceCIDR: nil,
			},
			testCIDRMap: getTestCidrMap(
				map[string][]*testClusterCIDR{
					getTestNodeSelector([]testNodeSelectorRequirement{
						{
							key:      "testLabel-0",
							operator: corev1.NodeSelectorOpIn,
							values:   []string{"node0"},
						},
					}): {
						{
							name:            "dual-stack-cidr-bad-v4",
							perNodeHostBits: 8,
							ipv4CIDR:        "10.10.0.0/16",
							ipv6CIDR:        "ace:cab:deca::/112",
						},
					},
				}),
			allocatedCIDRs:        nil,
			expectedAllocatedCIDR: nil,
		},
		{
			description: "fail, dualstack node allocating bad v6",

			fakeNodeHandler: &test.FakeNodeHandler{
				Existing: []*corev1.Node{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name: "node0",
							Labels: map[string]string{
								"testLabel-0": "node0",
							},
						},
						Spec: corev1.NodeSpec{
							PodCIDRs: []string{"10.10.0.1/24", "cdd::/86"},
						},
					},
				},
				Clientset: fake.NewClientset(),
			},
			allocatorParams: CIDRAllocatorParams{
				ServiceCIDR:          nil,
				SecondaryServiceCIDR: nil,
			},
			testCIDRMap: getTestCidrMap(
				map[string][]*testClusterCIDR{
					getTestNodeSelector([]testNodeSelectorRequirement{
						{
							key:      "testLabel-0",
							operator: corev1.NodeSelectorOpIn,
							values:   []string{"node0"},
						},
					}): {
						{
							name:            "dual-stack-cidr-bad-v6",
							perNodeHostBits: 8,
							ipv4CIDR:        "10.10.0.0/16",
							ipv6CIDR:        "ace:cab:deca::/112",
						},
					},
				}),
			allocatedCIDRs:        nil,
			expectedAllocatedCIDR: nil,
		},
	}

	logger, ctx := ktesting.NewTestContext(t)

	testFunc := func(tc testCaseMultiCIDR) {
		fakeClient := &clustercidrfake.Clientset{}
		fakeInformerFactory := clustercidrinformer.NewSharedInformerFactory(fakeClient, NoResyncPeriodFunc())
		fakeClusterCIDRInformer := fakeInformerFactory.Networking().V1().ClusterCIDRs()
		fakeCIDRClient := clustercidrfake.NewSimpleClientset().NetworkingV1().ClusterCIDRs()
		// Initialize the range allocator.
		allocator, err := NewMultiCIDRRangeAllocator(ctx, tc.fakeNodeHandler, fakeCIDRClient, test.FakeNodeInformer(tc.fakeNodeHandler), fakeClusterCIDRInformer, tc.allocatorParams, nil, tc.testCIDRMap)
		if err != nil {
			t.Logf("%v: failed to create CIDRRangeAllocator with error %v", tc.description, err)
		}
		rangeAllocator, ok := allocator.(*multiCIDRRangeAllocator)
		if !ok {
			t.Logf("%v: found non-default implementation of CIDRAllocator, skipping white-box test...", tc.description)
			return
		}
		rangeAllocator.nodesSynced = test.AlwaysReady
		rangeAllocator.recorder = &record.FakeRecorder{}

		// this is a bit of white box testing
		// pre allocate the CIDRs as per the test
		for _, allocatedList := range tc.allocatedCIDRs {
			for _, allocated := range allocatedList {
				_, cidr, err := utilnet.ParseCIDRSloppy(allocated)
				if err != nil {
					t.Fatalf("%v: unexpected error when parsing CIDR %v: %v", tc.description, allocated, err)
				}

				rangeAllocator.lock.Lock()
				clusterCIDRList, err := getClusterCIDRList("node0", rangeAllocator.cidrMap)
				rangeAllocator.lock.Unlock()
				if err != nil {
					t.Fatalf("%v: unexpected error when getting associated clusterCIDR for node %v %v", tc.description, "node0", err)
				}

				occupied := false
				for _, clusterCIDR := range clusterCIDRList {
					if err := rangeAllocator.Occupy(clusterCIDR, cidr); err == nil {
						occupied = true
						break
					}
				}
				if !occupied {
					t.Fatalf("%v: unable to occupy CIDR %v", tc.description, allocated)
				}
			}
		}

		if err := allocator.AllocateOrOccupyCIDR(logger, tc.fakeNodeHandler.Existing[0]); err == nil {
			t.Errorf("%v: unexpected success in AllocateOrOccupyCIDR: %v", tc.description, err)
		}
		// We don't expect any updates, so just sleep for some time
		time.Sleep(time.Second)
		if len(tc.fakeNodeHandler.GetUpdatedNodesCopy()) != 0 {
			t.Fatalf("%v: unexpected update of nodes: %v", tc.description, tc.fakeNodeHandler.GetUpdatedNodesCopy())
		}
		if len(tc.expectedAllocatedCIDR) == 0 {
			// nothing further expected
			return
		}
		for _, updatedNode := range tc.fakeNodeHandler.GetUpdatedNodesCopy() {
			if len(updatedNode.Spec.PodCIDRs) == 0 {
				continue // not assigned yet
			}
			// match
			for podCIDRIdx, expectedPodCIDR := range tc.expectedAllocatedCIDR {
				if updatedNode.Spec.PodCIDRs[podCIDRIdx] == expectedPodCIDR {
					t.Errorf("%v: found cidr %v that should not be allocated on node with CIDRs:%v", tc.description, expectedPodCIDR, updatedNode.Spec.PodCIDRs)
					break
				}
			}
		}
	}
	for _, tc := range testCaseMultiCIDRs {
		t.Run(tc.description, func(t *testing.T) {
			testFunc(tc)
		})
	}
}

type releasetestCaseMultiCIDR struct {
	description                      string
	fakeNodeHandler                  *test.FakeNodeHandler
	testCIDRMap                      map[string]*clusterCIDREntry
	allocatorParams                  CIDRAllocatorParams
	expectedAllocatedCIDRFirstRound  map[int]string
	expectedAllocatedCIDRSecondRound map[int]string
	allocatedCIDRs                   map[int][]string
	cidrsToRelease                   [][]string
}

func TestMultiCIDRReleaseCIDRSuccess(t *testing.T) {
	// Non-parallel test (overrides global var)
	oldNodePollInterval := nodePollInterval
	nodePollInterval = test.NodePollInterval
	defer func() {
		nodePollInterval = oldNodePollInterval
	}()

	testCaseMultiCIDRs := []releasetestCaseMultiCIDR{
		{
			description: "Correctly release preallocated CIDR",
			fakeNodeHandler: &test.FakeNodeHandler{
				Existing: []*corev1.Node{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name: "node0",
							Labels: map[string]string{
								"testLabel-0": "node0",
							},
						},
					},
				},
				Clientset: fake.NewClientset(),
			},
			allocatorParams: CIDRAllocatorParams{
				ServiceCIDR:          nil,
				SecondaryServiceCIDR: nil,
			},
			testCIDRMap: getTestCidrMap(
				map[string][]*testClusterCIDR{
					getTestNodeSelector([]testNodeSelectorRequirement{
						{
							key:      "testLabel-0",
							operator: corev1.NodeSelectorOpIn,
							values:   []string{"node0"},
						},
					}): {
						{
							name:            "cidr-release",
							perNodeHostBits: 2,
							ipv4CIDR:        "127.123.234.0/28",
						},
					},
				}),
			allocatedCIDRs: map[int][]string{
				0: {"127.123.234.0/30", "127.123.234.4/30", "127.123.234.8/30", "127.123.234.12/30"},
			},
			expectedAllocatedCIDRFirstRound: nil,
			cidrsToRelease: [][]string{
				{"127.123.234.4/30"},
			},
			expectedAllocatedCIDRSecondRound: map[int]string{
				0: "127.123.234.4/30",
			},
		},
		{
			description: "Correctly recycle CIDR",
			fakeNodeHandler: &test.FakeNodeHandler{
				Existing: []*corev1.Node{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name: "node0",
							Labels: map[string]string{
								"testLabel-0": "node0",
							},
						},
					},
				},
				Clientset: fake.NewClientset(),
			},
			allocatorParams: CIDRAllocatorParams{
				ServiceCIDR:          nil,
				SecondaryServiceCIDR: nil,
			},
			testCIDRMap: getTestCidrMap(
				map[string][]*testClusterCIDR{
					getTestNodeSelector([]testNodeSelectorRequirement{
						{
							key:      "testLabel-0",
							operator: corev1.NodeSelectorOpIn,
							values:   []string{"node0"},
						},
					}): {
						{
							name:            "cidr-release",
							perNodeHostBits: 2,
							ipv4CIDR:        "127.123.234.0/28",
						},
					},
				}),
			allocatedCIDRs: map[int][]string{
				0: {"127.123.234.4/30", "127.123.234.8/30", "127.123.234.12/30"},
			},
			expectedAllocatedCIDRFirstRound: map[int]string{
				0: "127.123.234.0/30",
			},
			cidrsToRelease: [][]string{
				{"127.123.234.0/30"},
			},
			expectedAllocatedCIDRSecondRound: map[int]string{
				0: "127.123.234.0/30",
			},
		},
	}
	logger, ctx := ktesting.NewTestContext(t)
	testFunc := func(tc releasetestCaseMultiCIDR) {
		fakeClient := &clustercidrfake.Clientset{}
		fakeInformerFactory := clustercidrinformer.NewSharedInformerFactory(fakeClient, NoResyncPeriodFunc())
		fakeClusterCIDRInformer := fakeInformerFactory.Networking().V1().ClusterCIDRs()
		fakeCIDRClient := clustercidrfake.NewSimpleClientset().NetworkingV1().ClusterCIDRs()
		// Initialize the range allocator.
		allocator, _ := NewMultiCIDRRangeAllocator(ctx, tc.fakeNodeHandler, fakeCIDRClient, test.FakeNodeInformer(tc.fakeNodeHandler), fakeClusterCIDRInformer, tc.allocatorParams, nil, tc.testCIDRMap)
		rangeAllocator, ok := allocator.(*multiCIDRRangeAllocator)
		if !ok {
			t.Logf("%v: found non-default implementation of CIDRAllocator, skipping white-box test...", tc.description)
			return
		}
		rangeAllocator.nodesSynced = test.AlwaysReady
		rangeAllocator.recorder = &record.FakeRecorder{}

		// this is a bit of white box testing
		for _, allocatedList := range tc.allocatedCIDRs {
			for _, allocated := range allocatedList {
				_, cidr, err := utilnet.ParseCIDRSloppy(allocated)
				if err != nil {
					t.Fatalf("%v: unexpected error when parsing CIDR %v: %v", tc.description, allocated, err)
				}

				rangeAllocator.lock.Lock()
				clusterCIDRList, err := getClusterCIDRList("node0", rangeAllocator.cidrMap)
				rangeAllocator.lock.Unlock()
				if err != nil {
					t.Fatalf("%v: unexpected error when getting associated clusterCIDR for node %v %v", tc.description, "node0", err)
				}

				occupied := false
				for _, clusterCIDR := range clusterCIDRList {
					if err := rangeAllocator.Occupy(clusterCIDR, cidr); err == nil {
						occupied = true
						clusterCIDR.AssociatedNodes["fakeNode"] = true
						break
					}
				}
				if !occupied {
					t.Fatalf("%v: unable to occupy CIDR %v", tc.description, allocated)
				}
			}
		}

		err := allocator.AllocateOrOccupyCIDR(logger, tc.fakeNodeHandler.Existing[0])
		if len(tc.expectedAllocatedCIDRFirstRound) != 0 {
			if err != nil {
				t.Fatalf("%v: unexpected error in AllocateOrOccupyCIDR: %v", tc.description, err)
			}
			if err := test.WaitForUpdatedNodeWithTimeout(tc.fakeNodeHandler, 1, wait.ForeverTestTimeout); err != nil {
				t.Fatalf("%v: timeout while waiting for Node update: %v", tc.description, err)
			}
		} else {
			if err == nil {
				t.Fatalf("%v: unexpected success in AllocateOrOccupyCIDR: %v", tc.description, err)
			}
			// We don't expect any updates here
			time.Sleep(time.Second)
			if len(tc.fakeNodeHandler.GetUpdatedNodesCopy()) != 0 {
				t.Fatalf("%v: unexpected update of nodes: %v", tc.description, tc.fakeNodeHandler.GetUpdatedNodesCopy())
			}
		}

		for _, cidrToRelease := range tc.cidrsToRelease {
			nodeToRelease := corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "fakeNode",
					Labels: map[string]string{
						"testLabel-0": "node0",
					},
				},
			}
			nodeToRelease.Spec.PodCIDRs = cidrToRelease
			err = allocator.ReleaseCIDR(logger, &nodeToRelease)
			if err != nil {
				t.Fatalf("%v: unexpected error in ReleaseCIDR: %v", tc.description, err)
			}
		}
		if err = allocator.AllocateOrOccupyCIDR(logger, tc.fakeNodeHandler.Existing[0]); err != nil {
			t.Fatalf("%v: unexpected error in AllocateOrOccupyCIDR: %v", tc.description, err)
		}
		if err := test.WaitForUpdatedNodeWithTimeout(tc.fakeNodeHandler, 1, wait.ForeverTestTimeout); err != nil {
			t.Fatalf("%v: timeout while waiting for Node update: %v", tc.description, err)
		}

		if len(tc.expectedAllocatedCIDRSecondRound) == 0 {
			// nothing further expected
			return
		}
		for _, updatedNode := range tc.fakeNodeHandler.GetUpdatedNodesCopy() {
			if len(updatedNode.Spec.PodCIDRs) == 0 {
				continue // not assigned yet
			}
			// match
			for podCIDRIdx, expectedPodCIDR := range tc.expectedAllocatedCIDRSecondRound {
				if updatedNode.Spec.PodCIDRs[podCIDRIdx] != expectedPodCIDR {
					t.Errorf("%v: found cidr %v that should not be allocated on node with CIDRs:%v", tc.description, expectedPodCIDR, updatedNode.Spec.PodCIDRs)
					break
				}
			}
		}
	}

	for _, tc := range testCaseMultiCIDRs {
		testFunc(tc)
	}
}

// ClusterCIDR tests.

var alwaysReady = func() bool { return true }

type nodeIPAMController struct {
	*multiCIDRRangeAllocator
	clusterCIDRStore cache.Store
}

func newController(ctx context.Context) (*clustercidrfake.Clientset, *nodeIPAMController) {
	client := clustercidrfake.NewSimpleClientset()
	informerFactory := clustercidrinformer.NewSharedInformerFactory(client, NoResyncPeriodFunc())
	cccInformer := informerFactory.Networking().V1().ClusterCIDRs()

	cccIndexer := cccInformer.Informer().GetIndexer()

	nodeClient := fake.NewClientset()
	nodeInformerFactory := informers.NewSharedInformerFactory(nodeClient, NoResyncPeriodFunc())
	nodeInformer := nodeInformerFactory.Core().V1().Nodes()

	// These reactors are required to mock functionality that would be covered
	// automatically if we weren't using the fake client.
	client.PrependReactor("create", "clustercidrs", func(action k8stesting.Action) (bool, runtime.Object, error) {
		clusterCIDR := action.(k8stesting.CreateAction).GetObject().(*v1.ClusterCIDR)

		if clusterCIDR.GenerateName != "" {
			clusterCIDR.Name = fmt.Sprintf("%s-%s", clusterCIDR.GenerateName, rand.String(8))
			clusterCIDR.GenerateName = ""
		}
		clusterCIDR.Generation = 1
		cccIndexer.Add(clusterCIDR)

		return false, clusterCIDR, nil
	})
	client.PrependReactor("update", "clustercidrs", func(action k8stesting.Action) (bool, runtime.Object, error) {
		clusterCIDR := action.(k8stesting.CreateAction).GetObject().(*v1.ClusterCIDR)
		clusterCIDR.Generation++
		cccIndexer.Update(clusterCIDR)

		return false, clusterCIDR, nil
	})

	_, clusterCIDR, _ := utilnet.ParseCIDRSloppy("192.168.0.0/16")
	_, serviceCIDR, _ := utilnet.ParseCIDRSloppy("10.1.0.0/16")

	allocatorParams := CIDRAllocatorParams{
		ClusterCIDRs:         []*net.IPNet{clusterCIDR},
		ServiceCIDR:          serviceCIDR,
		SecondaryServiceCIDR: nil,
		NodeCIDRMaskSizes:    []int{24},
	}
	testCIDRMap := make(map[string]*clusterCIDREntry)

	// Initialize the range allocator.
	ra, _ := NewMultiCIDRRangeAllocator(ctx, nodeClient, client.NetworkingV1().ClusterCIDRs(), nodeInformer, cccInformer, allocatorParams, nil, testCIDRMap)
	cccController := ra.(*multiCIDRRangeAllocator)

	cccController.clusterCIDRSynced = alwaysReady

	return client, &nodeIPAMController{
		cccController,
		informerFactory.Networking().V1().ClusterCIDRs().Informer().GetStore(),
	}
}

// Ensure default ClusterCIDR is created during bootstrap.
func TestClusterCIDRDefault(t *testing.T) {
	defaultCCC := makeClusterCIDR(defaultClusterCIDRName, "192.168.0.0/16", "", 8, nil)
	_, ctx := ktesting.NewTestContext(t)
	client, _ := newController(ctx)
	createdCCC, err := client.NetworkingV1().ClusterCIDRs().Get(context.TODO(), defaultClusterCIDRName, metav1.GetOptions{})
	assert.Nil(t, err, "Expected no error getting clustercidr objects")
	assert.Equal(t, defaultCCC.Spec, createdCCC.Spec)
	assert.Contains(t, createdCCC.GetFinalizers(), clusterCIDRFinalizer)
}

// Ensure SyncClusterCIDR creates a new valid ClusterCIDR.
func TestSyncClusterCIDRCreate(t *testing.T) {
	tests := []struct {
		name    string
		ccc     *v1.ClusterCIDR
		wantErr bool
	}{
		{
			name:    "valid IPv4 ClusterCIDR with no NodeSelector",
			ccc:     makeClusterCIDR("ipv4-ccc", "10.2.0.0/16", "", 8, nil),
			wantErr: false,
		},
		{
			name:    "valid IPv4 ClusterCIDR with NodeSelector",
			ccc:     makeClusterCIDR("ipv4-ccc-label", "10.3.0.0/16", "", 8, makeNodeSelector("foo", corev1.NodeSelectorOpIn, []string{"bar"})),
			wantErr: false,
		},
		{
			name:    "valid IPv4 ClusterCIDR with overlapping CIDRs",
			ccc:     makeClusterCIDR("ipv4-ccc-overlap", "10.2.0.0/24", "", 8, makeNodeSelector("foo", corev1.NodeSelectorOpIn, []string{"bar"})),
			wantErr: false,
		},
		{
			name:    "valid IPv6 ClusterCIDR with no NodeSelector",
			ccc:     makeClusterCIDR("ipv6-ccc", "", "fd00:1::/112", 8, nil),
			wantErr: false,
		},
		{
			name:    "valid IPv6 ClusterCIDR with NodeSelector",
			ccc:     makeClusterCIDR("ipv6-ccc-label", "", "fd00:2::/112", 8, makeNodeSelector("foo", corev1.NodeSelectorOpIn, []string{"bar"})),
			wantErr: false,
		},
		{
			name:    "valid IPv6 ClusterCIDR with overlapping CIDRs",
			ccc:     makeClusterCIDR("ipv6-ccc-overlap", "", "fd00:1:1::/112", 8, makeNodeSelector("foo", corev1.NodeSelectorOpIn, []string{"bar"})),
			wantErr: false,
		},
		{
			name:    "valid Dualstack ClusterCIDR with no NodeSelector",
			ccc:     makeClusterCIDR("dual-ccc", "10.2.0.0/16", "fd00:1::/112", 8, nil),
			wantErr: false,
		},
		{
			name:    "valid DualStack ClusterCIDR with NodeSelector",
			ccc:     makeClusterCIDR("dual-ccc-label", "10.3.0.0/16", "fd00:2::/112", 8, makeNodeSelector("foo", corev1.NodeSelectorOpIn, []string{"bar"})),
			wantErr: false,
		},
		{
			name:    "valid Dualstack ClusterCIDR with overlapping CIDRs",
			ccc:     makeClusterCIDR("dual-ccc-overlap", "10.2.0.0/16", "fd00:1:1::/112", 8, makeNodeSelector("foo", corev1.NodeSelectorOpIn, []string{"bar"})),
			wantErr: false,
		},
		// invalid ClusterCIDRs.
		{
			name:    "invalid ClusterCIDR with both IPv4 and IPv6 CIDRs nil",
			ccc:     makeClusterCIDR("invalid-ccc", "", "", 0, nil),
			wantErr: true,
		},
		{
			name:    "invalid IPv4 ClusterCIDR",
			ccc:     makeClusterCIDR("invalid-ipv4-ccc", "1000.2.0.0/16", "", 8, nil),
			wantErr: true,
		},
		{
			name:    "invalid IPv6 ClusterCIDR",
			ccc:     makeClusterCIDR("invalid-ipv6-ccc", "", "aaaaa:1:1::/112", 8, nil),
			wantErr: true,
		},
		{
			name:    "invalid dualstack ClusterCIDR",
			ccc:     makeClusterCIDR("invalid-dual-ccc", "10.2.0.0/16", "aaaaa:1:1::/112", 8, makeNodeSelector("foo", corev1.NodeSelectorOpIn, []string{"bar"})),
			wantErr: true,
		},
	}
	_, ctx := ktesting.NewTestContext(t)
	client, cccController := newController(ctx)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cccController.clusterCIDRStore.Add(tc.ccc)
			err := cccController.syncClusterCIDR(ctx, tc.ccc.Name)

			if tc.wantErr {
				assert.Error(t, err)
				return
			}

			assert.NoError(t, err)
			expectActions(t, client.Actions(), 1, "create", "clustercidrs")

			createdCCC, err := client.NetworkingV1().ClusterCIDRs().Get(context.TODO(), tc.ccc.Name, metav1.GetOptions{})
			require.NoError(t, err, "Expected no error getting clustercidr object")
			assert.Equal(t, tc.ccc.Spec, createdCCC.Spec)
			assert.Equal(t, []string{clusterCIDRFinalizer}, createdCCC.Finalizers)
		})
	}
}

// Ensure syncClusterCIDR for ClusterCIDR delete removes the ClusterCIDR.
func TestSyncClusterCIDRDelete(t *testing.T) {
	_, ctx := ktesting.NewTestContext(t)
	_, cccController := newController(ctx)

	testCCC := makeClusterCIDR("testing-1", "10.1.0.0/16", "", 8, makeNodeSelector("foo", corev1.NodeSelectorOpIn, []string{"bar"}))

	cccController.clusterCIDRStore.Add(testCCC)
	err := cccController.syncClusterCIDR(ctx, testCCC.Name)
	assert.NoError(t, err)

	deletionTimestamp := metav1.Now()
	testCCC.DeletionTimestamp = &deletionTimestamp
	cccController.clusterCIDRStore.Update(testCCC)
	err = cccController.syncClusterCIDR(ctx, testCCC.Name)
	assert.NoError(t, err)
}

// Ensure syncClusterCIDR for ClusterCIDR delete does not remove ClusterCIDR
// if a node is associated with the ClusterCIDR.
func TestSyncClusterCIDRDeleteWithNodesAssociated(t *testing.T) {
	_, ctx := ktesting.NewTestContext(t)
	client, cccController := newController(ctx)

	testCCC := makeClusterCIDR("testing-1", "10.1.0.0/16", "", 8, makeNodeSelector("foo", corev1.NodeSelectorOpIn, []string{"bar"}))

	cccController.clusterCIDRStore.Add(testCCC)
	err := cccController.syncClusterCIDR(ctx, testCCC.Name)
	assert.NoError(t, err)

	// Mock the IPAM controller behavior associating node with ClusterCIDR.
	cccController.lock.Lock()
	cccController.cidrMap[testCCC.Name].clusterCIDR.AssociatedNodes["test-node"] = true
	cccController.lock.Unlock()

	createdCCC, err := client.NetworkingV1().ClusterCIDRs().Get(context.TODO(), testCCC.Name, metav1.GetOptions{})
	assert.Nil(t, err, "Expected no error getting clustercidr object")

	deletionTimestamp := metav1.Now()
	createdCCC.DeletionTimestamp = &deletionTimestamp
	cccController.clusterCIDRStore.Update(createdCCC)
	err = cccController.syncClusterCIDR(ctx, createdCCC.Name)
	assert.Error(t, err, fmt.Sprintf("ClusterCIDR %s marked as terminating, won't be deleted until all associated nodes are deleted", createdCCC.Name))
}

// Ensure releasing the last node of a terminating ClusterCIDR enqueues the
// ClusterCIDR, so that its deletion does not wait for its next retry.
func TestReleaseCIDREnqueuesTerminatingClusterCIDR(t *testing.T) {
	logger, ctx := ktesting.NewTestContext(t)
	client, cccController := newController(ctx)

	testCCC := makeClusterCIDR("testing-1", "10.2.0.0/16", "", 8, makeNodeSelector("foo", corev1.NodeSelectorOpIn, []string{"bar"}))
	cccController.clusterCIDRStore.Add(testCCC)
	require.NoError(t, cccController.syncClusterCIDR(ctx, testCCC.Name))

	nodes := []*corev1.Node{
		makeNode("test-node-1", map[string]string{"foo": "bar"}),
		makeNode("test-node-2", map[string]string{"foo": "bar"}),
	}
	nodes[0].Spec.PodCIDRs = []string{"10.2.0.0/24"}
	nodes[1].Spec.PodCIDRs = []string{"10.2.1.0/24"}
	cccController.lock.Lock()
	for _, node := range nodes {
		require.NoError(t, cccController.occupyCIDRs(logger, node, cccController.cidrMap))
	}
	cccController.lock.Unlock()

	createdCCC, err := client.NetworkingV1().ClusterCIDRs().Get(context.TODO(), testCCC.Name, metav1.GetOptions{})
	require.NoError(t, err)
	deletionTimestamp := metav1.Now()
	createdCCC.DeletionTimestamp = &deletionTimestamp
	cccController.clusterCIDRStore.Update(createdCCC)
	require.Error(t, cccController.syncClusterCIDR(ctx, createdCCC.Name))
	require.Zero(t, cccController.cidrQueue.Len())

	require.NoError(t, cccController.ReleaseCIDR(logger, nodes[0]))
	assert.Zero(t, cccController.cidrQueue.Len(), "ClusterCIDR enqueued while a node is associated")

	require.NoError(t, cccController.ReleaseCIDR(logger, nodes[1]))
	require.Equal(t, 1, cccController.cidrQueue.Len(), "ClusterCIDR not enqueued after its last node was released")
	key, _ := cccController.cidrQueue.Get()
	defer cccController.cidrQueue.Done(key)
	assert.Equal(t, testCCC.Name, key)
	assert.NoError(t, cccController.syncClusterCIDR(ctx, key))
}

func TestMultiCIDRSetDataRace(t *testing.T) {
	_, cidr, err := utilnet.ParseCIDRSloppy("10.0.0.0/16")
	require.NoError(t, err)

	cidrSet, err := multicidrset.NewMultiCIDRSet("race-test", cidr, 8)
	require.NoError(t, err)

	clusterCIDR := &multicidrset.ClusterCIDR{
		Name:            "race-test",
		IPv4CIDRSet:     cidrSet,
		AssociatedNodes: make(map[string]bool),
	}

	ra := &multiCIDRRangeAllocator{
		lock:    &sync.Mutex{},
		cidrMap: map[string]*clusterCIDREntry{clusterCIDR.Name: {clusterCIDR: clusterCIDR}},
	}

	logger, _ := ktesting.NewTestContext(t)
	_, lookupCIDR, err := utilnet.ParseCIDRSloppy("10.0.1.0/24")
	require.NoError(t, err)

	var wg sync.WaitGroup
	wg.Add(4)
	go func() { defer wg.Done(); cidrSet.Occupy(lookupCIDR) }()
	go func() { defer wg.Done(); cidrSet.Release(lookupCIDR) }()
	go func() {
		defer wg.Done()
		ra.lock.Lock()
		ra.cidrInAllocatedList(logger, lookupCIDR, ra.cidrMap)
		ra.lock.Unlock()
	}()
	go func() {
		defer wg.Done()
		ra.lock.Lock()
		ra.cidrOverlapWithAllocatedList(logger, lookupCIDR, ra.cidrMap)
		ra.lock.Unlock()
	}()
	wg.Wait()
}

func expectActions(t *testing.T, actions []k8stesting.Action, num int, verb, resource string) {
	t.Helper()
	// if actions are less, the below logic will panic.
	if num > len(actions) {
		t.Fatalf("len of actions %v is unexpected. Expected to be at least %v", len(actions), num+1)
	}

	for i := 0; i < num; i++ {
		relativePos := len(actions) - i - 1
		assert.Equal(t, verb, actions[relativePos].GetVerb(), "Expected action -%d verb to be %s", i, verb)
		assert.Equal(t, resource, actions[relativePos].GetResource().Resource, "Expected action -%d resource to be %s", i, resource)
	}
}

func makeNodeSelector(key string, op corev1.NodeSelectorOperator, values []string) *corev1.NodeSelector {
	return &corev1.NodeSelector{
		NodeSelectorTerms: []corev1.NodeSelectorTerm{
			{
				MatchExpressions: []corev1.NodeSelectorRequirement{
					{
						Key:      key,
						Operator: op,
						Values:   values,
					},
				},
			},
		},
	}
}

// Returns 0 for resyncPeriod in case resyncing is not needed.
func NoResyncPeriodFunc() time.Duration {
	return 0
}

func TestHandleNodeDeleteWithTombstone(t *testing.T) {
	_, ctx := ktesting.NewTestContext(t)

	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "test-node",
			Labels: map[string]string{"testLabel-0": "node0"},
		},
		Spec: corev1.NodeSpec{
			PodCIDRs: []string{"10.10.0.1/24"},
		},
	}

	fakeNodeHandler := &test.FakeNodeHandler{
		Existing:  []*corev1.Node{node},
		Clientset: fake.NewClientset(),
	}

	fakeNodeInformer := test.FakeNodeInformer(fakeNodeHandler)
	fakeClient := &clustercidrfake.Clientset{}
	fakeInformerFactory := clustercidrinformer.NewSharedInformerFactory(fakeClient, NoResyncPeriodFunc())
	fakeClusterCIDRInformer := fakeInformerFactory.Networking().V1().ClusterCIDRs()
	fakeCIDRClient := clustercidrfake.NewSimpleClientset().NetworkingV1().ClusterCIDRs()

	allocatorParams := CIDRAllocatorParams{
		ServiceCIDR:          nil,
		SecondaryServiceCIDR: nil,
	}
	testCIDRMap := getTestCidrMap(
		map[string][]*testClusterCIDR{
			getTestNodeSelector([]testNodeSelectorRequirement{
				{
					key:      "testLabel-0",
					operator: corev1.NodeSelectorOpIn,
					values:   []string{"node0"},
				},
			}): {
				{
					name:            "test-cidr",
					perNodeHostBits: 8,
					ipv4CIDR:        "10.10.0.0/16",
				},
			},
		})

	nodeList, _ := fakeNodeHandler.List(context.TODO(), metav1.ListOptions{})
	allocator, err := NewMultiCIDRRangeAllocator(ctx, fakeNodeHandler, fakeCIDRClient, fakeNodeInformer, fakeClusterCIDRInformer, allocatorParams, nodeList, testCIDRMap)
	require.NoError(t, err)

	ra := allocator.(*multiCIDRRangeAllocator)

	tests := []struct {
		name string
		obj  interface{}
	}{
		{
			name: "regular node object",
			obj:  node,
		},
		{
			name: "DeletedFinalStateUnknown tombstone wrapping a node",
			obj: cache.DeletedFinalStateUnknown{
				Key: "test-node",
				Obj: node,
			},
		},
		{
			name: "DeletedFinalStateUnknown tombstone wrapping a non-node object",
			obj: cache.DeletedFinalStateUnknown{
				Key: "test-node",
				Obj: "not-a-node",
			},
		},
		{
			name: "completely unexpected object type",
			obj:  "not-a-node",
		},
	}

	logger, _ := ktesting.NewTestContext(t)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.NotPanics(t, func() {
				ra.handleNodeDelete(logger, tc.obj)
			})
		})
	}
}

// newDeleteRaceAllocator returns an allocator with a ClusterCIDR selecting the
// given nodes, and the store of its node informer.
func newDeleteRaceAllocator(t *testing.T, ctx context.Context, nodes ...*corev1.Node) (*multiCIDRRangeAllocator, cache.Store) {
	t.Helper()

	fakeNodeHandler := &test.FakeNodeHandler{
		Existing:  nodes,
		Clientset: fake.NewClientset(),
	}
	fakeNodeInformer := test.FakeNodeInformer(fakeNodeHandler)
	fakeInformerFactory := clustercidrinformer.NewSharedInformerFactory(&clustercidrfake.Clientset{}, NoResyncPeriodFunc())
	testCIDRMap := getTestCidrMap(
		map[string][]*testClusterCIDR{
			getTestNodeSelector([]testNodeSelectorRequirement{
				{
					key:      "testLabel-0",
					operator: corev1.NodeSelectorOpIn,
					values:   []string{"node0"},
				},
			}): {
				{
					name:            "test-cidr",
					perNodeHostBits: 8,
					ipv4CIDR:        "10.10.0.0/16",
				},
			},
		})

	allocator, err := NewMultiCIDRRangeAllocator(
		ctx, fakeNodeHandler, clustercidrfake.NewSimpleClientset().NetworkingV1().ClusterCIDRs(),
		fakeNodeInformer, fakeInformerFactory.Networking().V1().ClusterCIDRs(), CIDRAllocatorParams{},
		&corev1.NodeList{}, testCIDRMap,
	)
	require.NoError(t, err)
	return allocator.(*multiCIDRRangeAllocator), fakeNodeInformer.Informer().GetStore()
}

// assertCIDRFree asserts that no ClusterCIDR has the node associated or the CIDR
// allocated.
func assertCIDRFree(t *testing.T, ra *multiCIDRRangeAllocator, nodeName, cidr string) {
	t.Helper()

	_, podCIDR, err := utilnet.ParseCIDRSloppy(cidr)
	require.NoError(t, err)
	for _, entry := range ra.cidrMap {
		clusterCIDR := entry.clusterCIDR
		assert.False(t, clusterCIDR.AssociatedNodes[nodeName], "node is associated with ClusterCIDR %s", clusterCIDR.Name)
		assert.False(t, clusterCIDR.IPv4CIDRSet.CIDRAllocated(podCIDR), "CIDR %s is allocated in ClusterCIDR %s", cidr, clusterCIDR.Name)
	}
}

// TestAllocateOrOccupyCIDRForDeletedNode covers a node sync that read the node
// from the lister before the node was deleted, and runs after handleNodeDelete
// released it. The informer removes the node from its store before it calls the
// delete handler.
func TestAllocateOrOccupyCIDRForDeletedNode(t *testing.T) {
	t.Run("node with Pod CIDRs", func(t *testing.T) {
		logger, ctx := ktesting.NewTestContext(t)
		node := makeNode("node-1", map[string]string{"testLabel-0": "node0"})
		node.Spec.PodCIDRs = []string{"10.10.0.0/24"}
		ra, nodeStore := newDeleteRaceAllocator(t, ctx, node)
		require.NoError(t, ra.AllocateOrOccupyCIDR(logger, node))

		require.NoError(t, nodeStore.Delete(node))
		require.NoError(t, ra.ReleaseCIDR(logger, node))
		require.NoError(t, ra.AllocateOrOccupyCIDR(logger, node))

		assertCIDRFree(t, ra, node.Name, "10.10.0.0/24")
	})

	t.Run("node without Pod CIDRs", func(t *testing.T) {
		logger, ctx := ktesting.NewTestContext(t)
		node := makeNode("node-1", map[string]string{"testLabel-0": "node0"})
		ra, nodeStore := newDeleteRaceAllocator(t, ctx, node)

		require.NoError(t, nodeStore.Delete(node))
		require.NoError(t, ra.ReleaseCIDR(logger, node))
		require.NoError(t, ra.AllocateOrOccupyCIDR(logger, node))

		assertCIDRFree(t, ra, node.Name, "10.10.0.0/24")
	})
}

// TestUpdateCIDRsAllocationForDeletedNode asserts that the CIDRs reserved for a
// node are released when the node is deleted before they are assigned to it.
func TestUpdateCIDRsAllocationForDeletedNode(t *testing.T) {
	logger, ctx := ktesting.NewTestContext(t)
	node := makeNode("node-1", map[string]string{"testLabel-0": "node0"})
	ra, nodeStore := newDeleteRaceAllocator(t, ctx, node)

	ra.lock.Lock()
	defer ra.lock.Unlock()
	cidrs, clusterCIDR, err := ra.prioritizedCIDRs(logger, node, ra.cidrMap)
	require.NoError(t, err)
	require.Equal(t, []string{"10.10.0.0/24"}, ipnetToStringList(cidrs))

	require.NoError(t, nodeStore.Delete(node))
	err = ra.updateCIDRsAllocation(logger, multiCIDRNodeReservedCIDRs{
		nodeReservedCIDRs: nodeReservedCIDRs{
			nodeName:       node.Name,
			allocatedCIDRs: cidrs,
		},
		clusterCIDR: clusterCIDR,
	})
	require.Error(t, err)

	assertCIDRFree(t, ra, node.Name, "10.10.0.0/24")
}

func testNode(name string, labels map[string]string) *corev1.Node {
	return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}}
}

func matchExpression(key string, op corev1.NodeSelectorOperator, values ...string) corev1.NodeSelectorRequirement {
	return corev1.NodeSelectorRequirement{Key: key, Operator: op, Values: values}
}

// TestNodeSelectorMatcher covers the NodeSelector semantics: terms are ORed,
// requirements within a term are ANDed, an empty selector or term matches
// nothing, and matchFields is matched against Node fields rather than Node
// labels.
func TestNodeSelectorMatcher(t *testing.T) {
	inZoneA := matchExpression("zone", corev1.NodeSelectorOpIn, "a")
	inRackR1 := matchExpression("rack", corev1.NodeSelectorOpIn, "r1")
	isNode1 := matchExpression("metadata.name", corev1.NodeSelectorOpIn, "node1")

	node := testNode("node1", map[string]string{"zone": "a", "rack": "r1"})

	testCases := []struct {
		description  string
		nodeSelector *corev1.NodeSelector
		node         *corev1.Node
		wantMatch    bool
		wantReqCount int
	}{
		{
			description:  "nodeSelector without terms matches nothing",
			nodeSelector: &corev1.NodeSelector{},
			node:         node,
			wantMatch:    false,
		},
		{
			description:  "nodeSelector with an empty terms list matches nothing",
			nodeSelector: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{}},
			node:         node,
			wantMatch:    false,
		},
		{
			description: "requirements within a term are ANDed, all satisfied",
			nodeSelector: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{
				{MatchExpressions: []corev1.NodeSelectorRequirement{inZoneA, inRackR1}},
			}},
			node:         node,
			wantMatch:    true,
			wantReqCount: 2,
		},
		{
			description: "requirements within a term are ANDed, one unsatisfied",
			nodeSelector: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{
				{MatchExpressions: []corev1.NodeSelectorRequirement{
					inZoneA,
					matchExpression("rack", corev1.NodeSelectorOpIn, "r2"),
				}},
			}},
			node:      node,
			wantMatch: false,
		},
		{
			description: "terms are ORed, only the second term matches",
			nodeSelector: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{
				{MatchExpressions: []corev1.NodeSelectorRequirement{matchExpression("zone", corev1.NodeSelectorOpIn, "b")}},
				{MatchExpressions: []corev1.NodeSelectorRequirement{inRackR1}},
			}},
			node:         node,
			wantMatch:    true,
			wantReqCount: 1,
		},
		{
			description: "terms are ORed, no term matches",
			nodeSelector: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{
				{MatchExpressions: []corev1.NodeSelectorRequirement{matchExpression("zone", corev1.NodeSelectorOpIn, "b")}},
				{MatchExpressions: []corev1.NodeSelectorRequirement{matchExpression("rack", corev1.NodeSelectorOpIn, "r2")}},
			}},
			node:      node,
			wantMatch: false,
		},
		{
			description: "several terms match, the most specific one sets the requirement count",
			nodeSelector: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{
				{MatchExpressions: []corev1.NodeSelectorRequirement{inZoneA}},
				{MatchExpressions: []corev1.NodeSelectorRequirement{inZoneA, inRackR1}},
			}},
			node:         node,
			wantMatch:    true,
			wantReqCount: 2,
		},
		{
			description: "requirement count does not depend on term order",
			nodeSelector: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{
				{MatchExpressions: []corev1.NodeSelectorRequirement{inZoneA, inRackR1}},
				{MatchExpressions: []corev1.NodeSelectorRequirement{inZoneA}},
			}},
			node:         node,
			wantMatch:    true,
			wantReqCount: 2,
		},
		{
			description: "an empty term matches nothing",
			nodeSelector: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{
				{},
			}},
			node:      node,
			wantMatch: false,
		},
		{
			description: "an empty term does not rescue a non-matching sibling term",
			nodeSelector: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{
				{},
				{MatchExpressions: []corev1.NodeSelectorRequirement{matchExpression("zone", corev1.NodeSelectorOpIn, "b")}},
			}},
			node:      node,
			wantMatch: false,
		},
		{
			description: "matchFields is matched against the node name",
			nodeSelector: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{
				{MatchFields: []corev1.NodeSelectorRequirement{isNode1}},
			}},
			node:         node,
			wantMatch:    true,
			wantReqCount: 1,
		},
		{
			description: "matchFields does not match a different node name",
			nodeSelector: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{
				{MatchFields: []corev1.NodeSelectorRequirement{isNode1}},
			}},
			node:      testNode("node2", map[string]string{"zone": "a"}),
			wantMatch: false,
		},
		{
			description: "matchFields is not satisfied by a label of the same name",
			nodeSelector: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{
				{MatchFields: []corev1.NodeSelectorRequirement{isNode1}},
			}},
			node:      testNode("node2", map[string]string{"metadata.name": "node1"}),
			wantMatch: false,
		},
		{
			description: "matchFields and matchExpressions in one term are ANDed",
			nodeSelector: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{
				{
					MatchExpressions: []corev1.NodeSelectorRequirement{inZoneA},
					MatchFields:      []corev1.NodeSelectorRequirement{isNode1},
				},
			}},
			node:         node,
			wantMatch:    true,
			wantReqCount: 2,
		},
		{
			description: "matchFields and matchExpressions in one term are ANDed, field unsatisfied",
			nodeSelector: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{
				{
					MatchExpressions: []corev1.NodeSelectorRequirement{inZoneA},
					MatchFields:      []corev1.NodeSelectorRequirement{matchExpression("metadata.name", corev1.NodeSelectorOpIn, "node2")},
				},
			}},
			node:      node,
			wantMatch: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			matcher, err := newNodeSelectorMatcher(tc.nodeSelector)
			require.NoError(t, err)

			match, reqCount := matcher.match(tc.node)
			assert.Equal(t, tc.wantMatch, match)
			assert.Equal(t, tc.wantReqCount, reqCount)
		})
	}
}

// TestNewNodeSelectorMatcherNil checks that a nil nodeSelector compiles to a
// nil matcher, which orderedMatchingClusterCIDRs treats as the catch-all.
func TestNewNodeSelectorMatcherNil(t *testing.T) {
	matcher, err := newNodeSelectorMatcher(nil)
	require.NoError(t, err)
	assert.Nil(t, matcher)
}

func TestNodeSelectorMatcherInvalidOperator(t *testing.T) {
	_, err := newNodeSelectorMatcher(&corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{
		{MatchExpressions: []corev1.NodeSelectorRequirement{matchExpression("zone", "In", "a")}},
		{MatchExpressions: []corev1.NodeSelectorRequirement{matchExpression("zone", "Nonsense", "a")}},
	}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nodeSelectorTerms[1]")
}

func TestNodeSelectorMatcherInvalidMatchFields(t *testing.T) {
	for _, req := range []corev1.NodeSelectorRequirement{
		matchExpression("spec.unschedulable", corev1.NodeSelectorOpNotIn, "true"),
		{Key: "metadata.name", Operator: corev1.NodeSelectorOpExists},
		matchExpression("metadata.name", corev1.NodeSelectorOpIn, "node-a", "node-b"),
	} {
		_, err := newNodeSelectorMatcher(&corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{
			{MatchFields: []corev1.NodeSelectorRequirement{req}},
		}})
		assert.Error(t, err, "%+v", req)
	}
}

// TestReconcileCreateInvalidNodeSelector checks that a ClusterCIDR with an
// invalid nodeSelector is not requeued, since the nodeSelector is immutable.
func TestReconcileCreateInvalidNodeSelector(t *testing.T) {
	_, ctx := ktesting.NewTestContext(t)
	_, cccController := newController(ctx)
	recorder := record.NewFakeRecorder(1)
	cccController.recorder = recorder

	testCCC := makeClusterCIDR("invalid-selector", "10.1.0.0/16", "", 8, &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{
		{MatchFields: []corev1.NodeSelectorRequirement{matchExpression("spec.unschedulable", corev1.NodeSelectorOpNotIn, "true")}},
	}})
	cccController.clusterCIDRStore.Add(testCCC)
	require.NoError(t, cccController.syncClusterCIDR(ctx, testCCC.Name))

	assert.NotContains(t, cccController.cidrMap, testCCC.Name)
	require.Len(t, recorder.Events, 1)
	assert.Contains(t, <-recorder.Events, "InvalidClusterCIDR")
}

func makeTestEntry(t *testing.T, name, cidr string, nodeSelector *corev1.NodeSelector) *clusterCIDREntry {
	t.Helper()

	_, ipnet, err := utilnet.ParseCIDRSloppy(cidr)
	require.NoError(t, err)
	cidrSet, err := multicidrset.NewMultiCIDRSet(name, ipnet, 8)
	require.NoError(t, err)
	selector, err := newNodeSelectorMatcher(nodeSelector)
	require.NoError(t, err)

	return &clusterCIDREntry{
		clusterCIDR: &multicidrset.ClusterCIDR{
			Name:            name,
			IPv4CIDRSet:     cidrSet,
			AssociatedNodes: make(map[string]bool),
		},
		selector: selector,
	}
}

func makeTestEntries(t *testing.T, entries ...*clusterCIDREntry) map[string]*clusterCIDREntry {
	t.Helper()

	cidrMap := make(map[string]*clusterCIDREntry)
	for _, entry := range entries {
		cidrMap[entry.clusterCIDR.Name] = entry
	}
	return cidrMap
}

func matchedClusterCIDRNames(node *corev1.Node, allocate bool, cidrMap map[string]*clusterCIDREntry) []string {
	r := &multiCIDRRangeAllocator{}

	names := make([]string, 0)
	for _, clusterCIDR := range r.orderedMatchingClusterCIDRs(node, allocate, cidrMap) {
		names = append(names, clusterCIDR.Name)
	}
	return names
}

// TestOrderedMatchingClusterCIDRsNodeSelectors checks that a ClusterCIDR whose
// nodeSelector needs term structure (ORed terms, matchFields) is selected and
// ranked, and that ClusterCIDRs without a nodeSelector come last. The CIDRs are
// all the same size, so P0 decides the order among the selecting ClusterCIDRs.
func TestOrderedMatchingClusterCIDRsNodeSelectors(t *testing.T) {
	andTwo := &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{
		{MatchExpressions: []corev1.NodeSelectorRequirement{
			matchExpression("zone", corev1.NodeSelectorOpIn, "a"),
			matchExpression("rack", corev1.NodeSelectorOpIn, "r1"),
		}},
	}}
	orOne := &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{
		{MatchExpressions: []corev1.NodeSelectorRequirement{matchExpression("zone", corev1.NodeSelectorOpIn, "b")}},
		{MatchExpressions: []corev1.NodeSelectorRequirement{matchExpression("rack", corev1.NodeSelectorOpIn, "r1")}},
	}}
	noMatch := &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{
		{MatchExpressions: []corev1.NodeSelectorRequirement{matchExpression("zone", corev1.NodeSelectorOpIn, "b")}},
	}}

	cidrMap := makeTestEntries(t,
		// match-all has the smallest IP, so it would rank first if the
		// catch-all were ordered with the selecting ClusterCIDRs.
		makeTestEntry(t, "match-all", "10.0.0.0/16", nil),
		makeTestEntry(t, "and-two-reqs", "10.1.0.0/16", andTwo),
		makeTestEntry(t, "or-one-req", "10.2.0.0/16", orOne),
		makeTestEntry(t, "no-match", "10.3.0.0/16", noMatch),
		makeTestEntry(t, "no-terms", "10.4.0.0/16", &corev1.NodeSelector{}),
	)

	node := testNode("node1", map[string]string{"zone": "a", "rack": "r1"})
	assert.Equal(t, []string{"and-two-reqs", "or-one-req", "match-all"}, matchedClusterCIDRNames(node, true, cidrMap))

	// A node that satisfies neither labelled selector still gets the nil-selector pool.
	other := testNode("node2", map[string]string{"zone": "c"})
	assert.Equal(t, []string{"match-all"}, matchedClusterCIDRNames(other, true, cidrMap))
}

// TestOrderedMatchingClusterCIDRsMatchFields checks that matchFields selects on
// the node name rather than on a node label of the same name.
func TestOrderedMatchingClusterCIDRsMatchFields(t *testing.T) {
	byName := &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{
		{MatchFields: []corev1.NodeSelectorRequirement{
			matchExpression("metadata.name", corev1.NodeSelectorOpIn, "node1"),
		}},
	}}
	cidrMap := makeTestEntries(t, makeTestEntry(t, "by-node-name", "10.0.0.0/16", byName))

	assert.Equal(t, []string{"by-node-name"}, matchedClusterCIDRNames(testNode("node1", nil), true, cidrMap))
	assert.Empty(t, matchedClusterCIDRNames(testNode("node2", map[string]string{"metadata.name": "node1"}), true, cidrMap))
}

// TestOrderedMatchingClusterCIDRsTieBreak checks that equal ClusterCIDRs are
// ordered by IP address, then by name, in both the selecting and the
// catch-all groups.
func TestOrderedMatchingClusterCIDRsTieBreak(t *testing.T) {
	zoneA := &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{
		{MatchExpressions: []corev1.NodeSelectorRequirement{matchExpression("zone", corev1.NodeSelectorOpIn, "a")}},
	}}
	cidrMap := makeTestEntries(t,
		makeTestEntry(t, "b-high-ip", "10.9.0.0/16", zoneA),
		makeTestEntry(t, "c-low-ip", "10.1.0.0/16", zoneA),
		makeTestEntry(t, "a-low-ip", "10.1.0.0/16", zoneA),
		makeTestEntry(t, "y-catch-all", "10.5.0.0/16", nil),
		makeTestEntry(t, "x-catch-all", "10.5.0.0/16", nil),
	)

	node := testNode("node1", map[string]string{"zone": "a"})
	for range 10 {
		assert.Equal(t,
			[]string{"a-low-ip", "c-low-ip", "b-high-ip", "x-catch-all", "y-catch-all"},
			matchedClusterCIDRNames(node, true, cidrMap))
	}
}

// TestOrderedMatchingClusterCIDRsTerminating checks that a terminating
// ClusterCIDR, with or without a nodeSelector, is skipped for new allocations
// only.
func TestOrderedMatchingClusterCIDRsTerminating(t *testing.T) {
	zoneA := &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{
		{MatchExpressions: []corev1.NodeSelectorRequirement{matchExpression("zone", corev1.NodeSelectorOpIn, "a")}},
	}}
	selecting := makeTestEntry(t, "selecting", "10.1.0.0/16", zoneA)
	selecting.clusterCIDR.Terminating = true
	catchAll := makeTestEntry(t, "catch-all", "10.2.0.0/16", nil)
	catchAll.clusterCIDR.Terminating = true
	cidrMap := makeTestEntries(t, selecting, catchAll)

	node := testNode("node1", map[string]string{"zone": "a"})
	assert.Empty(t, matchedClusterCIDRNames(node, true, cidrMap))
	assert.Equal(t, []string{"selecting", "catch-all"}, matchedClusterCIDRNames(node, false, cidrMap))
}

// TestOccupyCIDRsTerminatingCatchAll checks that a node keeps its CIDRs from a
// terminating ClusterCIDR without a nodeSelector on every sync.
func TestOccupyCIDRsTerminatingCatchAll(t *testing.T) {
	logger, _ := ktesting.NewTestContext(t)
	catchAll := makeTestEntry(t, "catch-all", "10.2.0.0/16", nil)
	catchAll.clusterCIDR.Terminating = true
	ra := &multiCIDRRangeAllocator{cidrMap: makeTestEntries(t, catchAll)}

	node := testNode("node1", nil)
	node.Spec.PodCIDRs = []string{"10.2.3.0/24"}
	require.NoError(t, ra.occupyCIDRs(logger, node, ra.cidrMap))
	require.NoError(t, ra.occupyCIDRs(logger, node, ra.cidrMap))
	assert.True(t, catchAll.clusterCIDR.AssociatedNodes[node.Name])
}

// TestOccupyCIDRsOutsideMatchingClusterCIDRs covers a node whose Pod CIDR was
// allocated from a ClusterCIDR that no longer selects it, after a relabel or an
// upgrade that changed how the nodeSelector matches. The CIDR must stay
// reserved, so that it is not handed to another node, and must be released
// when the node goes away.
func TestOccupyCIDRsOutsideMatchingClusterCIDRs(t *testing.T) {
	logger, _ := ktesting.NewTestContext(t)
	zoneA := &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{
		{MatchExpressions: []corev1.NodeSelectorRequirement{matchExpression("zone", corev1.NodeSelectorOpIn, "a")}},
	}}
	zoneB := &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{
		{MatchExpressions: []corev1.NodeSelectorRequirement{matchExpression("zone", corev1.NodeSelectorOpIn, "b")}},
	}}
	// The two ClusterCIDRs overlap, so a CIDR left unreserved in zone-a could
	// be allocated again from zone-b.
	zoneAEntry := makeTestEntry(t, "zone-a", "10.0.0.0/16", zoneA)
	zoneBEntry := makeTestEntry(t, "zone-b", "10.0.0.0/16", zoneB)
	ra := &multiCIDRRangeAllocator{
		lock:    &sync.Mutex{},
		cidrMap: makeTestEntries(t, zoneAEntry, zoneBEntry),
	}

	node := testNode("node1", map[string]string{"zone": "c"})
	node.Spec.PodCIDRs = []string{"10.0.0.0/24"}
	require.NoError(t, ra.occupyCIDRs(logger, node, ra.cidrMap))

	_, podCIDR, err := utilnet.ParseCIDRSloppy("10.0.0.0/24")
	require.NoError(t, err)
	assert.True(t, ra.cidrInAllocatedList(logger, podCIDR, ra.cidrMap), "the node CIDR is not reserved")

	require.NoError(t, ra.ReleaseCIDR(logger, node))
	assert.False(t, ra.cidrInAllocatedList(logger, podCIDR, ra.cidrMap), "the node CIDR was not released")
	assert.False(t, zoneAEntry.clusterCIDR.AssociatedNodes[node.Name])
	assert.False(t, zoneBEntry.clusterCIDR.AssociatedNodes[node.Name])
}
