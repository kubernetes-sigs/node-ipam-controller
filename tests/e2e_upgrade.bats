#!/usr/bin/env bats

# Copyright The Kubernetes Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Upgrade from v0.3.0, the last release that converted node selectors to label
# selectors, to the controller under test, which matches them with the core v1
# semantics. Nodes got Pod CIDRs from ClusterCIDRs that don't select them, or
# whose node selector is rejected, after the upgrade. Their Pod CIDRs must stay
# allocated.
#
# Delete this file once upgrades from v0.3.0 are no longer supported: from a
# release with the core v1 semantics, the setup can't happen.

load helpers

PREVIOUS_CHART="oci://registry.k8s.io/networking/charts/node-ipam-controller"
PREVIOUS_VERSION="0.3.0"

setup_file() {
  install_controller "${PREVIOUS_CHART}" --version "${PREVIOUS_VERSION}"
  [ "$(controller_image)" = "${IMAGE_REPO}:v${PREVIOUS_VERSION}" ] ||
    { echo "controller image is $(controller_image), expected v${PREVIOUS_VERSION}"; return 1; }

  # v0.3.0 turns matchFields into label requirements, and the nodes have no
  # metadata.name label: both ClusterCIDRs select every node. Each holds a
  # single Pod CIDR.
  create_clustercidr e2e-upgrade-excluding 10.210.0.0/24 "" "    - matchFields:
      - {key: metadata.name, operator: NotIn, values: [e2e-upgrade-node-1]}"
  create_node e2e-upgrade-node-1
  create_clustercidr e2e-upgrade-invalid 10.211.0.0/24 "" "    - matchFields:
      - {key: metadata.name, operator: DoesNotExist}"
  create_node e2e-upgrade-node-3
  expect_ipv4 e2e-upgrade-node-1 10.210.0.0/24
  expect_ipv4 e2e-upgrade-node-3 10.211.0.0/24

  install_controller_under_test
  [ "$(controller_image)" = "${IMAGE_REPO}:${IMAGE_TAG}" ] ||
    { echo "controller image is $(controller_image), expected ${IMAGE_TAG}"; return 1; }
}

teardown_file() {
  cleanup
}

@test "upgrade: a ClusterCIDR that doesn't select its node anymore keeps the node's Pod CIDR" {
  expect_warning_event node e2e-upgrade-node-1 ClusterCIDRNotSelectingNode

  # The ClusterCIDR selects every node but node-1 now, and node-1 uses its only
  # Pod CIDR.
  create_node e2e-upgrade-node-2
  expect_not_ipv4 e2e-upgrade-node-2 10.210.0.0/24
}

@test "upgrade: a ClusterCIDR whose node selector is rejected keeps its node's Pod CIDR" {
  expect_warning_event clustercidr e2e-upgrade-invalid InvalidNodeSelector
  expect_warning_event node e2e-upgrade-node-3 ClusterCIDRNotSelectingNode

  # Contains the range of the rejected ClusterCIDR, and selects node-4 only.
  create_clustercidr e2e-upgrade-overlapping 10.211.0.0/16 "" "$(expressions_term e2e-pool overlapping)"
  create_node e2e-upgrade-node-4 e2e-pool=overlapping
  expect_not_ipv4 e2e-upgrade-node-4 10.211.0.0/24
}

@test "upgrade: the ClusterCIDRs are deleted only after their nodes" {
  delete_clustercidr e2e-upgrade-excluding e2e-upgrade-invalid
  expect_kept e2e-upgrade-excluding
  expect_kept e2e-upgrade-invalid

  delete_node e2e-upgrade-node-1 e2e-upgrade-node-3
  expect_deleted e2e-upgrade-excluding
  expect_deleted e2e-upgrade-invalid
}
