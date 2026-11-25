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
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
)

// nodeSelector is a parsed corev1.NodeSelector. The requirements of all its
// terms, matchExpressions and matchFields alike, are ANDed into a single label
// selector that is matched against the node labels.
type nodeSelector struct {
	selector labels.Selector
}

// parseNodeSelector parses ns into a nodeSelector.
func parseNodeSelector(ns *corev1.NodeSelector) (*nodeSelector, error) {
	if ns == nil {
		return nil, errors.New("node selector cannot be nil")
	}

	selector, err := nodeSelectorAsSelector(ns)
	if err != nil {
		return nil, err
	}
	return &nodeSelector{selector: selector}, nil
}

// selectsNoNode returns a nodeSelector that selects no node.
func selectsNoNode() *nodeSelector {
	return &nodeSelector{selector: labels.Nothing()}
}

// match reports whether the node labels match all the selector requirements,
// and the number of requirements the node labels match. The allocator uses that
// count to prefer the ClusterCIDR with the most specific match.
func (s *nodeSelector) match(node *corev1.Node) (bool, int) {
	reqs, selectable := s.selector.Requirements()
	if !selectable {
		return false, 0
	}

	matchCnt := 0
	for _, req := range reqs {
		if req.Matches(labels.Set(node.Labels)) {
			matchCnt++
		}
	}
	return matchCnt == len(reqs), matchCnt
}

// nodeSelectorRequirementsAsLabelRequirements converts the NodeSelectorRequirement
// type to a labels.Requirement type.
func nodeSelectorRequirementsAsLabelRequirements(nsr corev1.NodeSelectorRequirement) (*labels.Requirement, error) {
	var op selection.Operator
	switch nsr.Operator {
	case corev1.NodeSelectorOpIn:
		op = selection.In
	case corev1.NodeSelectorOpNotIn:
		op = selection.NotIn
	case corev1.NodeSelectorOpExists:
		op = selection.Exists
	case corev1.NodeSelectorOpDoesNotExist:
		op = selection.DoesNotExist
	case corev1.NodeSelectorOpGt:
		op = selection.GreaterThan
	case corev1.NodeSelectorOpLt:
		op = selection.LessThan
	default:
		return nil, fmt.Errorf("%q is not a valid node selector operator", nsr.Operator)
	}
	return labels.NewRequirement(nsr.Key, op, nsr.Values)
}

// TODO: nodeSelect and labelSelector semantics are different and the function
// doesn't translate them correctly, this has to be fixed before Beta
// xref: https://issues.k8s.io/116419
// nodeSelectorAsSelector converts the NodeSelector api type into a struct that
// implements labels.Selector
// Note: This function should be kept in sync with the selector methods in
// pkg/labels/selector.go.
func nodeSelectorAsSelector(ns *corev1.NodeSelector) (labels.Selector, error) {
	if ns == nil {
		return labels.Nothing(), nil
	}
	if len(ns.NodeSelectorTerms) == 0 {
		return labels.Everything(), nil
	}
	var requirements []labels.Requirement

	for _, nsTerm := range ns.NodeSelectorTerms {
		for _, expr := range nsTerm.MatchExpressions {
			req, err := nodeSelectorRequirementsAsLabelRequirements(expr)
			if err != nil {
				return nil, err
			}
			requirements = append(requirements, *req)
		}

		for _, field := range nsTerm.MatchFields {
			req, err := nodeSelectorRequirementsAsLabelRequirements(field)
			if err != nil {
				return nil, err
			}
			requirements = append(requirements, *req)
		}
	}

	selector := labels.NewSelector()
	selector = selector.Add(requirements...)
	return selector, nil
}
