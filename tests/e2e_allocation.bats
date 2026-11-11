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

# Allocation and release of Pod CIDRs when node labels, the ClusterCIDRs or
# the controller change between allocation and release. A ClusterCIDR that no
# node uses anymore but that the controller refuses to delete shows Pod CIDRs
# left allocated. Each test uses its own ranges, outside the catch-all
# ClusterCIDR 10.244.0.0/16.

load helpers

setup_file() {
  install_controller_under_test
}

teardown() {
  cleanup
}

@test "node deletion releases its Pod CIDR after a higher-ranked ClusterCIDR was created" {
  create_clustercidr e2e-a-original 10.200.0.0/24 "" "$(expressions_term e2e-zone a)"
  create_node e2e-a-node e2e-zone=a,e2e-rack=1
  expect_ipv4 e2e-a-node 10.200.0.0/24

  # Two matching requirements instead of one: ranks first for the node, and
  # its range contains the node's Pod CIDR.
  create_clustercidr e2e-a-newer 10.200.0.0/16 "" "    - matchExpressions:
      - {key: e2e-zone, operator: In, values: [a]}
      - {key: e2e-rack, operator: In, values: [\"1\"]}"
  sync_node e2e-a-node
  delete_node e2e-a-node

  # 10.200.0.0/24 is the only Pod CIDR of the original ClusterCIDR.
  create_node e2e-a-node-2 e2e-zone=a
  expect_ipv4 e2e-a-node-2 10.200.0.0/24
  delete_node e2e-a-node-2

  delete_clustercidr e2e-a-newer e2e-a-original
  expect_deleted e2e-a-newer
  expect_deleted e2e-a-original
}

@test "node deletion releases its Pod CIDR after its labels changed" {
  create_clustercidr e2e-b-original 10.201.0.0/24 "" "$(expressions_term e2e-zone b)"
  create_node e2e-b-node e2e-zone=b
  expect_ipv4 e2e-b-node 10.201.0.0/24

  relabel_node e2e-b-node e2e-zone=b-moved
  delete_node e2e-b-node

  delete_clustercidr e2e-b-original
  expect_deleted e2e-b-original
}

@test "a relabeled node is not associated with a ClusterCIDR that selects it now" {
  create_clustercidr e2e-c-original 10.202.0.0/24 "" "$(expressions_term e2e-zone c)"
  # Selects the relabeled node, and its range contains the node's Pod CIDR.
  create_clustercidr e2e-c-other 10.202.0.0/16 "" "$(expressions_term e2e-zone c-moved)"
  create_node e2e-c-node e2e-zone=c
  expect_ipv4 e2e-c-node 10.202.0.0/24

  relabel_node e2e-c-node e2e-zone=c-moved
  sync_node e2e-c-node
  delete_clustercidr e2e-c-other
  expect_deleted e2e-c-other

  delete_clustercidr e2e-c-original
  expect_kept e2e-c-original
  delete_node e2e-c-node
  expect_deleted e2e-c-original
}

@test "a node keeps a terminating ClusterCIDR rather than moving to an overlapping one" {
  # Same node selector; the /24 has fewer allocatable CIDRs, so it ranks first.
  create_clustercidr e2e-d-original 10.203.0.0/24 "" "$(expressions_term e2e-zone d)"
  create_clustercidr e2e-d-overlapping 10.203.0.0/16 "" "$(expressions_term e2e-zone d)"
  create_node e2e-d-node e2e-zone=d
  expect_ipv4 e2e-d-node 10.203.0.0/24

  delete_clustercidr e2e-d-original
  expect_kept e2e-d-original
  sync_node e2e-d-node
  delete_clustercidr e2e-d-overlapping
  expect_deleted e2e-d-overlapping

  delete_node e2e-d-node
  expect_deleted e2e-d-original
}

@test "a terminating ClusterCIDR is kept for its node across a controller restart" {
  create_clustercidr e2e-e-original 10.204.0.0/24 "" "$(expressions_term e2e-zone e)"
  create_node e2e-e-node e2e-zone=e
  expect_ipv4 e2e-e-node 10.204.0.0/24

  delete_clustercidr e2e-e-original
  restart_controller
  expect_kept e2e-e-original

  delete_node e2e-e-node
  expect_deleted e2e-e-original
}

@test "a relabeled node keeps its Pod CIDR across a controller restart" {
  create_clustercidr e2e-f-original 10.205.0.0/24 "" "$(expressions_term e2e-zone f)"
  create_node e2e-f-node e2e-zone=f
  expect_ipv4 e2e-f-node 10.205.0.0/24

  relabel_node e2e-f-node e2e-zone=f-moved
  restart_controller
  expect_warning_event node e2e-f-node ClusterCIDRNotSelectingNode

  # 10.205.0.0/24 is the only Pod CIDR of the original ClusterCIDR, and the
  # relabeled node still uses it.
  create_node e2e-f-node-2 e2e-zone=f
  expect_not_ipv4 e2e-f-node-2 10.205.0.0/24

  delete_clustercidr e2e-f-original
  expect_kept e2e-f-original
  delete_node e2e-f-node e2e-f-node-2
  expect_deleted e2e-f-original
}

@test "a ClusterCIDR that can't hold all Pod CIDRs of a node keeps none of them" {
  create_clustercidr e2e-g-original 10.206.0.0/24 fd00:206::/120 "$(expressions_term e2e-zone g)"
  create_node e2e-g-node e2e-zone=g,e2e-rack=1
  expect_ipv4 e2e-g-node 10.206.0.0/24

  # Ranks first for the node and holds its IPv4 Pod CIDR, not its IPv6 one.
  create_clustercidr e2e-g-partial 10.206.0.0/16 fd00:207::/120 "    - matchExpressions:
      - {key: e2e-zone, operator: In, values: [g]}
      - {key: e2e-rack, operator: In, values: [\"1\"]}"
  # At startup the node has no association, so its Pod CIDRs are occupied in
  # the first ClusterCIDR that can hold them.
  restart_controller
  delete_node e2e-g-node

  # The partial ClusterCIDR ranks first for this node; its first IPv4 block is
  # free unless the occupy at startup left it allocated.
  create_node e2e-g-node-2 e2e-zone=g,e2e-rack=1
  expect_ipv4 e2e-g-node-2 10.206.0.0/24
}

@test "a ClusterCIDR with an invalid node selector selects no node" {
  # matchFields only support In and NotIn.
  create_clustercidr e2e-h-invalid 10.207.0.0/24 "" "    - matchFields:
      - {key: metadata.name, operator: DoesNotExist}"
  expect_warning_event clustercidr e2e-h-invalid InvalidNodeSelector

  create_node e2e-h-node
  expect_not_ipv4 e2e-h-node 10.207.0.0/24

  delete_clustercidr e2e-h-invalid
  expect_deleted e2e-h-invalid
}

@test "a node held by a finalizer keeps its Pod CIDR until it is gone" {
  skip "Pod CIDRs are released when the node deletion starts, not when the node is gone"

  create_clustercidr e2e-i-original 10.208.0.0/24 "" "$(expressions_term e2e-zone i)"
  create_node e2e-i-node e2e-zone=i "${E2E_LABEL}/hold"
  expect_ipv4 e2e-i-node 10.208.0.0/24

  kubectl delete node e2e-i-node --wait=false
  sleep 5
  # 10.208.0.0/24 is the only Pod CIDR of the ClusterCIDR, and the node still
  # exists.
  create_node e2e-i-node-2 e2e-zone=i
  expect_not_ipv4 e2e-i-node-2 10.208.0.0/24
}
