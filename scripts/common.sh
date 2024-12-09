#!/usr/bin/env bash

# Copyright 2023 The Kubernetes Authors.
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

DEFAULT_KUBECONFIG_PATH="${SCRIPT_PATH}/../hack/test/kind/node-ipam-controller-local-test-env.yaml"

: ${CLUSTERCIDR_PATH:="${SCRIPT_PATH}/../examples/clustercidr-dual.yaml"}
: ${IPAM_ENABLE_LEADER_ELECTION:="false"}
: ${SKIP_CRDS_INSTALL:="false"}
: ${TEST_ENV_CLUSTER_NAME:="node-ipam-controller-local-test-env"}
: ${TEST_ENV_KUBECONFIG_PATH:="${DEFAULT_KUBECONFIG_PATH}"}
: ${QUIET_MODE:="--quiet"}
: ${IMG_TAG:="test"}

export KUBECONFIG="${TEST_ENV_KUBECONFIG_PATH}"
export IMG="registry.k8s.io/node-ipam-controller:${IMG_TAG}"


function help {
  printf "\n"
  printf "\tEnvironment variables:\n"
  printf "\tCLUSTERCIDR_PATH: if specified ClusterCIDR by the given path will be created, default=examples/clustercidr-dual.yaml\n"
  printf "\\tIPAM_ENABLE_LEADER_ELECTION: if specified ClusterCIDR by the given path will be created, default=examples/clustercidr-dual.yaml\n"
  printf "\tSKIP_CRDS_INSTALL: whether to skip CRDs installation, default=false\n"
  printf "\tTEST_ENV_CLUSTER_NAME: name of the kind cluster to create, default=node-ipam-controller-local-test-env\n"
  printf "\tTEST_ENV_KUBECONFIG_PATH: path to kubeconfig file, default=hack/test/kind/node-ipam-controller-local-test-env.yaml\n"
  exit 0
}

if [[ "$1" == "--help" ]]; then
  help
fi

function if_error_exit {
    if [ "$?" != "0" ]; then
        if [ -n "$1" ]; then
            RED="\033[91m"
            ENDCOLOR="\033[0m"
            echo -e "[ ${RED}FAILED${ENDCOLOR} ] ${1}"
        fi
        exit 1
    fi
}

function kind_delete_cluster {
  kind delete cluster --name "${TEST_ENV_CLUSTER_NAME}"
  if_error_exit "failed to delete local test cluster"
}

function kind_create_cluster {
  kind_delete_cluster

  kind create cluster \
    --name "${TEST_ENV_CLUSTER_NAME}" \
    --config "${SCRIPT_PATH}"/../hack/test/kind/kind-cfg.yaml
  if_error_exit "failed to create local test cluster"
}

function build_controller {
  echo "Building node-ipam-controller image"
  pushd "$SCRIPT_PATH"/.. > /dev/null || exit

  make image-build
  if_error_exit "failed to build image"
  popd > /dev/null || exit
}

function load_controller_image {
  echo "Loading node-ipam-controller image ${IMG} to the cluster ${TEST_ENV_CLUSTER_NAME}"
  CMD_KIND_LOAD_NODEIPAM_TEST_IMAGE=("kind load docker-image ${IMG} --name ${TEST_ENV_CLUSTER_NAME}")
  ${CMD_KIND_LOAD_NODEIPAM_TEST_IMAGE} &> /dev/null
  if_error_exit "error loading image to kind, command was: ${CMD_KIND_LOAD_NODEIPAM_TEST_IMAGE}"
}

# Check if kind is installed
[ -x "$(command -v kind)" ]
if_error_exit "kind is not installed"
