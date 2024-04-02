# node-ipam-controller

Out of tree implementation of https://github.com/kubernetes/enhancements/tree/master/keps/sig-network/2593-multiple-cluster-cidrs

It allows users to use an ipam-controller that allocates IP ranges to Nodes, setting the node.spec.PodCIDRs fields.
The ipam-controller is configured via CRDs

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
