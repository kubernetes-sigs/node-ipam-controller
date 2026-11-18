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

set -eu

function setup_suite {
  export BATS_TEST_TIMEOUT=300
  export CLUSTER_NAME="${CLUSTER_NAME:-nodeipam-e2e}"
  export NAMESPACE="nodeipam"
  export RELEASE="node-ipam-controller"
  export IMAGE_REPO="registry.k8s.io/networking/node-ipam-controller"
  # Set IMAGE_TAG and SKIP_IMAGE_BUILD to test an image loaded into the
  # cluster already, e.g. one built from another commit.
  export IMAGE_TAG="${IMAGE_TAG:-e2e}"
  export ROOT_DIR="$BATS_TEST_DIRNAME/.."

  if kind get clusters | grep -q "^${CLUSTER_NAME}$"; then
    echo "Kind cluster ${CLUSTER_NAME} already exists. Skipping creation." >&3
  else
    # The kube-controller-manager must not allocate node CIDRs itself.
    kind create cluster --name "${CLUSTER_NAME}" \
      --config "$ROOT_DIR"/hack/test/kind/kind-cfg.yaml \
      --wait 1m --retain
    touch "$BATS_SUITE_TMPDIR/created-cluster"
  fi
  kind get kubeconfig --name "${CLUSTER_NAME}" > "$BATS_SUITE_TMPDIR/kubeconfig"
  export KUBECONFIG="$BATS_SUITE_TMPDIR/kubeconfig"

  # The image of the controller under test, built from the working tree.
  if [ -z "${SKIP_IMAGE_BUILD:-}" ]; then
    (cd "$ROOT_DIR" && IMG="${IMAGE_REPO}:${IMAGE_TAG}" PUSH=--load make image-build)
    kind load docker-image "${IMAGE_REPO}:${IMAGE_TAG}" --name "${CLUSTER_NAME}"
  fi
}

function teardown_suite {
  rm -rf "$ROOT_DIR"/_artifacts/logs || true
  kind export logs "$ROOT_DIR"/_artifacts/logs --name "${CLUSTER_NAME}" || true
  # Keep a cluster the suite didn't create, e.g. the one of `make run`.
  if [ -f "$BATS_SUITE_TMPDIR/created-cluster" ] && [ -z "${KEEP_CLUSTER:-}" ]; then
    kind delete cluster --name "${CLUSTER_NAME}"
  fi
}
