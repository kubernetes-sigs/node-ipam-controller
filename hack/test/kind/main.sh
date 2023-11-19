#!/usr/bin/env bash
# Include the magic
. /home/mneverov/Temp/demo-magic/demo-magic.sh


DIR="$( pwd )"

pushd "$DIR" >/dev/null 2>&1 || exit
trap "{ popd >/dev/null 2>&1; }" EXIT

cd hack/test/kind || exit

DEMO_PROMPT="${GREEN}➜ ${CYAN}\W ${COLOR_RESET}"

# Clear the screen before starting
clear

pe "# creating kind cluster with kube-controller-manager --allocate-node-cidrs=false"

pei "kind create cluster --config ./kind-cfg.yaml"
PROMPT_TIMEOUT=12
wait

# should be visible
popd >/dev/null 2>&1 || exit

pei "# create ClusterCIDR CRD"
PROMPT_TIMEOUT=1
wait
pei "kl create -f ./config/crd/networking.x-k8s.io_clustercidrs.yaml"
wait

pei "# create ClusterCIDR sample"
wait
pei "bat ./samples/clustercidr.yaml"
PROMPT_TIMEOUT=5
wait
pei "kl create -f ./samples/clustercidr.yaml"
PROMPT_TIMEOUT=1
wait

pei "#run clusterCIDR Controller"
wait
pei "./bin/manager --kubeconfig=/home/mneverov/.kube/config"

PROMPT_TIMEOUT=0
p ""
cmd
