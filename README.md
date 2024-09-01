# node-ipam-controller

Out of tree implementation of https://github.com/kubernetes/enhancements/tree/master/keps/sig-network/2593-multiple-cluster-cidrs

It allows users to use an ipam-controller that allocates IP ranges to Nodes, setting the node.spec.PodCIDRs fields.
The ipam-controller is configured via CRDs

## Config

* `apiserver` - The address of the Kubernetes API server. Overrides any value in kubeconfig. Only required if out-of-cluster. (Env: `IPAM_API_SERVER_URL`)
* `kubeconfig` - Path to a kubeconfig. Only required if out-of-cluster. (Env: `IPAM_KUBECONFIG`)
* `health-probe-address` - Specifies the TCP address for the health server to listen on. (Env: `IPAM_HEALTH_PROBE_ADDR`)
* `enable-leader-election` - Enable leader election for the controller manager. Ensures there is only one active controller manager. (Env: `IPAM_ENABLE_LEADER_ELECTION`)
* `leader-elect-lease-duration` - Duration that non-leader candidates will wait to force acquire leadership (duration string). (Env: `IPAM_LEASE_DURATION`)
* `leader-elect-renew-deadline` - Interval between attempts by the acting master to renew a leadership slot before it stops leading (duration string). (Env: `IPAM_RENEW_DEADLINE`)
* `leader-elect-retry-period` - Duration the clients should wait between attempting acquisition and renewal of a leadership (duration string). (Env: `IPAM_RESOURCE_LOCK`)
* `leader-elect-resource-lock` - The type of resource object that is used for locking. Supported options are 'leases', 'endpoints', 'configmaps'. (Env: `IPAM_RESOURCE_LOCK_NAME`)
* `leader-elect-resource-name` - The name of the resource object that is used for locking. (Env: `IPAM_RESOURCE_NAME`)

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

Create a Kind cluster with disabled Node CIDRs allocation:

```sh
kind create cluster --config hack/test/kind/kind-cfg.yaml
```

Install ClusterCIDR CRD and configure node-ipam-controller to use dual mode (See the [examples](examples) folder for 
more examples):

```sh
kubectl create -f charts/node-ipam-controller/gen/crds/networking.x-k8s.io_clustercidrs.yaml
kubectl create -f examples/clustercidr-dual.yaml
```

Run the controller outside the cluster by specifying Kind cluster kubeconfig:

```sh
./bin/manager --kubeconfig="$HOME"/.kube/config
```

To run the controller inside the cluster, a Docker image must first be loaded into a registry accessible within the Kind cluster.

```sh
docker build -t registry.k8s.io/node-ipam-controller/node-ipam-controller:local -f Dockerfile .
docker save --output node-ipam-controller.tar registry.k8s.io/node-ipam-controller/node-ipam-controller:local
kind load docker-image registry.k8s.io/node-ipam-controller/node-ipam-controller:local
```

Check Kind [documentation](https://kind.sigs.k8s.io/docs/user/local-registry/) on how to use local container image registry.

Install node-ipam-controller in the cluster via helm:

```sh
helm install node-ipam-controller ./charts/node-ipam-controller --create-namespace --namespace nodeipam --set image.tag=local
```

