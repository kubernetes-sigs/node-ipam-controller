# Node IPAM Controller

Manage Pod CIDR allocation for Nodes using the `ClusterCIDR` custom resource.

## About

The Node IPAM Controller implements [KEP-2593: Multiple ClusterCIDRs][kep-2593],
enabling declarative, flexible IP address management for Kubernetes clusters.
It watches Node and `ClusterCIDR` resources, allocates CIDR ranges, and sets
`node.spec.podCIDRs` on each Node.

[kep-2593]: https://github.com/kubernetes/enhancements/tree/master/keps/sig-network/2593-multiple-cluster-cidrs

## Motivation

The built-in Kubernetes IPAM controller supports only a single, cluster-wide
Pod CIDR range. This is limiting for clusters that need:

- **Different subnet sizes per node group** — allocate larger subnets to
  high-density worker nodes and smaller subnets to control plane nodes.
- **Multiple IP pools for the same node group** — expand Pod IP capacity
  without re-IPing existing nodes.
- **Dual-stack networking** — assign both IPv4 and IPv6 CIDRs to nodes
  from a single resource.

The `ClusterCIDR` CRD solves these problems by letting cluster operators define
multiple IP ranges with node selectors, giving fine-grained control over Pod IP
allocation.

## Key Features

- **Multiple CIDR ranges** — define as many `ClusterCIDR` resources as needed,
  each with its own IP pool and node selector.
- **IPv4, IPv6, and dual-stack** — first-class support for all addressing modes.
- **Node selector targeting** — assign different CIDR ranges to different node
  groups using label selectors.
- **Variable subnet sizes** — control per-node allocation size via
  `perNodeHostBits`.
- **Helm installation** — deploy with a single `helm install` command.

## Getting Started

### Prerequisites

- Kubernetes cluster v1.31+
- [Helm](https://helm.sh/) v3
- The cluster's built-in node IPAM controller must be disabled
  (`--allocate-node-cidrs=false` on kube-controller-manager)

### Installation

Install via Helm:

```sh
helm install node-ipam-controller ./charts/node-ipam-controller \
  --create-namespace \
  --namespace nodeipam
```

### Create a ClusterCIDR

Apply a `ClusterCIDR` resource to start allocating Pod CIDRs to nodes.

**Dual-stack example:**

```yaml
apiVersion: networking.x-k8s.io/v1
kind: ClusterCIDR
metadata:
  name: default
spec:
  perNodeHostBits: 8
  ipv4: 10.244.0.0/16
  ipv6: 2001:db8::/110
```

**IPv4-only:**

```yaml
apiVersion: networking.x-k8s.io/v1
kind: ClusterCIDR
metadata:
  name: default-ipv4
spec:
  perNodeHostBits: 8
  ipv4: 10.244.0.0/16
```

**Different subnets per node group:**

```yaml
apiVersion: networking.x-k8s.io/v1
kind: ClusterCIDR
metadata:
  name: controlplane-cidr
spec:
  perNodeHostBits: 7
  ipv4: 10.244.0.0/16
  nodeSelector:
    nodeSelectorTerms:
    - matchExpressions:
      - key: node-role.kubernetes.io/control-plane
        operator: Exists
---
apiVersion: networking.x-k8s.io/v1
kind: ClusterCIDR
metadata:
  name: worker-large-cidr
spec:
  perNodeHostBits: 10
  ipv4: 10.244.0.0/16
  nodeSelector:
    nodeSelectorTerms:
    - matchExpressions:
      - key: node-role.kubernetes.io/worker-large
        operator: Exists
```

More examples are available in the [`examples/`](./examples) directory.

## Configuration

The controller accepts the following flags (each has an `IPAM_` prefixed
environment variable override):

| Flag                          | Env Variable                   | Default              | Description                                                    |
|-------------------------------|--------------------------------|----------------------|----------------------------------------------------------------|
| `apiserver`                   | `IPAM_API_SERVER_URL`          |                      | Kubernetes API server address (only if out-of-cluster).        |
| `kubeconfig`                  | `IPAM_KUBECONFIG`              |                      | Path to kubeconfig (only if out-of-cluster).                   |
| `webserver-bind-address`      | `IPAM_WEBSERVER_BIND_ADDR`     | `:8081`              | Address for the health probe and metrics server.               |
| `enable-leader-election`      | `IPAM_ENABLE_LEADER_ELECTION`  | `true`               | Enable leader election for high availability.                  |
| `leader-elect-lease-duration` | `IPAM_LEASE_DURATION`          | `15s`                | Duration non-leaders wait before force-acquiring leadership.   |
| `leader-elect-renew-deadline` | `IPAM_RENEW_DEADLINE`          | `10s`                | Interval for the leader to renew its lease.                    |
| `leader-elect-retry-period`   | `IPAM_LEADER_ELECT_RETRY_PERIOD`| `2s`                | Wait between leadership acquisition attempts.                  |
| `leader-elect-resource-lock`  | `IPAM_RESOURCE_LOCK_NAME`      | `leases`             | Resource type for leader election (`leases`, `endpoints`, `configmaps`). |
| `leader-elect-resource-name`  | `IPAM_RESOURCE_NAME`           | `node-ipam-controller`| Name of the leader election lock resource.                    |
| `leader-elect-id`             | `IPAM_LEADER_ELECT_ID`         |                      | Leader election ID. Falls back to `POD_NAME`, then hostname.  |
| `leader-elect-namespace`      | `IPAM_LEADER_ELECT_NAMESPACE`  |                      | Namespace for the leader election lock. Falls back to `POD_NAMESPACE`. |

## Development

### Build

```sh
make build          # Build binary to bin/node-ipam-controller
make image-build    # Build Docker image
```

### Run locally

```sh
make run            # Create kind cluster + run controller in-cluster
make setup-test-env # Create kind cluster with CRDs only (for out-of-cluster dev)
make teardown-test-env
```

When running outside the cluster, disable leader election
(`IPAM_ENABLE_LEADER_ELECTION=false`) or set `POD_NAME` and `POD_NAMESPACE`.

## Community, Discussion, and Support

This project is maintained under the [Kubernetes SIG Network][sig-network]
umbrella.

- Chat with us on the Kubernetes Slack in the
  [#sig-network](https://kubernetes.slack.com/messages/sig-network) channel
- Join the [SIG Network mailing list](https://groups.google.com/a/kubernetes.io/g/sig-network)

Pull requests and issues are welcome! See the [issue tracker] if you are
unsure where to start, especially issues labeled
[good first issue](https://github.com/kubernetes-sigs/node-ipam-controller/labels/good%20first%20issue)
and [help wanted](https://github.com/kubernetes-sigs/node-ipam-controller/labels/help%20wanted).

[sig-network]: https://github.com/kubernetes/community/tree/master/sig-network
[issue tracker]: https://github.com/kubernetes-sigs/node-ipam-controller/issues

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for details on how to get started.

## Code of Conduct

Participation in the Kubernetes community is governed by the
[Kubernetes Code of Conduct](code-of-conduct.md).

## License

Apache License 2.0 — see [LICENSE](LICENSE) for details.
