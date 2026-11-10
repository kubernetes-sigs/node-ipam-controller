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
	"net"
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
	"k8s.io/client-go/kubernetes/scheme"
	corelisters "k8s.io/client-go/listers/core/v1"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/tools/reference"
	"k8s.io/client-go/util/workqueue"
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

// getTestNodeSelector builds a NodeSelector ANDing all the requirements in a
// single term, and returns it serialized. The test CIDR maps are keyed by that
// serialization so that getTestCidrMap can recover the NodeSelector and parse
// it the same way the allocator does.
func getTestNodeSelector(requirements []testNodeSelectorRequirement) string {
	matchExpressions := make([]corev1.NodeSelectorRequirement, 0, len(requirements))
	for _, nsr := range requirements {
		matchExpressions = append(matchExpressions, corev1.NodeSelectorRequirement{
			Key:      nsr.key,
			Operator: nsr.operator,
			Values:   nsr.values,
		})
	}

	return encodeTestNodeSelector(&corev1.NodeSelector{
		NodeSelectorTerms: []corev1.NodeSelectorTerm{{MatchExpressions: matchExpressions}},
	})
}

// encodeTestNodeSelector serializes a NodeSelector into a key of the test CIDR
// maps, see getTestNodeSelector.
func encodeTestNodeSelector(ns *corev1.NodeSelector) string {
	encoded, err := json.Marshal(ns)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

// parseTestNodeSelector parses a node selector serialized by getTestNodeSelector.
func parseTestNodeSelector(encoded string) *nodeSelector {
	testNodeSelector := &corev1.NodeSelector{}
	if err := json.Unmarshal([]byte(encoded), testNodeSelector); err != nil {
		panic(err)
	}

	selector, err := parseNodeSelector(testNodeSelector)
	if err != nil {
		panic(err)
	}
	return selector
}

func getTestCidrMap(testClusterCIDRMap map[string][]*testClusterCIDR) map[string]*clusterCIDREntry {
	cidrMap := make(map[string]*clusterCIDREntry, 0)
	for encodedSelector, testClusterCIDRList := range testClusterCIDRMap {
		selector := parseTestNodeSelector(encodedSelector)
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

			if _, ok := cidrMap[clusterCIDR.Name]; ok {
				panic("duplicate test ClusterCIDR name " + clusterCIDR.Name)
			}
			cidrMap[clusterCIDR.Name] = &clusterCIDREntry{clusterCIDR: clusterCIDR, selector: selector}
		}
	}
	return cidrMap
}

// getClusterCIDRList returns the ClusterCIDRs selecting a node labeled with
// testLabel-0=nodeName.
func getClusterCIDRList(nodeName string, cidrMap map[string]*clusterCIDREntry) ([]*multicidrset.ClusterCIDR, error) {
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: nodeName, Labels: map[string]string{"testLabel-0": nodeName}}}

	var clusterCIDRList []*multicidrset.ClusterCIDR
	for _, entry := range cidrMap {
		if entry.selector == nil {
			continue
		}
		if selectorMatches, _ := entry.selector.match(node); selectorMatches {
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

// TestMultiCIDROccupyWithClusterCIDRNotHoldingAllPodCIDRs asserts that a node
// whose Pod CIDRs a higher ranked ClusterCIDR can't all hold is associated with
// the ClusterCIDR holding them, and nothing is left allocated in the other one.
func TestMultiCIDROccupyWithClusterCIDRNotHoldingAllPodCIDRs(t *testing.T) {
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
		{
			description: "original dual stack node, new dual stack ClusterCIDR containing only the IPv4 Pod CIDR",
			podCIDRs:    []string{"10.10.0.0/24", "ace:cab:deca::/120"},
			catchAll: &testClusterCIDR{
				name: "catch-all", perNodeHostBits: 8, ipv4CIDR: "10.10.0.0/16", ipv6CIDR: "ace:cab:deca::/112",
			},
			selecting: &testClusterCIDR{
				name: "dual-stack", perNodeHostBits: 8, ipv4CIDR: "10.0.0.0/8", ipv6CIDR: "fd00:10::/112",
			},
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
			cidrMap := getTestCidrMap(map[string][]*testClusterCIDR{
				testNodeSelector: {tc.selecting, tc.catchAll},
			})
			// A ClusterCIDR without node selector is a catch all one.
			cidrMap[tc.catchAll.name].selector = nil
			return cidrMap
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

		assertOccupiedInCatchAll := func(t *testing.T, cidrMap map[string]*clusterCIDREntry) {
			t.Helper()
			assert.True(t, cidrMap[tc.catchAll.name].clusterCIDR.AssociatedNodes[node.Name],
				"node is not associated with the catch all ClusterCIDR")
			selecting := cidrMap[tc.selecting.name].clusterCIDR
			for _, cidrSet := range []*multicidrset.MultiCIDRSet{selecting.IPv4CIDRSet, selecting.IPv6CIDRSet} {
				if cidrSet == nil {
					continue
				}
				for _, podCIDR := range tc.podCIDRs {
					_, cidr, err := utilnet.ParseCIDRSloppy(podCIDR)
					require.NoError(t, err)
					assert.False(t, cidrSet.CIDRAllocated(cidr), "%s is allocated in %s", podCIDR, selecting.Name)
				}
			}
		}

		t.Run(tc.description+", at startup", func(t *testing.T) {
			_, ctx := ktesting.NewTestContext(t)
			cidrMap := newCIDRMap()

			assert.NotPanics(t, func() {
				_, err := newAllocator(ctx, &corev1.NodeList{Items: []corev1.Node{*node}}, cidrMap)
				assert.NoError(t, err)
			})
			assertOccupiedInCatchAll(t, cidrMap)
		})

		t.Run(tc.description+", on node sync", func(t *testing.T) {
			logger, ctx := ktesting.NewTestContext(t)
			cidrMap := newCIDRMap()
			allocator, err := newAllocator(ctx, &corev1.NodeList{}, cidrMap)
			require.NoError(t, err)

			assert.NotPanics(t, func() {
				assert.NoError(t, allocator.AllocateOrOccupyCIDR(logger, node))
			})
			assertOccupiedInCatchAll(t, cidrMap)
		})
	}
}

// TestOccupyCIDRsInTerminatingClusterCIDR asserts that a node keeps its
// association with the terminating ClusterCIDR holding its Pod CIDRs, at
// startup and on node sync, so that the ClusterCIDR is not deleted while the
// node uses it.
func TestOccupyCIDRsInTerminatingClusterCIDR(t *testing.T) {
	groupA := selectorWithTerms(expressionsTerm(nodeSelectorRequirement("group", corev1.NodeSelectorOpIn, "a")))

	testCases := []struct {
		description string
		selector    *corev1.NodeSelector
		overlapping bool
	}{
		{
			description: "catch all ClusterCIDR",
		},
		{
			description: "ClusterCIDR selecting the node",
			selector:    groupA,
		},
		{
			description: "ClusterCIDR selecting the node, with an overlapping catch all ClusterCIDR",
			selector:    groupA,
			overlapping: true,
		},
	}

	for _, tc := range testCases {
		node := makeNode("node-1", map[string]string{"group": "a"})
		node.Spec.PodCIDRs = []string{"10.10.0.0/24"}

		newCIDRMap := func(t *testing.T) map[string]*clusterCIDREntry {
			terminating := makeTestClusterCIDREntry(t, "terminating", "10.10.0.0/16", 8, tc.selector)
			terminating.clusterCIDR.Terminating = true
			cidrMap := makeTestCIDRMap(terminating)
			if tc.overlapping {
				cidrMap["overlapping"] = makeTestClusterCIDREntry(t, "overlapping", "10.0.0.0/8", 8, nil)
			}
			return cidrMap
		}
		newAllocator := func(
			ctx context.Context, nodeList *corev1.NodeList, cidrMap map[string]*clusterCIDREntry,
		) (CIDRAllocator, error) {
			fakeNodeHandler := &test.FakeNodeHandler{
				Existing:  []*corev1.Node{node},
				Clientset: fake.NewClientset(),
			}
			fakeInformerFactory := clustercidrinformer.NewSharedInformerFactory(
				&clustercidrfake.Clientset{}, NoResyncPeriodFunc(),
			)
			return NewMultiCIDRRangeAllocator(
				ctx, fakeNodeHandler, clustercidrfake.NewSimpleClientset().NetworkingV1().ClusterCIDRs(),
				test.FakeNodeInformer(fakeNodeHandler), fakeInformerFactory.Networking().V1().ClusterCIDRs(),
				CIDRAllocatorParams{}, nodeList, cidrMap,
			)
		}
		assertAssociated := func(t *testing.T, cidrMap map[string]*clusterCIDREntry) {
			t.Helper()
			assert.True(t, cidrMap["terminating"].clusterCIDR.AssociatedNodes[node.Name],
				"node is not associated with the terminating ClusterCIDR")
			if tc.overlapping {
				assert.False(t, cidrMap["overlapping"].clusterCIDR.AssociatedNodes[node.Name],
					"node is associated with the overlapping ClusterCIDR")
			}
		}

		t.Run(tc.description+", at startup", func(t *testing.T) {
			_, ctx := ktesting.NewTestContext(t)
			cidrMap := newCIDRMap(t)

			_, err := newAllocator(ctx, &corev1.NodeList{Items: []corev1.Node{*node}}, cidrMap)
			require.NoError(t, err)
			assertAssociated(t, cidrMap)
		})

		t.Run(tc.description+", on node sync", func(t *testing.T) {
			logger, ctx := ktesting.NewTestContext(t)
			cidrMap := newCIDRMap(t)
			allocator, err := newAllocator(ctx, &corev1.NodeList{}, cidrMap)
			require.NoError(t, err)

			require.NoError(t, allocator.AllocateOrOccupyCIDR(logger, node))
			assertAssociated(t, cidrMap)
		})
	}
}

// TestOccupyCIDRsInClusterCIDRNotSelectingNode asserts that a node's Pod CIDRs
// are occupied in the ClusterCIDR whose range holds them even if its node
// selector doesn't select the node, at startup and on node sync, so that no
// other node gets them, and that one Warning event on the node reports it.
func TestOccupyCIDRsInClusterCIDRNotSelectingNode(t *testing.T) {
	testCases := []struct {
		description string
		selector    *corev1.NodeSelector
	}{
		{
			// Before the node selectors followed the core v1 semantics, matchFields
			// were label requirements, and NotIn matched every node.
			description: "matchFields excluding the node",
			selector: selectorWithTerms(fieldsTerm(
				nodeSelectorRequirement("metadata.name", corev1.NodeSelectorOpNotIn, "node-1"),
			)),
		},
		{
			// Before, an empty node selector term matched every node.
			description: "empty node selector term",
			selector:    selectorWithTerms(corev1.NodeSelectorTerm{}),
		},
		{
			description: "node labels changed after the allocation",
			selector: selectorWithTerms(expressionsTerm(
				nodeSelectorRequirement("zone", corev1.NodeSelectorOpIn, "a"),
			)),
		},
	}

	for _, tc := range testCases {
		node := makeNode("node-1", map[string]string{"zone": "b"})
		node.Spec.PodCIDRs = []string{"10.0.0.0/24"}
		newNode := makeNode("node-2", map[string]string{"pool": "new"})

		newCIDRMap := func(t *testing.T) map[string]*clusterCIDREntry {
			return makeTestCIDRMap(
				makeTestClusterCIDREntry(t, "original", "10.0.0.0/16", 8, tc.selector),
				// Overlaps the original range and selects the new node only.
				makeTestClusterCIDREntry(t, "other", "10.0.0.0/8", 8, selectorWithTerms(expressionsTerm(
					nodeSelectorRequirement("pool", corev1.NodeSelectorOpIn, "new"),
				))),
			)
		}
		newAllocator := func(
			t *testing.T, ctx context.Context, nodeList *corev1.NodeList, cidrMap map[string]*clusterCIDREntry,
		) *multiCIDRRangeAllocator {
			t.Helper()
			fakeNodeHandler := &test.FakeNodeHandler{
				Existing:  []*corev1.Node{node},
				Clientset: fake.NewClientset(),
			}
			fakeInformerFactory := clustercidrinformer.NewSharedInformerFactory(
				&clustercidrfake.Clientset{}, NoResyncPeriodFunc(),
			)
			allocator, err := NewMultiCIDRRangeAllocator(
				ctx, fakeNodeHandler, clustercidrfake.NewSimpleClientset().NetworkingV1().ClusterCIDRs(),
				test.FakeNodeInformer(fakeNodeHandler), fakeInformerFactory.Networking().V1().ClusterCIDRs(),
				CIDRAllocatorParams{}, nodeList, cidrMap,
			)
			require.NoError(t, err)
			return allocator.(*multiCIDRRangeAllocator)
		}
		assertOccupied := func(t *testing.T, ra *multiCIDRRangeAllocator) {
			t.Helper()
			logger, _ := ktesting.NewTestContext(t)
			require.NotContains(t, clusterCIDRNames(ra.orderedMatchingClusterCIDRs(node, false, ra.cidrMap)), "original")
			assertNodeOnlyIn(t, node, ra.cidrMap, "original")

			cidrs, _, err := ra.prioritizedCIDRs(logger, newNode, ra.cidrMap)
			require.NoError(t, err)
			_, podCIDR, err := utilnet.ParseCIDRSloppy(node.Spec.PodCIDRs[0])
			require.NoError(t, err)
			for _, cidr := range cidrs {
				assert.False(t, cidr.Contains(podCIDR.IP) || podCIDR.Contains(cidr.IP),
					"new node got %v, which overlaps Pod CIDR %v of node %s", cidr, podCIDR, node.Name)
			}
		}

		wantEvent := "Warning ClusterCIDRNotSelectingNode Pod CIDRs [10.0.0.0/24] are in ClusterCIDR original, whose node selector doesn't select the node"

		t.Run(tc.description+", at startup", func(t *testing.T) {
			logger, ctx := ktesting.NewTestContext(t)
			ra := newAllocator(t, ctx, &corev1.NodeList{Items: []corev1.Node{*node}}, newCIDRMap(t))
			assertOccupied(t, ra)

			// The event sink starts in Run, after the startup occupy; the event
			// must be kept until then, not dropped.
			recorder := record.NewFakeRecorder(10)
			ra.recorder = recorder
			ra.startEvents()
			require.NoError(t, ra.AllocateOrOccupyCIDR(logger, node))
			assert.Equal(t, []string{wantEvent}, drainEvents(recorder))
		})

		t.Run(tc.description+", on node sync", func(t *testing.T) {
			logger, ctx := ktesting.NewTestContext(t)
			ra := newAllocator(t, ctx, &corev1.NodeList{}, newCIDRMap(t))
			recorder := record.NewFakeRecorder(10)
			ra.recorder = recorder
			ra.startEvents()

			require.NoError(t, ra.AllocateOrOccupyCIDR(logger, node))
			assertOccupied(t, ra)
			// The node keeps the ClusterCIDR now, a later sync reports nothing.
			require.NoError(t, ra.AllocateOrOccupyCIDR(logger, node))
			assert.Equal(t, []string{wantEvent}, drainEvents(recorder))
		})
	}
}

// TestBootstrapClusterCIDRWithInvalidNodeSelector asserts that a ClusterCIDR
// whose node selector is rejected now, but was accepted before node selectors
// followed the core v1 semantics, is loaded at startup: the Pod CIDRs of its
// nodes are occupied in it, it is not deleted while they use them, and both
// are reported once Run sends events.
func TestBootstrapClusterCIDRWithInvalidNodeSelector(t *testing.T) {
	_, ctx := ktesting.NewTestContext(t)
	// Before, matchFields were label requirements, and DoesNotExist matched
	// every node.
	clusterCIDR := makeClusterCIDR("legacy", "10.1.0.0/16", "", 8, selectorWithTerms(fieldsTerm(
		nodeSelectorRequirement("metadata.name", corev1.NodeSelectorOpDoesNotExist),
	)))
	clusterCIDR.ResourceVersion = "1"
	clusterCIDR.Generation = 1
	clusterCIDR.Finalizers = []string{clusterCIDRFinalizer}
	node := makeNode("node-1", nil)
	node.Spec.PodCIDRs = []string{"10.1.0.0/24"}

	client := clustercidrfake.NewSimpleClientset(clusterCIDR)
	cccInformer := clustercidrinformer.NewSharedInformerFactory(client, NoResyncPeriodFunc()).Networking().V1().ClusterCIDRs()
	nodeClient := fake.NewClientset(node)
	nodeInformer := informers.NewSharedInformerFactory(nodeClient, NoResyncPeriodFunc()).Core().V1().Nodes()
	allocator, err := NewMultiCIDRRangeAllocator(
		ctx, nodeClient, client.NetworkingV1().ClusterCIDRs(), nodeInformer, cccInformer,
		CIDRAllocatorParams{}, &corev1.NodeList{Items: []corev1.Node{*node}}, nil,
	)
	require.NoError(t, err)
	ra := allocator.(*multiCIDRRangeAllocator)

	require.Contains(t, ra.cidrMap, "legacy", "ClusterCIDR with an invalid node selector not loaded")
	assertNodeOnlyIn(t, node, ra.cidrMap, "legacy")

	recorder := record.NewFakeRecorder(10)
	ra.recorder = recorder
	ra.startEvents()
	events := drainEvents(recorder)
	require.Len(t, events, 2)
	assert.Contains(t, events[0], "Warning InvalidNodeSelector Invalid node selector, the ClusterCIDR selects no node: ")
	assert.Equal(t, "Warning ClusterCIDRNotSelectingNode Pod CIDRs [10.1.0.0/24] are in ClusterCIDR legacy, whose node selector doesn't select the node", events[1])

	// Deleting the ClusterCIDR waits for the node.
	deleting := clusterCIDR.DeepCopy()
	now := metav1.Now()
	deleting.DeletionTimestamp = &now
	require.NoError(t, cccInformer.Informer().GetIndexer().Add(deleting))
	require.Error(t, ra.syncClusterCIDR(ctx, deleting.Name))
	updated, err := client.NetworkingV1().ClusterCIDRs().Get(ctx, clusterCIDR.Name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Contains(t, updated.Finalizers, clusterCIDRFinalizer)
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
			description: "Dualstack CIDRs, clusterCIDR with equal label count, allocatable pod CIDRs and allocatable IPs, IPv4 CIDRs, prioritize clusterCIDR with lower alphanumeric name",
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
							ipv4CIDR:        "10.0.0.0/16",
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
							key:      "testLabel-2",
							operator: corev1.NodeSelectorOpIn,
							values:   []string{"label2"},
						},
					}): {
						{
							name:            "dual-stack-cidr-2",
							perNodeHostBits: 8,
							ipv4CIDR:        "10.0.0.0/16",
							ipv6CIDR:        "abc:def:deca::/112",
						},
					},
				}),
			expectedAllocatedCIDR: map[int]string{
				0: "10.0.0.0/24",
				1: "ace:cab:deca::/120",
			},
		},
		{
			description: "Dualstack CIDRs, clusterCIDR with equal label count, allocatable pod CIDRs and allocatable IPs, prioritize clusterCIDR with smaller IP",
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
		{
			description: "Dualstack CIDRs, node selected by the second of two ORed node selector terms",
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
					encodeTestNodeSelector(selectorWithTerms(
						expressionsTerm(nodeSelectorRequirement("testLabel-0", corev1.NodeSelectorOpIn, "other-node")),
						expressionsTerm(nodeSelectorRequirement("testLabel-0", corev1.NodeSelectorOpIn, "node0")),
					)): {
						{
							name:            "ored-terms-cidr",
							perNodeHostBits: 8,
							ipv4CIDR:        "10.20.0.0/16",
							ipv6CIDR:        "ace:cab:deca::/112",
						},
					},
				}),
			expectedAllocatedCIDR: map[int]string{
				0: "10.20.0.0/24",
				1: "ace:cab:deca::/120",
			},
		},
		{
			description: "Single stack CIDR, node selected by its name through matchFields",
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
					encodeTestNodeSelector(selectorWithTerms(
						fieldsTerm(nodeSelectorRequirement("metadata.name", corev1.NodeSelectorOpIn, "node0")),
					)): {
						{
							name:            "by-name-cidr",
							perNodeHostBits: 8,
							ipv4CIDR:        "10.30.0.0/16",
						},
					},
				}),
			expectedAllocatedCIDR: map[int]string{
				0: "10.30.0.0/24",
			},
		},
		{
			description: "Single stack CIDR, node excluded by its name through matchFields gets a CIDR from the next matching clusterCIDR",
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
					// Has fewer allocatable CIDRs, so it would be preferred if it
					// selected the node.
					encodeTestNodeSelector(selectorWithTerms(
						fieldsTerm(nodeSelectorRequirement("metadata.name", corev1.NodeSelectorOpNotIn, "node0")),
					)): {
						{
							name:            "excluding-cidr",
							perNodeHostBits: 4,
							ipv4CIDR:        "10.40.0.0/24",
						},
					},
					getTestNodeSelector([]testNodeSelectorRequirement{
						{
							key:      "testLabel-0",
							operator: corev1.NodeSelectorOpIn,
							values:   []string{"node0"},
						},
					}): {
						{
							name:            "fallback-cidr",
							perNodeHostBits: 8,
							ipv4CIDR:        "10.50.0.0/16",
						},
					},
				}),
			expectedAllocatedCIDR: map[int]string{
				0: "10.50.0.0/24",
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
		{
			description: "fail, the only clusterCIDR has a node selector without terms, which selects no node",
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
					encodeTestNodeSelector(selectorWithTerms()): {
						{
							name:            "no-terms-cidr",
							perNodeHostBits: 8,
							ipv4CIDR:        "10.60.0.0/16",
						},
					},
				}),
			expectedAllocatedCIDR: map[int]string{
				0: "10.60.0.0/24",
			},
		},
		{
			description: "fail, the only clusterCIDR excludes the node by its name through matchFields",
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
					encodeTestNodeSelector(selectorWithTerms(
						fieldsTerm(nodeSelectorRequirement("metadata.name", corev1.NodeSelectorOpNotIn, "node0")),
					)): {
						{
							name:            "excluding-cidr",
							perNodeHostBits: 8,
							ipv4CIDR:        "10.70.0.0/16",
						},
					},
				}),
			expectedAllocatedCIDR: map[int]string{
				0: "10.70.0.0/24",
			},
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
		{
			description: "Correctly release preallocated CIDR of a clusterCIDR selecting the node through ORed node selector terms",
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
					encodeTestNodeSelector(selectorWithTerms(
						expressionsTerm(nodeSelectorRequirement("testLabel-0", corev1.NodeSelectorOpIn, "other-node")),
						expressionsTerm(nodeSelectorRequirement("testLabel-0", corev1.NodeSelectorOpIn, "node0")),
					)): {
						{
							name:            "ored-terms-cidr-release",
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
	testCIDRMap := make(map[string]*clusterCIDREntry, 0)

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
		obj  any
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
	for name, entry := range ra.cidrMap {
		assert.False(t, entry.clusterCIDR.AssociatedNodes[nodeName], "node is associated with ClusterCIDR %s", name)
		assert.False(t, entry.clusterCIDR.IPv4CIDRSet.CIDRAllocated(podCIDR), "CIDR %s is allocated in ClusterCIDR %s", cidr, name)
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

// makeTestClusterCIDREntry builds the cidrMap entry of an IPv4 ClusterCIDR
// selected by the given node selector, or of a catch all ClusterCIDR if ns is nil.
func makeTestClusterCIDREntry(t *testing.T, name, ipv4CIDR string, perNodeHostBits int, ns *corev1.NodeSelector) *clusterCIDREntry {
	t.Helper()

	var selector *nodeSelector
	if ns != nil {
		var err error
		selector, err = parseNodeSelector(ns)
		require.NoError(t, err)
	}

	_, cidr, err := utilnet.ParseCIDRSloppy(ipv4CIDR)
	require.NoError(t, err)
	cidrSet, err := multicidrset.NewMultiCIDRSet(name, cidr, perNodeHostBits)
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

func makeTestCIDRMap(entries ...*clusterCIDREntry) map[string]*clusterCIDREntry {
	cidrMap := make(map[string]*clusterCIDREntry, len(entries))
	for _, entry := range entries {
		cidrMap[entry.clusterCIDR.Name] = entry
	}
	return cidrMap
}

func clusterCIDRNames(clusterCIDRs []*multicidrset.ClusterCIDR) []string {
	names := make([]string, 0, len(clusterCIDRs))
	for _, clusterCIDR := range clusterCIDRs {
		names = append(names, clusterCIDR.Name)
	}
	return names
}

// TestOrderedMatchingClusterCIDRsNodeSelectorSemantics covers the node selector
// semantics the allocator has to honour: the terms are ORed, the requirements
// within a term are ANDed, matchFields are matched against the node fields, and
// an empty term matches no node.
// xref: https://github.com/kubernetes-sigs/node-ipam-controller/issues/36
func TestOrderedMatchingClusterCIDRsNodeSelectorSemantics(t *testing.T) {
	node := makeNode("node-1", map[string]string{"zone": "a"})

	testCases := []struct {
		description  string
		nodeSelector *corev1.NodeSelector
		wantMatch    bool
	}{
		{
			description: "node matches the first of two ORed terms",
			nodeSelector: selectorWithTerms(
				corev1.NodeSelectorTerm{MatchExpressions: []corev1.NodeSelectorRequirement{
					nodeSelectorRequirement("zone", corev1.NodeSelectorOpIn, "a"),
				}},
				corev1.NodeSelectorTerm{MatchExpressions: []corev1.NodeSelectorRequirement{
					nodeSelectorRequirement("zone", corev1.NodeSelectorOpIn, "b"),
				}},
			),
			wantMatch: true,
		},
		{
			description: "node matches the second of two ORed terms",
			nodeSelector: selectorWithTerms(
				corev1.NodeSelectorTerm{MatchExpressions: []corev1.NodeSelectorRequirement{
					nodeSelectorRequirement("zone", corev1.NodeSelectorOpIn, "b"),
				}},
				corev1.NodeSelectorTerm{MatchExpressions: []corev1.NodeSelectorRequirement{
					nodeSelectorRequirement("zone", corev1.NodeSelectorOpIn, "a"),
				}},
			),
			wantMatch: true,
		},
		{
			description: "node matches neither of two ORed terms",
			nodeSelector: selectorWithTerms(
				corev1.NodeSelectorTerm{MatchExpressions: []corev1.NodeSelectorRequirement{
					nodeSelectorRequirement("zone", corev1.NodeSelectorOpIn, "b"),
				}},
				corev1.NodeSelectorTerm{MatchExpressions: []corev1.NodeSelectorRequirement{
					nodeSelectorRequirement("zone", corev1.NodeSelectorOpIn, "c"),
				}},
			),
			wantMatch: false,
		},
		{
			description: "node matches only one requirement of an ANDed term",
			nodeSelector: selectorWithTerms(corev1.NodeSelectorTerm{
				MatchExpressions: []corev1.NodeSelectorRequirement{
					nodeSelectorRequirement("zone", corev1.NodeSelectorOpIn, "a"),
					nodeSelectorRequirement("rack", corev1.NodeSelectorOpIn, "1"),
				},
			}),
			wantMatch: false,
		},
		{
			description: "node is selected by its name through matchFields",
			nodeSelector: selectorWithTerms(corev1.NodeSelectorTerm{
				MatchFields: []corev1.NodeSelectorRequirement{
					nodeSelectorRequirement("metadata.name", corev1.NodeSelectorOpIn, "node-1"),
				},
			}),
			wantMatch: true,
		},
		{
			description: "another node name in matchFields does not select the node",
			nodeSelector: selectorWithTerms(corev1.NodeSelectorTerm{
				MatchFields: []corev1.NodeSelectorRequirement{
					nodeSelectorRequirement("metadata.name", corev1.NodeSelectorOpIn, "node-2"),
				},
			}),
			wantMatch: false,
		},
		{
			description:  "a node selector without terms selects no node",
			nodeSelector: selectorWithTerms(),
			wantMatch:    false,
		},
		{
			description:  "an empty term selects no node",
			nodeSelector: selectorWithTerms(corev1.NodeSelectorTerm{}),
			wantMatch:    false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			ra := &multiCIDRRangeAllocator{}
			cidrMap := makeTestCIDRMap(makeTestClusterCIDREntry(t, "test-cc", "10.0.0.0/16", 8, tc.nodeSelector))

			matching := ra.orderedMatchingClusterCIDRs(node, true, cidrMap)

			if !tc.wantMatch {
				assert.Empty(t, matching)
				return
			}
			assert.Equal(t, []string{"test-cc"}, clusterCIDRNames(matching))
		})
	}
}

// TestOrderedMatchingClusterCIDRsPriority asserts that the ClusterCIDR matching
// the node with the most requirements is preferred, counting the requirements of
// the matching term only.
func TestOrderedMatchingClusterCIDRsPriority(t *testing.T) {
	node := makeNode("node-1", map[string]string{"zone": "a", "rack": "1"})

	cidrMap := makeTestCIDRMap(
		// Matches the node with a single requirement.
		makeTestClusterCIDREntry(t, "loose-cc", "10.0.0.0/16", 8, selectorWithTerms(
			corev1.NodeSelectorTerm{MatchExpressions: []corev1.NodeSelectorRequirement{
				nodeSelectorRequirement("zone", corev1.NodeSelectorOpIn, "a"),
			}},
		)),
		// Matches the node with two requirements through its second term. The
		// first term has more requirements but does not match, so it must not be
		// counted.
		makeTestClusterCIDREntry(t, "tight-cc", "10.1.0.0/16", 8, selectorWithTerms(
			corev1.NodeSelectorTerm{MatchExpressions: []corev1.NodeSelectorRequirement{
				nodeSelectorRequirement("zone", corev1.NodeSelectorOpIn, "b"),
				nodeSelectorRequirement("rack", corev1.NodeSelectorOpIn, "1"),
				nodeSelectorRequirement("role", corev1.NodeSelectorOpExists),
			}},
			corev1.NodeSelectorTerm{MatchExpressions: []corev1.NodeSelectorRequirement{
				nodeSelectorRequirement("zone", corev1.NodeSelectorOpIn, "a"),
				nodeSelectorRequirement("rack", corev1.NodeSelectorOpIn, "1"),
			}},
		)),
	)

	ra := &multiCIDRRangeAllocator{}
	matching := ra.orderedMatchingClusterCIDRs(node, true, cidrMap)
	assert.Equal(t, []string{"tight-cc", "loose-cc"}, clusterCIDRNames(matching),
		"the ClusterCIDR matching more requirements must come first")
}

// TestOrderedMatchingClusterCIDRsCatchAll covers the ClusterCIDRs without a node
// selector: they apply to every node, but only after the selecting ones.
func TestOrderedMatchingClusterCIDRsCatchAll(t *testing.T) {
	node := makeNode("node-1", map[string]string{"zone": "a"})
	zoneA := selectorWithTerms(corev1.NodeSelectorTerm{MatchExpressions: []corev1.NodeSelectorRequirement{
		nodeSelectorRequirement("zone", corev1.NodeSelectorOpIn, "a"),
	}})

	newCIDRMap := func(t *testing.T) map[string]*clusterCIDREntry {
		return makeTestCIDRMap(
			// The selecting ClusterCIDR has more allocatable CIDRs than the catch
			// all ones, it must still come first.
			makeTestClusterCIDREntry(t, "zone-a-cc", "10.0.0.0/16", 8, zoneA),
			// Catch all ClusterCIDRs of the same size are ordered by IP address.
			makeTestClusterCIDREntry(t, "catch-all-b", "10.1.0.0/24", 4, nil),
			makeTestClusterCIDREntry(t, "catch-all-a", "10.2.0.0/24", 4, nil),
			// Fewer allocatable CIDRs, so it comes first among the catch all ones.
			makeTestClusterCIDREntry(t, "catch-all-small", "10.3.0.0/26", 4, nil),
		)
	}

	t.Run("catch all ClusterCIDRs come last and in priority order", func(t *testing.T) {
		ra := &multiCIDRRangeAllocator{}
		matching := ra.orderedMatchingClusterCIDRs(node, true, newCIDRMap(t))
		assert.Equal(t, []string{"zone-a-cc", "catch-all-small", "catch-all-b", "catch-all-a"}, clusterCIDRNames(matching))
	})

	t.Run("catch all ClusterCIDRs apply to nodes no ClusterCIDR selects", func(t *testing.T) {
		ra := &multiCIDRRangeAllocator{}
		matching := ra.orderedMatchingClusterCIDRs(makeNode("node-2", nil), true, newCIDRMap(t))
		assert.Equal(t, []string{"catch-all-small", "catch-all-b", "catch-all-a"}, clusterCIDRNames(matching))
	})

	t.Run("a terminating catch all ClusterCIDR is skipped for new allocations only", func(t *testing.T) {
		ra := &multiCIDRRangeAllocator{}
		cidrMap := newCIDRMap(t)
		cidrMap["catch-all-b"].clusterCIDR.Terminating = true

		matching := ra.orderedMatchingClusterCIDRs(node, true, cidrMap)
		assert.Equal(t, []string{"zone-a-cc", "catch-all-small", "catch-all-a"}, clusterCIDRNames(matching))

		matching = ra.orderedMatchingClusterCIDRs(node, false, cidrMap)
		assert.Equal(t, []string{"zone-a-cc", "catch-all-small", "catch-all-b", "catch-all-a"}, clusterCIDRNames(matching))
	})

	t.Run("a node carrying the former internal default label lists every catch all ClusterCIDR once", func(t *testing.T) {
		ra := &multiCIDRRangeAllocator{}
		labeled := makeNode("node-3", map[string]string{"kubernetes.io/clusterCIDR": "default"})
		matching := ra.orderedMatchingClusterCIDRs(labeled, true, newCIDRMap(t))
		assert.Equal(t, []string{"catch-all-small", "catch-all-b", "catch-all-a"}, clusterCIDRNames(matching))
	})

	t.Run("a node selector on the former internal default label is an ordinary selector", func(t *testing.T) {
		ra := &multiCIDRRangeAllocator{}
		cidrMap := makeTestCIDRMap(
			makeTestClusterCIDREntry(t, "catch-all", "10.1.0.0/24", 4, nil),
			makeTestClusterCIDREntry(t, "labelled-default", "10.2.0.0/24", 4, selectorWithTerms(corev1.NodeSelectorTerm{
				MatchExpressions: []corev1.NodeSelectorRequirement{
					nodeSelectorRequirement("kubernetes.io/clusterCIDR", corev1.NodeSelectorOpIn, "default"),
				},
			})),
		)
		matching := ra.orderedMatchingClusterCIDRs(node, true, cidrMap)
		assert.Equal(t, []string{"catch-all"}, clusterCIDRNames(matching))
	})
}

// TestNonMatchingClusterCIDRs asserts that the ClusterCIDRs whose node selector
// doesn't select the node are listed in priority order, terminating ones
// included, without the catch all ones and the ones selecting the node.
func TestNonMatchingClusterCIDRs(t *testing.T) {
	node := makeNode("node-1", map[string]string{"zone": "a"})
	zoneSelector := func(zone string) *corev1.NodeSelector {
		return selectorWithTerms(expressionsTerm(nodeSelectorRequirement("zone", corev1.NodeSelectorOpIn, zone)))
	}

	terminating := makeTestClusterCIDREntry(t, "zone-c-terminating", "10.4.0.0/24", 4, zoneSelector("c"))
	terminating.clusterCIDR.Terminating = true
	cidrMap := makeTestCIDRMap(
		makeTestClusterCIDREntry(t, "zone-a", "10.0.0.0/24", 4, zoneSelector("a")),
		makeTestClusterCIDREntry(t, "catch-all", "10.1.0.0/24", 4, nil),
		makeTestClusterCIDREntry(t, "zone-b", "10.2.0.0/24", 4, zoneSelector("b")),
		// Fewer allocatable CIDRs, so it comes first.
		makeTestClusterCIDREntry(t, "zone-b-small", "10.3.0.0/26", 4, zoneSelector("b")),
		terminating,
		// An empty term selects no node.
		makeTestClusterCIDREntry(t, "empty-term", "10.5.0.0/24", 4, selectorWithTerms(corev1.NodeSelectorTerm{})),
	)

	assert.Equal(t,
		[]string{"zone-b-small", "zone-b", "zone-c-terminating", "empty-term"},
		clusterCIDRNames(nonMatchingClusterCIDRs(node, cidrMap)))
}

// TestMapAndDeleteClusterCIDRSet asserts that a ClusterCIDR is mapped once under
// its name, with its parsed node selector, and unmapped again.
func TestMapAndDeleteClusterCIDRSet(t *testing.T) {
	testCases := []struct {
		description  string
		nodeSelector *corev1.NodeSelector
	}{
		{
			description:  "no node selector, the ClusterCIDR is a catch all one",
			nodeSelector: nil,
		},
		{
			description: "single term",
			nodeSelector: selectorWithTerms(corev1.NodeSelectorTerm{
				MatchExpressions: []corev1.NodeSelectorRequirement{
					nodeSelectorRequirement("zone", corev1.NodeSelectorOpIn, "a"),
				},
			}),
		},
		{
			description: "ORed terms",
			nodeSelector: selectorWithTerms(
				corev1.NodeSelectorTerm{MatchExpressions: []corev1.NodeSelectorRequirement{
					nodeSelectorRequirement("zone", corev1.NodeSelectorOpIn, "a"),
				}},
				corev1.NodeSelectorTerm{MatchExpressions: []corev1.NodeSelectorRequirement{
					nodeSelectorRequirement("zone", corev1.NodeSelectorOpIn, "b"),
				}},
			),
		},
		{
			description: "matchFields",
			nodeSelector: selectorWithTerms(corev1.NodeSelectorTerm{
				MatchFields: []corev1.NodeSelectorRequirement{
					nodeSelectorRequirement("metadata.name", corev1.NodeSelectorOpIn, "node-1"),
				},
			}),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			logger, _ := ktesting.NewTestContext(t)
			ra := &multiCIDRRangeAllocator{}
			clusterCIDR := &v1.ClusterCIDR{
				ObjectMeta: metav1.ObjectMeta{Name: "test-cc"},
				Spec:       v1.ClusterCIDRSpec{NodeSelector: tc.nodeSelector, PerNodeHostBits: 8, IPv4: "10.0.0.0/16"},
			}

			selector, err := ra.clusterCIDRNodeSelector(clusterCIDR)
			require.NoError(t, err)
			assert.Equal(t, tc.nodeSelector == nil, selector == nil, "only a ClusterCIDR without node selector is a catch all one")
			clusterCIDRSet, err := ra.createClusterCIDRSet(clusterCIDR, false)
			require.NoError(t, err)

			cidrMap := make(map[string]*clusterCIDREntry)
			require.NoError(t, ra.mapClusterCIDRSet(cidrMap, selector, clusterCIDRSet))
			require.Contains(t, cidrMap, "test-cc")
			assert.Same(t, clusterCIDRSet, cidrMap["test-cc"].clusterCIDR)
			assert.Same(t, selector, cidrMap["test-cc"].selector)

			// Mapping a ClusterCIDR with the same name again must not replace it.
			otherSet, err := ra.createClusterCIDRSet(clusterCIDR, false)
			require.NoError(t, err)
			require.NoError(t, ra.mapClusterCIDRSet(cidrMap, selector, otherSet))
			assert.Len(t, cidrMap, 1)
			assert.Same(t, clusterCIDRSet, cidrMap["test-cc"].clusterCIDR)

			require.NoError(t, ra.deleteClusterCIDR(logger, clusterCIDR, cidrMap))
			assert.Empty(t, cidrMap)
		})
	}
}

// TestClusterCIDRsSharingANodeSelector asserts that ClusterCIDRs with the same
// node selector are independent of each other.
func TestClusterCIDRsSharingANodeSelector(t *testing.T) {
	logger, _ := ktesting.NewTestContext(t)
	node := makeNode("node-1", map[string]string{"zone": "a"})
	zoneA := func() *corev1.NodeSelector {
		return selectorWithTerms(corev1.NodeSelectorTerm{MatchExpressions: []corev1.NodeSelectorRequirement{
			nodeSelectorRequirement("zone", corev1.NodeSelectorOpIn, "a"),
		}})
	}

	ra := &multiCIDRRangeAllocator{}
	cidrMap := makeTestCIDRMap(
		makeTestClusterCIDREntry(t, "zone-a-1", "10.0.0.0/24", 4, zoneA()),
		makeTestClusterCIDREntry(t, "zone-a-2", "10.1.0.0/24", 4, zoneA()),
	)

	matching := ra.orderedMatchingClusterCIDRs(node, true, cidrMap)
	assert.Equal(t, []string{"zone-a-1", "zone-a-2"}, clusterCIDRNames(matching))

	require.NoError(t, ra.deleteClusterCIDR(logger, &v1.ClusterCIDR{ObjectMeta: metav1.ObjectMeta{Name: "zone-a-1"}}, cidrMap))

	matching = ra.orderedMatchingClusterCIDRs(node, true, cidrMap)
	assert.Equal(t, []string{"zone-a-2"}, clusterCIDRNames(matching))
}

// TestSyncClusterCIDRDeleteWithUnparseableNodeSelector asserts that a ClusterCIDR
// whose node selector cannot be parsed, e.g. because it carries the finalizer of
// a controller version that accepted the node selector, can still be deleted.
func TestSyncClusterCIDRDeleteWithUnparseableNodeSelector(t *testing.T) {
	_, ctx := ktesting.NewTestContext(t)
	client, cccController := newController(ctx)

	// matchFields only support the In and NotIn operators.
	testCCC := makeClusterCIDR("legacy", "10.1.0.0/16", "", 8, selectorWithTerms(corev1.NodeSelectorTerm{
		MatchFields: []corev1.NodeSelectorRequirement{
			nodeSelectorRequirement("metadata.name", corev1.NodeSelectorOpExists),
		},
	}))
	testCCC.Finalizers = []string{clusterCIDRFinalizer}
	deletionTimestamp := metav1.Now()
	testCCC.DeletionTimestamp = &deletionTimestamp

	_, err := client.NetworkingV1().ClusterCIDRs().Create(ctx, testCCC, metav1.CreateOptions{})
	require.NoError(t, err)
	require.NoError(t, cccController.clusterCIDRStore.Add(testCCC))

	require.NoError(t, cccController.syncClusterCIDR(ctx, testCCC.Name))

	updated, err := client.NetworkingV1().ClusterCIDRs().Get(ctx, testCCC.Name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.NotContains(t, updated.Finalizers, clusterCIDRFinalizer)
}

// TestClusterCIDREventReference asserts that the event recorder can refer to a
// ClusterCIDR from the informer cache, which carries no kind. Without it, the
// recorder drops the events on ClusterCIDRs.
func TestClusterCIDREventReference(t *testing.T) {
	clusterCIDR := makeClusterCIDR("no-kind", "10.1.0.0/16", "", 8, nil)
	clusterCIDR.TypeMeta = metav1.TypeMeta{}

	ref, err := reference.GetReference(scheme.Scheme, clusterCIDR)
	require.NoError(t, err)
	assert.Equal(t, "ClusterCIDR", ref.Kind)
	assert.Equal(t, "networking.x-k8s.io/v1", ref.APIVersion)
}

// TestSyncClusterCIDRCreateWithUnparseableNodeSelector asserts that a ClusterCIDR
// whose node selector cannot be parsed is loaded as selecting no node and gets
// the finalizer. The sync is not retried, since the node selector is immutable,
// and the InvalidNodeSelector event is reported once.
func TestSyncClusterCIDRCreateWithUnparseableNodeSelector(t *testing.T) {
	_, ctx := ktesting.NewTestContext(t)
	client, cccController := newController(ctx)

	// matchFields only support the In and NotIn operators.
	testCCC := makeClusterCIDR("unparseable", "10.1.0.0/16", "", 8, selectorWithTerms(corev1.NodeSelectorTerm{
		MatchFields: []corev1.NodeSelectorRequirement{
			nodeSelectorRequirement("metadata.name", corev1.NodeSelectorOpExists),
		},
	}))
	// An object from the API server has a resource version, so the sync
	// updates it rather than creating it.
	testCCC.ResourceVersion = "1"
	_, err := client.NetworkingV1().ClusterCIDRs().Create(ctx, testCCC, metav1.CreateOptions{})
	require.NoError(t, err)
	require.NoError(t, cccController.clusterCIDRStore.Add(testCCC))
	recorder := record.NewFakeRecorder(10)
	cccController.recorder = recorder
	cccController.startEvents()

	// The node selector is immutable, retrying can't help: the ClusterCIDR is
	// loaded, selecting no node.
	require.NoError(t, cccController.syncClusterCIDR(ctx, testCCC.Name))
	// The finalizer update syncs it again; it reports the selector once.
	require.NoError(t, cccController.syncClusterCIDR(ctx, testCCC.Name))

	cccController.lock.Lock()
	require.Contains(t, cccController.cidrMap, testCCC.Name)
	entry := cccController.cidrMap[testCCC.Name]
	require.NotNil(t, entry.selector, "a ClusterCIDR with an invalid node selector is a catch all one")
	matches, _ := entry.selector.match(makeNode("node-1", map[string]string{"zone": "a"}))
	assert.False(t, matches, "a ClusterCIDR with an invalid node selector selects a node")
	cccController.lock.Unlock()

	created, err := client.NetworkingV1().ClusterCIDRs().Get(ctx, testCCC.Name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Contains(t, created.Finalizers, clusterCIDRFinalizer)

	events := drainEvents(recorder)
	require.Len(t, events, 1)
	assert.Contains(t, events[0], "Warning InvalidNodeSelector Invalid node selector, the ClusterCIDR selects no node: ")
	assert.Contains(t, events[0], "nodeSelectorTerms[0].matchFields[0].operator")
}

// TestNodeSelectorCannotAffectAnotherClusterCIDR asserts that a ClusterCIDR is
// always matched with its own node selector, whatever the other ClusterCIDRs.
func TestNodeSelectorCannotAffectAnotherClusterCIDR(t *testing.T) {
	ra := &multiCIDRRangeAllocator{}
	cidrMap := makeTestCIDRMap(
		// A field key that renders like the legit selector below once turned
		// into a field selector string.
		makeTestClusterCIDREntry(t, "a-crafted", "10.0.0.0/16", 8, selectorWithTerms(corev1.NodeSelectorTerm{
			MatchFields: []corev1.NodeSelectorRequirement{
				nodeSelectorRequirement("metadata.name=x] || fields[metadata.name", corev1.NodeSelectorOpIn, "y"),
			},
		})),
		makeTestClusterCIDREntry(t, "b-legit", "10.1.0.0/16", 8, selectorWithTerms(
			corev1.NodeSelectorTerm{MatchFields: []corev1.NodeSelectorRequirement{
				nodeSelectorRequirement("metadata.name", corev1.NodeSelectorOpIn, "x"),
			}},
			corev1.NodeSelectorTerm{MatchFields: []corev1.NodeSelectorRequirement{
				nodeSelectorRequirement("metadata.name", corev1.NodeSelectorOpIn, "y"),
			}},
		)),
	)

	matching := ra.orderedMatchingClusterCIDRs(makeNode("y", nil), true, cidrMap)
	assert.Equal(t, []string{"b-legit"}, clusterCIDRNames(matching))
}

// newReleaseTestAllocator returns an allocator with the given cidrMap, whose node
// lister reads from the returned store, which holds the node.
func newReleaseTestAllocator(t *testing.T, cidrMap map[string]*clusterCIDREntry, node *corev1.Node) (*multiCIDRRangeAllocator, cache.Store) {
	t.Helper()

	nodeStore := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
	require.NoError(t, nodeStore.Add(node))
	return &multiCIDRRangeAllocator{
		lock:       &sync.Mutex{},
		cidrMap:    cidrMap,
		nodeLister: corelisters.NewNodeLister(nodeStore),
	}, nodeStore
}

// assertNodeReleased asserts that no ClusterCIDR keeps the node associated or any
// of its Pod CIDRs allocated.
func assertNodeReleased(t *testing.T, node *corev1.Node, cidrMap map[string]*clusterCIDREntry) {
	t.Helper()

	for name, entry := range cidrMap {
		assert.False(t, entry.clusterCIDR.AssociatedNodes[node.Name], "node %s is still associated with ClusterCIDR %s", node.Name, name)
		for _, podCIDR := range node.Spec.PodCIDRs {
			_, cidr, err := utilnet.ParseCIDRSloppy(podCIDR)
			require.NoError(t, err)
			assert.False(t, entry.clusterCIDR.IPv4CIDRSet.CIDROverlaps(cidr), "Pod CIDR %s is still allocated in ClusterCIDR %s", podCIDR, name)
		}
	}
}

// assertNodeOnlyIn asserts that the node is associated with the named
// ClusterCIDR only, that its Pod CIDRs are allocated there, and that no other
// ClusterCIDR has them allocated.
func assertNodeOnlyIn(t *testing.T, node *corev1.Node, cidrMap map[string]*clusterCIDREntry, name string) {
	t.Helper()

	for entryName, entry := range cidrMap {
		assert.Equal(t, entryName == name, entry.clusterCIDR.AssociatedNodes[node.Name], "association of node %s with ClusterCIDR %s", node.Name, entryName)
		for _, podCIDR := range node.Spec.PodCIDRs {
			_, cidr, err := utilnet.ParseCIDRSloppy(podCIDR)
			require.NoError(t, err)
			assert.Equal(t, entryName == name, entry.clusterCIDR.IPv4CIDRSet.CIDROverlaps(cidr), "allocation of Pod CIDR %s in ClusterCIDR %s", podCIDR, entryName)
		}
	}
}

// TestReleaseCIDRAfterHigherPriorityClusterCIDRCreated asserts that a node keeps
// the ClusterCIDR its Pod CIDRs were allocated from when a ClusterCIDR that takes
// precedence for the node is created, and that releasing the node frees them.
func TestReleaseCIDRAfterHigherPriorityClusterCIDRCreated(t *testing.T) {
	zoneA := func() *corev1.NodeSelector {
		return selectorWithTerms(expressionsTerm(
			nodeSelectorRequirement("zone", corev1.NodeSelectorOpIn, "a"),
		))
	}
	zoneARack1 := func() *corev1.NodeSelector {
		return selectorWithTerms(expressionsTerm(
			nodeSelectorRequirement("zone", corev1.NodeSelectorOpIn, "a"),
			nodeSelectorRequirement("rack", corev1.NodeSelectorOpIn, "1"),
		))
	}

	testCases := []struct {
		description string
		newCIDR     string
		newSelector *corev1.NodeSelector
	}{
		{
			description: "disjoint ranges, more matching requirements",
			newCIDR:     "10.1.0.0/16",
			newSelector: zoneARack1(),
		},
		{
			description: "the new range contains the Pod CIDRs of the node, more matching requirements",
			newCIDR:     "10.0.0.0/8",
			newSelector: zoneARack1(),
		},
		{
			description: "the new range contains the Pod CIDRs of the node, same node selector, fewer allocatable CIDRs",
			newCIDR:     "10.0.0.0/20",
			newSelector: zoneA(),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			logger, _ := ktesting.NewTestContext(t)
			node := makeNode("node-1", map[string]string{"zone": "a", "rack": "1"})
			node.Spec.PodCIDRs = []string{"10.0.0.0/24"}

			original := makeTestClusterCIDREntry(t, "original", "10.0.0.0/16", 8, zoneA())
			ra, _ := newReleaseTestAllocator(t, makeTestCIDRMap(original), node)
			require.NoError(t, ra.AllocateOrOccupyCIDR(logger, node))
			require.True(t, original.clusterCIDR.AssociatedNodes[node.Name])

			newer := makeTestClusterCIDREntry(t, "newer", tc.newCIDR, 8, tc.newSelector)
			ra.cidrMap[newer.clusterCIDR.Name] = newer
			require.Equal(t, []string{"newer", "original"}, clusterCIDRNames(ra.orderedMatchingClusterCIDRs(node, false, ra.cidrMap)))

			// Any node update, e.g. a kubelet heartbeat, syncs the node again.
			require.NoError(t, ra.AllocateOrOccupyCIDR(logger, node))
			assertNodeOnlyIn(t, node, ra.cidrMap, "original")

			require.NoError(t, ra.ReleaseCIDR(logger, node))
			assertNodeReleased(t, node, ra.cidrMap)
		})
	}
}

// TestReleaseCIDRAfterNodeLabelsChanged asserts that a node keeps the ClusterCIDR
// its Pod CIDRs were allocated from after the node labels changed so that this
// ClusterCIDR does not select the node anymore, and that releasing the node frees
// them.
func TestReleaseCIDRAfterNodeLabelsChanged(t *testing.T) {
	zoneB := func() *corev1.NodeSelector {
		return selectorWithTerms(expressionsTerm(
			nodeSelectorRequirement("zone", corev1.NodeSelectorOpIn, "b"),
		))
	}

	testCases := []struct {
		description string
		// otherCIDR, if not empty, is the range of another ClusterCIDR, with
		// otherSelector as node selector.
		otherCIDR     string
		otherSelector *corev1.NodeSelector
	}{
		{
			description: "no ClusterCIDR selects the node anymore",
		},
		{
			description:   "another ClusterCIDR selects the node now",
			otherCIDR:     "10.1.0.0/16",
			otherSelector: zoneB(),
		},
		{
			description: "only a catch all ClusterCIDR applies to the node now",
			otherCIDR:   "10.2.0.0/16",
		},
		{
			description:   "another ClusterCIDR containing the Pod CIDRs of the node selects the node now",
			otherCIDR:     "10.0.0.0/8",
			otherSelector: zoneB(),
		},
		{
			description: "only a catch all ClusterCIDR containing the Pod CIDRs of the node applies to the node now",
			otherCIDR:   "10.0.0.0/8",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			logger, _ := ktesting.NewTestContext(t)
			node := makeNode("node-1", map[string]string{"zone": "a"})
			node.Spec.PodCIDRs = []string{"10.0.0.0/24"}

			original := makeTestClusterCIDREntry(t, "original", "10.0.0.0/16", 8, selectorWithTerms(expressionsTerm(
				nodeSelectorRequirement("zone", corev1.NodeSelectorOpIn, "a"),
			)))
			cidrMap := makeTestCIDRMap(original)
			if tc.otherCIDR != "" {
				cidrMap["other"] = makeTestClusterCIDREntry(t, "other", tc.otherCIDR, 8, tc.otherSelector)
			}
			ra, nodeStore := newReleaseTestAllocator(t, cidrMap, node)
			require.NoError(t, ra.AllocateOrOccupyCIDR(logger, node))
			require.True(t, original.clusterCIDR.AssociatedNodes[node.Name])

			// The node is relabeled, synced again, then deleted.
			node = node.DeepCopy()
			node.Labels = map[string]string{"zone": "b"}
			require.NoError(t, nodeStore.Update(node))
			require.NotContains(t, clusterCIDRNames(ra.orderedMatchingClusterCIDRs(node, false, ra.cidrMap)), "original")
			require.NoError(t, ra.AllocateOrOccupyCIDR(logger, node))
			assertNodeOnlyIn(t, node, ra.cidrMap, "original")

			require.NoError(t, ra.ReleaseCIDR(logger, node))
			assertNodeReleased(t, node, ra.cidrMap)
		})
	}
}

// TestOccupyCIDRsKeepsTerminatingAssociatedClusterCIDR asserts that a node keeps
// a terminating ClusterCIDR its Pod CIDRs were allocated from, rather than moving
// to an overlapping ClusterCIDR, so that the terminating one is not deleted while
// the node uses it.
func TestOccupyCIDRsKeepsTerminatingAssociatedClusterCIDR(t *testing.T) {
	logger, _ := ktesting.NewTestContext(t)
	zoneA := selectorWithTerms(expressionsTerm(
		nodeSelectorRequirement("zone", corev1.NodeSelectorOpIn, "a"),
	))
	node := makeNode("node-1", map[string]string{"zone": "a"})
	node.Spec.PodCIDRs = []string{"10.0.0.0/24"}

	original := makeTestClusterCIDREntry(t, "original", "10.0.0.0/16", 8, zoneA)
	// Ranks below the original one: more allocatable CIDRs.
	overlapping := makeTestClusterCIDREntry(t, "overlapping", "10.0.0.0/8", 8, zoneA)
	ra, _ := newReleaseTestAllocator(t, makeTestCIDRMap(original, overlapping), node)
	ra.cidrQueue = workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[string]())
	defer ra.cidrQueue.ShutDown()
	require.NoError(t, ra.AllocateOrOccupyCIDR(logger, node))
	assertNodeOnlyIn(t, node, ra.cidrMap, "original")

	original.clusterCIDR.Terminating = true
	require.NoError(t, ra.AllocateOrOccupyCIDR(logger, node))
	assertNodeOnlyIn(t, node, ra.cidrMap, "original")

	require.NoError(t, ra.ReleaseCIDR(logger, node))
	assertNodeReleased(t, node, ra.cidrMap)
	require.Equal(t, 1, ra.cidrQueue.Len(), "terminating ClusterCIDR not enqueued after its last node was released")
	key, _ := ra.cidrQueue.Get()
	defer ra.cidrQueue.Done(key)
	assert.Equal(t, "original", key)
}

// TestOccupyCIDRsWithAssociationOutsideItsRange asserts that an association
// whose ClusterCIDR can't hold the node's Pod CIDRs, left over from a deleted
// node with the same name, doesn't stop the Pod CIDRs from being occupied.
func TestOccupyCIDRsWithAssociationOutsideItsRange(t *testing.T) {
	logger, _ := ktesting.NewTestContext(t)
	node := makeNode("node-1", map[string]string{"zone": "a"})
	node.Spec.PodCIDRs = []string{"10.0.0.0/24"}

	original := makeTestClusterCIDREntry(t, "original", "10.0.0.0/16", 8, selectorWithTerms(expressionsTerm(
		nodeSelectorRequirement("zone", corev1.NodeSelectorOpIn, "a"),
	)))
	stale := makeTestClusterCIDREntry(t, "stale", "10.1.0.0/16", 8, selectorWithTerms(expressionsTerm(
		nodeSelectorRequirement("zone", corev1.NodeSelectorOpIn, "b"),
	)))
	stale.clusterCIDR.AssociatedNodes[node.Name] = true
	ra, _ := newReleaseTestAllocator(t, makeTestCIDRMap(original, stale), node)

	require.NoError(t, ra.AllocateOrOccupyCIDR(logger, node))

	assert.True(t, original.clusterCIDR.AssociatedNodes[node.Name], "node is not associated with ClusterCIDR original")
	_, podCIDR, err := utilnet.ParseCIDRSloppy(node.Spec.PodCIDRs[0])
	require.NoError(t, err)
	assert.True(t, original.clusterCIDR.IPv4CIDRSet.CIDRAllocated(podCIDR), "Pod CIDR is not allocated in ClusterCIDR original")
}

// drainEvents returns the events the recorder holds and closes it.
func drainEvents(recorder *record.FakeRecorder) []string {
	close(recorder.Events)
	var events []string
	for event := range recorder.Events {
		events = append(events, event)
	}
	return events
}

func TestOccupyCIDRsPrefersClusterCIDRHoldingPodCIDRsWhole(t *testing.T) {
	zoneA := selectorWithTerms(expressionsTerm(nodeSelectorRequirement("zone", corev1.NodeSelectorOpIn, "a")))
	zoneB := selectorWithTerms(expressionsTerm(nodeSelectorRequirement("zone", corev1.NodeSelectorOpIn, "b")))

	testCases := []struct {
		description string
		// other ranks above original for the node, but doesn't hold its Pod
		// CIDR whole.
		other    *clusterCIDREntry
		original *corev1.NodeSelector
		events   []string
	}{
		{
			description: "per-node blocks larger than the Pod CIDR",
			other:       makeTestClusterCIDREntry(t, "other", "10.0.0.0/16", 9, zoneA),
			original:    zoneA,
		},
		{
			description: "range smaller than the Pod CIDR",
			other:       makeTestClusterCIDREntry(t, "other", "10.0.0.0/25", 7, zoneA),
			original:    zoneA,
		},
		{
			description: "range smaller than the Pod CIDR, original not selecting the node",
			other:       makeTestClusterCIDREntry(t, "other", "10.0.0.0/25", 7, zoneA),
			original:    zoneB,
			events: []string{"Warning ClusterCIDRNotSelectingNode Pod CIDRs [10.0.0.0/24] are in ClusterCIDR original, " +
				"whose node selector doesn't select the node"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			logger, _ := ktesting.NewTestContext(t)
			node := makeNode("node-1", map[string]string{"zone": "a"})
			node.Spec.PodCIDRs = []string{"10.0.0.0/24"}
			original := makeTestClusterCIDREntry(t, "original", "10.0.0.0/16", 8, tc.original)
			cidrMap := makeTestCIDRMap(original, tc.other)
			ra, _ := newReleaseTestAllocator(t, cidrMap, node)
			recorder := record.NewFakeRecorder(10)
			ra.recorder = recorder
			ra.eventsStarted = true

			require.NoError(t, ra.AllocateOrOccupyCIDR(logger, node))

			assertNodeOnlyIn(t, node, cidrMap, "original")
			assert.Equal(t, tc.events, drainEvents(recorder))
		})
	}
}

func TestOccupyCIDRsInClusterCIDRNotHoldingPodCIDRsWhole(t *testing.T) {
	logger, _ := ktesting.NewTestContext(t)
	node := makeNode("node-1", map[string]string{"zone": "a"})
	node.Spec.PodCIDRs = []string{"10.0.0.0/24"}
	// No ClusterCIDR holds the Pod CIDR whole, e.g. after a migration from
	// another IPAM controller.
	small := makeTestClusterCIDREntry(t, "small", "10.0.0.0/25", 7, selectorWithTerms(expressionsTerm(
		nodeSelectorRequirement("zone", corev1.NodeSelectorOpIn, "a"),
	)))
	cidrMap := makeTestCIDRMap(small)
	ra, _ := newReleaseTestAllocator(t, cidrMap, node)
	recorder := record.NewFakeRecorder(10)
	ra.recorder = recorder
	ra.eventsStarted = true

	require.NoError(t, ra.AllocateOrOccupyCIDR(logger, node))

	assert.True(t, small.clusterCIDR.AssociatedNodes[node.Name], "node is not associated with ClusterCIDR small")
	_, rangeCIDR, err := utilnet.ParseCIDRSloppy("10.0.0.0/25")
	require.NoError(t, err)
	assert.True(t, small.clusterCIDR.IPv4CIDRSet.CIDRAllocated(rangeCIDR), "the part of the Pod CIDR in ClusterCIDR small is not reserved")
	assert.Equal(t, []string{"Warning ClusterCIDRNotHoldingPodCIDRs Pod CIDRs [10.0.0.0/24] are in ClusterCIDR small, " +
		"whose range doesn't contain them or whose per-node blocks are larger: " +
		"they may be reserved only in part, and another node can get an overlapping Pod CIDR"}, drainEvents(recorder))
}

func TestReleaseCIDRKeepsPerNodeBlockSharedWithAnotherNode(t *testing.T) {
	t.Skip("A per-node block holding the Pod CIDRs of two nodes is released with either of them")

	logger, _ := ktesting.NewTestContext(t)
	nodeA := makeNode("node-a", map[string]string{"zone": "a"})
	nodeA.Spec.PodCIDRs = []string{"10.0.0.0/24"}
	nodeB := makeNode("node-b", map[string]string{"zone": "a"})
	nodeB.Spec.PodCIDRs = []string{"10.0.1.0/24"}
	// No ClusterCIDR holds the Pod CIDRs whole: both are in the per-node block
	// 10.0.0.0/23.
	wide := makeTestClusterCIDREntry(t, "wide", "10.0.0.0/16", 9, selectorWithTerms(expressionsTerm(
		nodeSelectorRequirement("zone", corev1.NodeSelectorOpIn, "a"),
	)))
	ra, nodeStore := newReleaseTestAllocator(t, makeTestCIDRMap(wide), nodeA)
	require.NoError(t, nodeStore.Add(nodeB))
	ra.recorder = record.NewFakeRecorder(10)
	ra.eventsStarted = true
	require.NoError(t, ra.AllocateOrOccupyCIDR(logger, nodeA))
	require.NoError(t, ra.AllocateOrOccupyCIDR(logger, nodeB))

	require.NoError(t, nodeStore.Delete(nodeA))
	require.NoError(t, ra.ReleaseCIDR(logger, nodeA))

	_, podCIDR, err := utilnet.ParseCIDRSloppy(nodeB.Spec.PodCIDRs[0])
	require.NoError(t, err)
	assert.True(t, wide.clusterCIDR.IPv4CIDRSet.CIDROverlaps(podCIDR), "Pod CIDR of node-b is not reserved")
}

func TestBootstrapDeletingClusterCIDRIsTerminating(t *testing.T) {
	_, ctx := ktesting.NewTestContext(t)
	// A catch-all ClusterCIDR. The API server bumps the generation when it sets
	// the deletion timestamp; the controller must not rely on that.
	clusterCIDR := makeClusterCIDR("deleting", "10.1.0.0/16", "", 8, nil)
	clusterCIDR.ResourceVersion = "1"
	clusterCIDR.Generation = 1
	clusterCIDR.Finalizers = []string{clusterCIDRFinalizer}
	now := metav1.Now()
	clusterCIDR.DeletionTimestamp = &now
	node := makeNode("node-1", nil)
	node.Spec.PodCIDRs = []string{"10.1.0.0/24"}

	client := clustercidrfake.NewSimpleClientset(clusterCIDR)
	cccInformer := clustercidrinformer.NewSharedInformerFactory(client, NoResyncPeriodFunc()).Networking().V1().ClusterCIDRs()
	nodeClient := fake.NewClientset(node)
	nodeInformer := informers.NewSharedInformerFactory(nodeClient, NoResyncPeriodFunc()).Core().V1().Nodes()
	allocator, err := NewMultiCIDRRangeAllocator(
		ctx, nodeClient, client.NetworkingV1().ClusterCIDRs(), nodeInformer, cccInformer,
		CIDRAllocatorParams{}, &corev1.NodeList{Items: []corev1.Node{*node}}, nil,
	)
	require.NoError(t, err)
	ra := allocator.(*multiCIDRRangeAllocator)

	require.Contains(t, ra.cidrMap, "deleting")
	assert.True(t, ra.cidrMap["deleting"].clusterCIDR.Terminating, "ClusterCIDR being deleted is not terminating")
	// The node keeps its Pod CIDR, and a new node gets none from the ClusterCIDR.
	assertNodeOnlyIn(t, node, ra.cidrMap, "deleting")
	assert.Empty(t, ra.orderedMatchingClusterCIDRs(makeNode("node-2", nil), true, ra.cidrMap))
}
