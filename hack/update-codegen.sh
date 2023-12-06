#!/usr/bin/env bash

# Copyright 2017 The Kubernetes Authors.
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

set -o errexit
set -o nounset
set -o pipefail

PROJECT_ROOT=$(realpath $(dirname "${BASH_SOURCE[0]}")/..)

CODEGEN_VERSION=$(go list -m -f '{{.Version}}' k8s.io/code-generator)
CODEGEN_PKG=${CODEGEN_PKG:-$(go env GOPATH)"/pkg/mod/k8s.io/code-generator@${CODEGEN_VERSION}"}

cd $(dirname "${BASH_SOURCE[0]}")/..

source "${CODEGEN_PKG}/kube_codegen.sh"

ln -s ../.. github.com
trap "rm github.com" EXIT

kube::codegen::gen_helpers \
    --input-pkg-root github.com/mneverov/cluster-cidr-controller/pkg/apis \
    --output-base "${PROJECT_ROOT}" \
    --boilerplate "${PROJECT_ROOT}/hack/boilerplate.go.txt"

kube::codegen::gen_client \
    --with-watch \
    --input-pkg-root github.com/mneverov/cluster-cidr-controller/pkg/apis \
    --output-pkg-root github.com/mneverov/cluster-cidr-controller/pkg/client \
    --output-base "${PROJECT_ROOT}" \
    --boilerplate "${PROJECT_ROOT}/hack/boilerplate.go.txt"
