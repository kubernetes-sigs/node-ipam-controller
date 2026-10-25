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
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/util/yaml"

	v1 "sigs.k8s.io/node-ipam-controller/pkg/apis/clustercidr/v1"
	"sigs.k8s.io/node-ipam-controller/pkg/controller/ipam/multicidrset"
)

// loadExampleCIDRMap maps the ClusterCIDRs of the given files in the examples
// directory, the way the controller does when it creates them.
func loadExampleCIDRMap(t *testing.T, files ...string) map[string][]*multicidrset.ClusterCIDR {
	t.Helper()

	ra := &multiCIDRRangeAllocator{}
	cidrMap := make(map[string][]*multicidrset.ClusterCIDR)
	for _, file := range files {
		f, err := os.Open(filepath.Join("..", "..", "..", "examples", file))
		require.NoError(t, err)
		defer f.Close()

		decoder := yaml.NewYAMLOrJSONDecoder(f, 4096)
		for {
			clusterCIDR := &v1.ClusterCIDR{}
			err := decoder.Decode(clusterCIDR)
			if errors.Is(err, io.EOF) {
				break
			}
			require.NoError(t, err, file)

			key, err := ra.nodeSelectorKey(clusterCIDR)
			require.NoError(t, err, clusterCIDR.Name)
			clusterCIDRSet, err := ra.createClusterCIDRSet(clusterCIDR, false)
			require.NoError(t, err, clusterCIDR.Name)
			require.NoError(t, ra.mapClusterCIDRSet(cidrMap, key, clusterCIDRSet))
		}
	}
	return cidrMap
}

// TestExamplesLoad asserts that the controller accepts every ClusterCIDR in
// the examples directory.
func TestExamplesLoad(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "..", "..", "examples", "*.yaml"))
	require.NoError(t, err)
	require.NotEmpty(t, files)

	for _, file := range files {
		t.Run(filepath.Base(file), func(t *testing.T) {
			assert.NotEmpty(t, loadExampleCIDRMap(t, filepath.Base(file)))
		})
	}
}
