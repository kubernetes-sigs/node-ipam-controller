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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func nodeSelectorRequirement(key string, op corev1.NodeSelectorOperator, values ...string) corev1.NodeSelectorRequirement {
	return corev1.NodeSelectorRequirement{Key: key, Operator: op, Values: values}
}

func selectorWithTerms(terms ...corev1.NodeSelectorTerm) *corev1.NodeSelector {
	return &corev1.NodeSelector{NodeSelectorTerms: terms}
}

func expressionsTerm(requirements ...corev1.NodeSelectorRequirement) corev1.NodeSelectorTerm {
	return corev1.NodeSelectorTerm{MatchExpressions: requirements}
}

func fieldsTerm(requirements ...corev1.NodeSelectorRequirement) corev1.NodeSelectorTerm {
	return corev1.NodeSelectorTerm{MatchFields: requirements}
}

// TestParseNodeSelectorMatch pins how node selectors are matched. Several cases
// differ from the core v1 semantics: the terms are ANDed instead of ORed,
// matchFields are matched as labels, and a node selector without terms or with
// an empty term selects every node.
// xref: https://github.com/kubernetes-sigs/node-ipam-controller/issues/36
func TestParseNodeSelectorMatch(t *testing.T) {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "node-1",
			Labels: map[string]string{"zone": "a", "rack": "1", "rank": "7"},
		},
	}
	label := nodeSelectorRequirement

	testCases := []struct {
		description  string
		nodeSelector *corev1.NodeSelector
		wantMatch    bool
		wantMatchCnt int
	}{
		{
			description: "all requirements of a single term match",
			nodeSelector: selectorWithTerms(expressionsTerm(
				label("zone", corev1.NodeSelectorOpIn, "a"),
				label("rank", corev1.NodeSelectorOpGt, "5"),
			)),
			wantMatch:    true,
			wantMatchCnt: 2,
		},
		{
			description: "one requirement of a single term does not match",
			nodeSelector: selectorWithTerms(expressionsTerm(
				label("zone", corev1.NodeSelectorOpIn, "a"),
				label("rank", corev1.NodeSelectorOpGt, "9"),
			)),
			wantMatch:    false,
			wantMatchCnt: 1,
		},
		{
			description: "the requirements of all terms match",
			nodeSelector: selectorWithTerms(
				expressionsTerm(label("zone", corev1.NodeSelectorOpIn, "a")),
				expressionsTerm(label("rack", corev1.NodeSelectorOpIn, "1")),
			),
			wantMatch:    true,
			wantMatchCnt: 2,
		},
		{
			// Core v1 ORs the terms, so the first term selects the node.
			description: "the terms are ANDed",
			nodeSelector: selectorWithTerms(
				expressionsTerm(label("zone", corev1.NodeSelectorOpIn, "a")),
				expressionsTerm(label("zone", corev1.NodeSelectorOpIn, "b")),
			),
			wantMatch:    false,
			wantMatchCnt: 1,
		},
		{
			// Core v1 matches matchFields against the node name.
			description: "matchFields are matched against the node labels",
			nodeSelector: selectorWithTerms(fieldsTerm(
				nodeSelectorRequirement("metadata.name", corev1.NodeSelectorOpIn, "node-1"),
			)),
			wantMatch:    false,
			wantMatchCnt: 0,
		},
		{
			// Core v1 excludes node-1.
			description: "matchFields NotIn matches a node without such a label",
			nodeSelector: selectorWithTerms(fieldsTerm(
				nodeSelectorRequirement("metadata.name", corev1.NodeSelectorOpNotIn, "node-1"),
			)),
			wantMatch:    true,
			wantMatchCnt: 1,
		},
		{
			// Core v1 selects no node.
			description:  "a node selector without terms selects every node",
			nodeSelector: selectorWithTerms(),
			wantMatch:    true,
			wantMatchCnt: 0,
		},
		{
			// Core v1 selects no node.
			description:  "an empty term selects every node",
			nodeSelector: selectorWithTerms(corev1.NodeSelectorTerm{}),
			wantMatch:    true,
			wantMatchCnt: 0,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			selector, err := parseNodeSelector(tc.nodeSelector)
			require.NoError(t, err)

			gotMatch, gotMatchCnt := selector.match(node)
			assert.Equal(t, tc.wantMatch, gotMatch, "unexpected match")
			assert.Equal(t, tc.wantMatchCnt, gotMatchCnt, "unexpected match count")
		})
	}
}

func TestParseNodeSelectorError(t *testing.T) {
	testCases := []struct {
		description  string
		nodeSelector *corev1.NodeSelector
		wantErr      string
	}{
		{
			description:  "nil node selector",
			nodeSelector: nil,
			wantErr:      "node selector cannot be nil",
		},
		{
			description: "unknown operator",
			nodeSelector: selectorWithTerms(expressionsTerm(
				nodeSelectorRequirement("zone", corev1.NodeSelectorOperator("Equals"), "a"),
			)),
			wantErr: `"Equals" is not a valid node selector operator`,
		},
		{
			description: "invalid label key",
			nodeSelector: selectorWithTerms(expressionsTerm(
				nodeSelectorRequirement("-not-a-label-key-", corev1.NodeSelectorOpExists),
			)),
			wantErr: "-not-a-label-key-",
		},
		{
			description: "Gt takes an integer value",
			nodeSelector: selectorWithTerms(expressionsTerm(
				nodeSelectorRequirement("rank", corev1.NodeSelectorOpGt, "high"),
			)),
			wantErr: "must be an integer",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			_, err := parseNodeSelector(tc.nodeSelector)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}
