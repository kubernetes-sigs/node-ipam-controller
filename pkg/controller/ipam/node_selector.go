/*
Copyright The Kubernetes Authors.

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
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/component-helpers/scheduling/corev1/nodeaffinity"
)

// nodeSelector is a parsed corev1.NodeSelector. It matches nodes with the
// semantics documented on the API type: the node selector terms are ORed, the
// matchExpressions and matchFields requirements within a single term are ANDed,
// and an empty term matches no nodes. matchExpressions are matched against the
// node labels, matchFields against the node fields.
//
// Every term is parsed into its own nodeaffinity.NodeSelector so that terms can
// be matched individually, which the allocator needs to rank the ClusterCIDRs
// matching a node.
type nodeSelector struct {
	terms []nodeSelectorTerm
}

// nodeSelectorTerm is a single parsed corev1.NodeSelectorTerm.
type nodeSelectorTerm struct {
	matcher *nodeaffinity.NodeSelector
	// size is the number of requirements in the term.
	size int
}

// parseNodeSelector parses ns into a nodeSelector. Empty terms are dropped as
// they match no nodes; a selector without terms therefore matches no nodes.
func parseNodeSelector(ns *corev1.NodeSelector) (*nodeSelector, error) {
	if ns == nil {
		return nil, fmt.Errorf("node selector cannot be nil")
	}

	// Parse the whole selector first, so that errors report the index of the
	// invalid term in ns. Parsing the terms one by one below then cannot fail.
	if _, err := nodeaffinity.NewNodeSelector(ns); err != nil {
		return nil, err
	}

	s := &nodeSelector{}
	for i := range ns.NodeSelectorTerms {
		term := ns.NodeSelectorTerms[i]
		if len(term.MatchExpressions) == 0 && len(term.MatchFields) == 0 {
			continue
		}

		// nodeaffinity.NewNodeSelector matches the terms it is given as a
		// disjunction, so a selector holding a single term matches that term.
		matcher, err := nodeaffinity.NewNodeSelector(&corev1.NodeSelector{
			NodeSelectorTerms: []corev1.NodeSelectorTerm{term},
		})
		if err != nil {
			return nil, err
		}

		s.terms = append(s.terms, nodeSelectorTerm{
			matcher: matcher,
			size:    len(term.MatchExpressions) + len(term.MatchFields),
		})
	}
	return s, nil
}

// match reports whether the node matches any of the selector terms, and the
// number of requirements of the largest term the node matches. The allocator
// uses that count to prefer the ClusterCIDR with the most specific match.
func (s *nodeSelector) match(node *corev1.Node) (bool, int) {
	best := -1
	for i := range s.terms {
		if s.terms[i].size > best && s.terms[i].matcher.Match(node) {
			best = s.terms[i].size
		}
	}
	if best < 0 {
		return false, 0
	}
	return true, best
}
