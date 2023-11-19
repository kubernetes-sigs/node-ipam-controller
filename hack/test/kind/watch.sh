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

# Print and execute a simple command
pe "# watching control-plane node CIDR"

PROMPT_TIMEOUT=0
wait

# Wait until the user presses enter
pei "watch \" kl get node kind-control-plane -ojson | jq -r '{cidr: .spec.podCIDR, cidrs: .spec.podCIDRs}'\""
