#!/bin/bash

# Copyright 2021 The Kubernetes Authors.
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

BINDIR="/usr/local/bin"

function install_binaries {
    echo "$#"
    [ $# -eq 2 ]
    if_error_exit "install_binaries: $# Wrong number of arguments to ${FUNCNAME[0]}"

    [ -d "${BINDIR}" ]
    if_error_exit "Directory \"${BINDIR}\" does not exist"

    local k8s_version="${1}"
    local kind_version="${2}"

    setup_kind "${kind_version}"
    setup_kubectl "${k8s_version}"
    setup_test_binaries "${k8s_version}"
}

function setup_kind {
    [ $# -eq 1 ]
    if_error_exit "setup_kubectl: $# Wrong number of arguments to ${FUNCNAME[0]}"

    local kind_version="${1}"

    local tmp_file
    tmp_file="$(mktemp -q)"
    if_error_exit "Could not create temp file, mktemp failed"

    echo "Downloading kind [${kind_version}]..."
    status_code=$(curl -w "%{http_code}" -L https://kind.sigs.k8s.io/dl/"${kind_version}"/kind-linux-amd64 -o "${tmp_file}")
    if [ "${status_code}"  != "200" ]; then
      echo "failed to download kind. Status code: ${status_code}"
      exit 1
    fi
    if_error_exit "cannot download kind"

    sudo mv "${tmp_file}" "${BINDIR}"/kind
    sudo chmod +x "${BINDIR}"/kind

    echo "The kind tool is set."
}

function setup_kubectl {
    [ $# -eq 1 ]
    if_error_exit "setup_kubectl: $# Wrong number of arguments to ${FUNCNAME[0]}"

    local k8s_version="${1}"

    local tmp_file
    tmp_file="$(mktemp -q)"
    if_error_exit "Could not create temp file, mktemp failed"

    echo "Downloading kubectl [${k8s_version}]..."
    status_code=$(curl -w "%{http_code}" -L https://dl.k8s.io/"${k8s_version}"/bin/linux/amd64/kubectl -o "${tmp_file}")
    if [ "${status_code}"  != "200" ]; then
      echo "failed to download kubectl. Status code: ${status_code}"
      exit 1
    fi
    if_error_exit "cannot download kubectl"

    sudo mv "${tmp_file}" "${BINDIR}"/kubectl
    sudo chmod +x "${BINDIR}"/kubectl

    echo "The kubectl tool is set."
}

function setup_test_binaries {
    [ $# -eq 1 ]
    if_error_exit "setup_test_binaries $# Wrong number of arguments to ${FUNCNAME[0]}"

    local k8s_version="${1}"

    local temp_directory
    temp_directory="$(mktemp -qd)"
    if_error_exit "Could not create temp directory, mktemp failed"

    echo "Downloading ginkgo and e2e.test [${k8s_version}]..."
    status_code=$(curl -w "%{http_code}" -L https://dl.k8s.io/release/"${k8s_version}"/kubernetes-test-linux-amd64.tar.gz -o "${temp_directory}"/kubernetes-test-linux-amd64.tar.gz)
    if [ "${status_code}"  != "200" ]; then
      echo "failed to download e2e.test and ginkgo. Status code: ${status_code}"
      exit 1
    fi
    if_error_exit "cannot download kubernetes-test package"

    tar xvzf "${temp_directory}"/kubernetes-test-linux-amd64.tar.gz \
        --directory "${temp_directory}" \
        --strip-components=3 kubernetes/test/bin/ginkgo kubernetes/test/bin/e2e.test
    if_error_exit "failed to untar kubernetes-test"

    sudo cp "${temp_directory}"/ginkgo /usr/local/bin/ginkgo
    sudo cp "${temp_directory}"/e2e.test /usr/local/bin/e2e.test
    rm -rf "${temp_directory}"
    sudo chmod +x "${BINDIR}/ginkgo"
    sudo chmod +x "${BINDIR}/e2e.test"

    echo "The tools ginko and e2e.test are set."
}

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

if [ "$#" -lt 2 ]; then
    echo "Error: Provide k8s and kind version."
    exit 1
fi

install_binaries "${1}" "${2}"


