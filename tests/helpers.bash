#!/usr/bin/env bash

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

# Helpers shared by the e2e tests. The tests use fake Node objects: no kubelet
# runs for them, the controller only needs the Node object. Every object a test
# creates carries the label E2E_LABEL, so that cleanup finds it.

E2E_LABEL="node-ipam-e2e"
FINALIZER="networking.x-k8s.io/cluster-cidr-finalizer"
# How long a ClusterCIDR that no node uses may take to be deleted.
DELETE_TIMEOUT=30
# How long a ClusterCIDR that nodes use is watched to stay.
KEEP_SECONDS=15

# install_controller installs or upgrades the controller release with the
# chart and arguments "$@", and the catch-all ClusterCIDR of `make run`.
# Without --reset-values, an upgrade without --set reuses the values of the
# previous release, e.g. the image of the controller under test.
function install_controller {
  helm upgrade --install "${RELEASE}" "$@" --reset-values \
    --namespace "${NAMESPACE}" --create-namespace --wait --timeout 3m
  wait_for_controller
  kubectl apply -f "$ROOT_DIR"/examples/clustercidr-dual.yaml
}

# install_controller_under_test installs the chart and image of the working tree.
function install_controller_under_test {
  install_controller "$ROOT_DIR"/charts/node-ipam-controller \
    --set image.repository="${IMAGE_REPO}" --set image.tag="${IMAGE_TAG}"
}

# controller_image prints the image of the controller Deployment.
function controller_image {
  kubectl -n "${NAMESPACE}" get deployment "${RELEASE}" -o jsonpath='{.spec.template.spec.containers[0].image}'
}

function restart_controller {
  kubectl -n "${NAMESPACE}" rollout restart deployment/"${RELEASE}"
  wait_for_controller
}

# wait_for_controller waits until the rollout is done, the previous pod is gone,
# and the new pod holds the leader lease. A ready pod is not active yet: the
# previous one may still hold the lease and act on the objects a test creates.
function wait_for_controller {
  kubectl -n "${NAMESPACE}" rollout status deployment/"${RELEASE}" --timeout=2m
  local pods
  for _ in $(seq 1 120); do
    pods=$(kubectl -n "${NAMESPACE}" get pods -l app.kubernetes.io/name=node-ipam-controller -o name)
    if [ "$(echo "${pods}" | wc -l)" -eq 1 ] &&
      [ "$(kubectl -n "${NAMESPACE}" get lease "${RELEASE}" -o jsonpath='{.spec.holderIdentity}')" = "${pods#pod/}" ]; then
      return 0
    fi
    sleep 1
  done
  echo "controller pod ${pods} did not take over the leader lease"
  return 1
}

# create_clustercidr creates ClusterCIDR $1 with IPv4 range $2, IPv6 range $3
# (none if empty) and the node selector terms $4 (catch-all if empty), and
# waits until the controller has loaded it, i.e. added its finalizer.
function create_clustercidr {
  local ipv6="" selector=""
  [ -n "$3" ] && ipv6="ipv6: $3"
  [ -n "$4" ] && selector="nodeSelector:
    nodeSelectorTerms:
$4"
  kubectl create -f - <<EOF
apiVersion: networking.x-k8s.io/v1
kind: ClusterCIDR
metadata:
  name: $1
  labels:
    ${E2E_LABEL}: "true"
spec:
  perNodeHostBits: 8
  ipv4: $2
  ${ipv6}
  ${selector}
EOF
  for _ in $(seq 1 60); do
    kubectl get clustercidr "$1" -o jsonpath='{.metadata.finalizers}' | grep -q "${FINALIZER}" && return 0
    sleep 1
  done
  echo "the controller did not load ClusterCIDR $1"
  return 1
}

# expressions_term prints a node selector term requiring label $1 In [$2].
function expressions_term {
  printf '    - matchExpressions:\n      - {key: %s, operator: In, values: ["%s"]}\n' "$1" "$2"
}

# create_node creates the fake node $1 with the labels $2 (k=v,k=v) and the
# finalizer $3 (none if empty), and waits for its Pod CIDRs. The NoSchedule
# taint keeps DaemonSet pods off it.
function create_node {
  local labels="" kv finalizers=""
  if [ -n "${2:-}" ]; then
    IFS=',' read -ra kvs <<< "$2"
    for kv in "${kvs[@]}"; do
      labels="${labels}
    ${kv%%=*}: \"${kv#*=}\""
    done
  fi
  [ -n "${3:-}" ] && finalizers="finalizers: [\"$3\"]"
  kubectl create -f - <<EOF
apiVersion: v1
kind: Node
metadata:
  name: $1
  ${finalizers}
  labels:
    ${E2E_LABEL}: "true"${labels}
spec:
  taints:
  - key: ${E2E_LABEL}
    effect: NoSchedule
EOF
  for _ in $(seq 1 60); do
    [ -n "$(pod_cidrs "$1")" ] && return 0
    sleep 1
  done
  echo "node $1 got no Pod CIDRs"
  return 1
}

# pod_cidrs prints the Pod CIDRs of node $1, separated by spaces.
function pod_cidrs {
  kubectl get node "$1" -o jsonpath='{.spec.podCIDRs[*]}'
}

# sync_node updates node $1, as any kubelet status update would, which makes
# the controller sync it, and gives the controller time to process it.
function sync_node {
  kubectl annotate node "$1" --overwrite "${E2E_LABEL}/synced-at=$(date +%s%N)"
  sleep 5
}

# relabel_node sets label $2 (k=v) on node $1.
function relabel_node {
  kubectl label node "$1" --overwrite "$2"
  sleep 5
}

function delete_node {
  kubectl delete node "$@" --wait=true
  sleep 2
}

function delete_clustercidr {
  kubectl delete clustercidr "$@" --wait=false
}

# expect_deleted fails unless ClusterCIDR $1 is deleted within DELETE_TIMEOUT.
function expect_deleted {
  kubectl wait --for=delete clustercidr "$1" --timeout="${DELETE_TIMEOUT}s" && return 0
  echo "ClusterCIDR $1 was not deleted within ${DELETE_TIMEOUT}s"
  return 1
}

# expect_kept fails if ClusterCIDR $1 is deleted within KEEP_SECONDS.
function expect_kept {
  sleep "${KEEP_SECONDS}"
  kubectl get clustercidr "$1" > /dev/null && return 0
  echo "ClusterCIDR $1 was deleted while a node uses it"
  return 1
}

# expect_ipv4 fails unless the IPv4 Pod CIDR of node $1 is $2.
function expect_ipv4 {
  local got
  got=$(pod_cidrs "$1" | tr ' ' '\n' | grep -v ':' || true)
  [ "${got}" = "$2" ] && return 0
  echo "node $1 has IPv4 Pod CIDR '${got}', expected '$2'"
  return 1
}

# expect_not_ipv4 fails if the IPv4 Pod CIDR of node $1 is $2.
function expect_not_ipv4 {
  local got
  got=$(pod_cidrs "$1" | tr ' ' '\n' | grep -v ':' || true)
  [ "${got}" != "$2" ] && return 0
  echo "node $1 got IPv4 Pod CIDR $2, expected another one"
  return 1
}

# expect_warning_event fails unless a Warning event with reason $3 is recorded
# on $1 (node or clustercidr) $2 within 30s. It matches the object's UID, not
# its name, so that events of an earlier object with the same name don't count.
function expect_warning_event {
  local uid
  uid=$(kubectl get "$1" "$2" -o jsonpath='{.metadata.uid}')
  for _ in $(seq 1 30); do
    [ -n "$(kubectl get events -A -o name \
      --field-selector "involvedObject.uid=${uid},reason=$3,type=Warning")" ] && return 0
    sleep 1
  done
  echo "no Warning event $3 on $1 $2"
  return 1
}

# cleanup deletes the nodes and ClusterCIDRs the tests created, removing
# finalizers that would hold them, e.g. after a failed test.
function cleanup {
  local obj
  for obj in $(kubectl get nodes -l "${E2E_LABEL}" -o name); do
    kubectl patch "${obj}" --type=merge -p '{"metadata":{"finalizers":null}}' > /dev/null
  done
  kubectl delete nodes -l "${E2E_LABEL}" --wait=true
  kubectl delete clustercidrs -l "${E2E_LABEL}" --wait=false
  kubectl wait --for=delete clustercidrs -l "${E2E_LABEL}" --timeout=30s > /dev/null 2>&1 || true
  for obj in $(kubectl get clustercidrs -l "${E2E_LABEL}" -o name); do
    kubectl patch "${obj}" --type=merge -p '{"metadata":{"finalizers":null}}' > /dev/null
  done
}
