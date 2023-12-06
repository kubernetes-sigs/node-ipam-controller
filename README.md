# cluster-cidr-controller

Out of tree implementation of https://github.com/kubernetes/enhancements/tree/master/keps/sig-network/2593-multiple-cluster-cidrs

It allows users to use an ipam-controller that allocates IP ranges to Nodes, setting the node.spec.PodCIDRs fields
The ipam-controller is configured via CRDs
