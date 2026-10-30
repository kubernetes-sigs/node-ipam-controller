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

func TestParseNodeSelectorMatch(t *testing.T) {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "node-1",
			Labels: map[string]string{"zone": "a", "rack": "1", "rank": "7", "tier": "gold"},
		},
	}
	unlabeledNode := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-2"}}

	nameIn := func(name string) corev1.NodeSelectorRequirement {
		return nodeSelectorRequirement("metadata.name", corev1.NodeSelectorOpIn, name)
	}
	nameNotIn := func(name string) corev1.NodeSelectorRequirement {
		return nodeSelectorRequirement("metadata.name", corev1.NodeSelectorOpNotIn, name)
	}
	label := nodeSelectorRequirement

	testCases := []struct {
		description  string
		node         *corev1.Node
		nodeSelector *corev1.NodeSelector
		wantMatch    bool
		wantMatchCnt int
	}{
		// A single term: its requirements are ANDed.
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
			wantMatch: false,
		},

		// Several terms: they are ORed.
		{
			description: "the first of two terms matches",
			nodeSelector: selectorWithTerms(
				expressionsTerm(label("zone", corev1.NodeSelectorOpIn, "a")),
				expressionsTerm(label("zone", corev1.NodeSelectorOpIn, "b")),
			),
			wantMatch:    true,
			wantMatchCnt: 1,
		},
		{
			description: "the second of two terms matches",
			nodeSelector: selectorWithTerms(
				expressionsTerm(label("zone", corev1.NodeSelectorOpIn, "b")),
				expressionsTerm(label("zone", corev1.NodeSelectorOpIn, "a")),
			),
			wantMatch:    true,
			wantMatchCnt: 1,
		},
		{
			description: "both of two terms match",
			nodeSelector: selectorWithTerms(
				expressionsTerm(label("zone", corev1.NodeSelectorOpIn, "a")),
				expressionsTerm(label("rack", corev1.NodeSelectorOpIn, "1")),
			),
			wantMatch:    true,
			wantMatchCnt: 1,
		},
		{
			description: "none of two terms matches",
			nodeSelector: selectorWithTerms(
				expressionsTerm(label("zone", corev1.NodeSelectorOpIn, "b")),
				expressionsTerm(label("zone", corev1.NodeSelectorOpIn, "c")),
			),
			wantMatch: false,
		},
		{
			// Every requirement matches some term, but no term matches as a
			// whole: this is what tells ORed ANDs from a flat OR of requirements.
			description: "every term matches only partially",
			nodeSelector: selectorWithTerms(
				expressionsTerm(label("zone", corev1.NodeSelectorOpIn, "a"), label("rack", corev1.NodeSelectorOpIn, "2")),
				expressionsTerm(label("zone", corev1.NodeSelectorOpIn, "b"), label("rack", corev1.NodeSelectorOpIn, "1")),
			),
			wantMatch: false,
		},

		// The match count is that of the largest matching term.
		{
			description: "the largest matching term gives the match count",
			nodeSelector: selectorWithTerms(
				expressionsTerm(label("zone", corev1.NodeSelectorOpIn, "a")),
				expressionsTerm(label("zone", corev1.NodeSelectorOpIn, "a"), label("rank", corev1.NodeSelectorOpExists)),
			),
			wantMatch:    true,
			wantMatchCnt: 2,
		},
		{
			description: "a larger term that does not match is not counted",
			nodeSelector: selectorWithTerms(
				expressionsTerm(label("zone", corev1.NodeSelectorOpIn, "a")),
				expressionsTerm(label("zone", corev1.NodeSelectorOpIn, "b"), label("rank", corev1.NodeSelectorOpExists)),
			),
			wantMatch:    true,
			wantMatchCnt: 1,
		},

		// matchFields are matched against the node fields.
		{
			description:  "matchFields In the node name",
			nodeSelector: selectorWithTerms(fieldsTerm(nameIn("node-1"))),
			wantMatch:    true,
			wantMatchCnt: 1,
		},
		{
			description:  "matchFields In another node name",
			nodeSelector: selectorWithTerms(fieldsTerm(nameIn("node-2"))),
			wantMatch:    false,
		},
		{
			description:  "matchFields NotIn another node name",
			nodeSelector: selectorWithTerms(fieldsTerm(nameNotIn("node-2"))),
			wantMatch:    true,
			wantMatchCnt: 1,
		},
		{
			description:  "matchFields NotIn the node name",
			nodeSelector: selectorWithTerms(fieldsTerm(nameNotIn("node-1"))),
			wantMatch:    false,
		},
		{
			description:  "several matchFields of a term are ANDed, all match",
			nodeSelector: selectorWithTerms(fieldsTerm(nameIn("node-1"), nameNotIn("node-2"))),
			wantMatch:    true,
			wantMatchCnt: 2,
		},
		{
			description:  "several matchFields of a term are ANDed, one does not match",
			nodeSelector: selectorWithTerms(fieldsTerm(nameIn("node-1"), nameNotIn("node-1"))),
			wantMatch:    false,
		},

		// matchExpressions and matchFields of a term are ANDed.
		{
			description: "labels and fields of a term both match",
			nodeSelector: selectorWithTerms(corev1.NodeSelectorTerm{
				MatchExpressions: []corev1.NodeSelectorRequirement{label("zone", corev1.NodeSelectorOpIn, "a")},
				MatchFields:      []corev1.NodeSelectorRequirement{nameIn("node-1")},
			}),
			wantMatch:    true,
			wantMatchCnt: 2,
		},
		{
			description: "labels of a term match, fields do not",
			nodeSelector: selectorWithTerms(corev1.NodeSelectorTerm{
				MatchExpressions: []corev1.NodeSelectorRequirement{label("zone", corev1.NodeSelectorOpIn, "a")},
				MatchFields:      []corev1.NodeSelectorRequirement{nameIn("node-2")},
			}),
			wantMatch: false,
		},
		{
			description: "fields of a term match, labels do not",
			nodeSelector: selectorWithTerms(corev1.NodeSelectorTerm{
				MatchExpressions: []corev1.NodeSelectorRequirement{label("zone", corev1.NodeSelectorOpIn, "b")},
				MatchFields:      []corev1.NodeSelectorRequirement{nameIn("node-1")},
			}),
			wantMatch: false,
		},

		// A fields term and a labels term are ORed like any other terms.
		{
			description: "a fields term does not match, a labels term does",
			nodeSelector: selectorWithTerms(
				fieldsTerm(nameIn("node-2")),
				expressionsTerm(label("zone", corev1.NodeSelectorOpIn, "a")),
			),
			wantMatch:    true,
			wantMatchCnt: 1,
		},
		{
			description: "a fields term matches, a labels term does not",
			nodeSelector: selectorWithTerms(
				fieldsTerm(nameIn("node-1")),
				expressionsTerm(label("zone", corev1.NodeSelectorOpIn, "b")),
			),
			wantMatch:    true,
			wantMatchCnt: 1,
		},

		// Empty selectors and terms match no node.
		{
			description:  "a selector without terms matches no node",
			nodeSelector: selectorWithTerms(),
			wantMatch:    false,
		},
		{
			description:  "an empty term matches no node",
			nodeSelector: selectorWithTerms(corev1.NodeSelectorTerm{}),
			wantMatch:    false,
		},
		{
			description: "an empty term is dropped, the remaining terms still match",
			nodeSelector: selectorWithTerms(
				corev1.NodeSelectorTerm{},
				expressionsTerm(label("zone", corev1.NodeSelectorOpIn, "a")),
			),
			wantMatch:    true,
			wantMatchCnt: 1,
		},

		// Operators.
		{
			description:  "NotIn matches a label with a different value",
			nodeSelector: selectorWithTerms(expressionsTerm(label("zone", corev1.NodeSelectorOpNotIn, "b"))),
			wantMatch:    true,
			wantMatchCnt: 1,
		},
		{
			description:  "DoesNotExist matches a label the node does not have",
			nodeSelector: selectorWithTerms(expressionsTerm(label("region", corev1.NodeSelectorOpDoesNotExist))),
			wantMatch:    true,
			wantMatchCnt: 1,
		},
		{
			description:  "Lt matches a lower integer label value",
			nodeSelector: selectorWithTerms(expressionsTerm(label("rank", corev1.NodeSelectorOpLt, "9"))),
			wantMatch:    true,
			wantMatchCnt: 1,
		},
		{
			description:  "Gt does not match a label value that is not an integer",
			nodeSelector: selectorWithTerms(expressionsTerm(label("tier", corev1.NodeSelectorOpGt, "1"))),
			wantMatch:    false,
		},
		{
			description:  "a node without labels matches DoesNotExist",
			node:         unlabeledNode,
			nodeSelector: selectorWithTerms(expressionsTerm(label("zone", corev1.NodeSelectorOpDoesNotExist))),
			wantMatch:    true,
			wantMatchCnt: 1,
		},
		{
			description:  "a node without labels does not match In",
			node:         unlabeledNode,
			nodeSelector: selectorWithTerms(expressionsTerm(label("zone", corev1.NodeSelectorOpIn, "a"))),
			wantMatch:    false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			testNode := tc.node
			if testNode == nil {
				testNode = node
			}

			selector, err := parseNodeSelector(tc.nodeSelector)
			require.NoError(t, err)

			gotMatch, gotMatchCnt := selector.match(testNode)
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
			description: "unknown matchExpressions operator",
			nodeSelector: selectorWithTerms(expressionsTerm(
				nodeSelectorRequirement("zone", corev1.NodeSelectorOperator("Equals"), "a"),
			)),
			wantErr: "nodeSelectorTerms[0].matchExpressions[0].operator",
		},
		{
			description: "matchFields do not support the Exists operator",
			nodeSelector: selectorWithTerms(fieldsTerm(
				nodeSelectorRequirement("metadata.name", corev1.NodeSelectorOpExists),
			)),
			wantErr: "nodeSelectorTerms[0].matchFields[0].operator",
		},
		{
			description: "matchFields take a single value",
			nodeSelector: selectorWithTerms(fieldsTerm(
				nodeSelectorRequirement("metadata.name", corev1.NodeSelectorOpIn, "node-1", "node-2"),
			)),
			wantErr: "nodeSelectorTerms[0].matchFields[0].values",
		},
		{
			description: "invalid label key",
			nodeSelector: selectorWithTerms(expressionsTerm(
				nodeSelectorRequirement("-not-a-label-key-", corev1.NodeSelectorOpExists),
			)),
			wantErr: "nodeSelectorTerms[0].matchExpressions[0].key",
		},
		{
			description: "Gt takes an integer value",
			nodeSelector: selectorWithTerms(expressionsTerm(
				nodeSelectorRequirement("rank", corev1.NodeSelectorOpGt, "high"),
			)),
			wantErr: "nodeSelectorTerms[0].matchExpressions[0]",
		},
		{
			description: "the error names the index of the invalid term",
			nodeSelector: selectorWithTerms(
				expressionsTerm(nodeSelectorRequirement("zone", corev1.NodeSelectorOpIn, "a")),
				corev1.NodeSelectorTerm{},
				fieldsTerm(nodeSelectorRequirement("metadata.name", corev1.NodeSelectorOpExists)),
			),
			wantErr: "nodeSelectorTerms[2].matchFields[0].operator",
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
