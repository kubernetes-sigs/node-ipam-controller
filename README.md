# node-ipam-controller

Out of tree implementation
of https://github.com/kubernetes/enhancements/tree/master/keps/sig-network/2593-multiple-cluster-cidrs

It allows users to use an ipam-controller that allocates IP ranges to Nodes, setting the node.spec.PodCIDRs fields.
The ipam-controller is configured via CRDs

## Config

| Command line                | Environment                 | Default              | Description                                                                                                          |
|-----------------------------|-----------------------------|----------------------|----------------------------------------------------------------------------------------------------------------------|
| apiserver                   | IPAM_API_SERVER_URL         |                      | The address of the Kubernetes API server. Overrides any value in kubeconfig. Only required if out-of-cluster.        |
| kubeconfig                  | IPAM_KUBECONFIG             |                      | Path to a kubeconfig. Only required if out-of-cluster.                                                               |
| webserver-bind-address      | IPAM_WEBSERVER_BIND_ADDR    | :8081                | Specifies the TCP address for the probes and metric server to listen on.                                             |
| enable-leader-election      | IPAM_ENABLE_LEADER_ELECTION | true                 | Enable leader election for the controller manager. Ensures there is only one active controller manager.              |
| leader-elect-lease-duration | IPAM_LEASE_DURATION         | 15s                  | Duration that non-leader candidates will wait to force acquire leadership (duration string).                         |
| leader-elect-renew-deadline | IPAM_RENEW_DEADLINE         | 10s                  | Interval between attempts by the acting master to renew a leadership slot before it stops leading (duration string). |
| leader-elect-retry-period   | IPAM_RESOURCE_LOCK          | 2s                   | Duration the clients should wait between attempting acquisition and renewal of a leadership (duration string).       |
| leader-elect-resource-lock  | IPAM_RESOURCE_LOCK_NAME     | leases               | The type of resource object that is used for locking. Supported options are 'leases', 'endpoints', 'configmaps'.     |
| leader-elect-resource-name  | IPAM_RESOURCE_NAME          | node-ipam-controller | The name of the resource object that is used for locking.                                                            |

## Build

To build the binary for node-ipam-controller:

```sh
make build
```

To build the Docker image for node-ipam-controller:

```sh
make image-build
```

## Run

### In Cluster

The following command runs a kind cluster, builds the controller, and install it in the cluster.
It also creates a [default ClusterCIDR](./examples/clustercidr-dual.yaml).

```sh
make run
```

### Outside Cluster

The following command runs a kind cluster and install CRDs.
NOTE: run the controller with leader election disabled (see `IPAM_ENABLE_LEADER_ELECTION`) or specify `POD_NAME` and
`POD_NAMESPACE` environment variables that are used as leader election ID and namespace.

```sh
make setup-test-env
```
