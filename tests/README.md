# End-to-end tests

The tests run against a [kind](https://kind.sigs.k8s.io/) cluster with
[bats](https://bats-core.readthedocs.io/). They create fake Node objects (no
kubelet runs for them) and ClusterCIDRs, and check which Pod CIDRs the
controller allocates and when it lets a ClusterCIDR be deleted.

- `e2e_allocation.bats`: allocation and release after node labels, the
  ClusterCIDRs or the controller changed.
- `e2e_upgrade.bats`: upgrade from v0.3.0, the last release that converted node
  selectors to label selectors. Delete it once upgrades from v0.3.0 are no
  longer supported.

## Running

Requires `kind`, `kubectl`, `helm`, `docker` and `bats`.

```sh
bats -T --print-output-on-failure tests/
```

The suite creates the kind cluster `nodeipam-e2e`, builds the controller image
from the working tree, and deletes the cluster at the end. To use an existing
cluster, e.g. the one of `make run`, set `CLUSTER_NAME`; the suite keeps a
cluster it didn't create:

```sh
CLUSTER_NAME=node-ipam-controller-local-test-env bats -T tests/
```

Set `KEEP_CLUSTER=1` to keep the cluster the suite created. To test an image
that is loaded into the cluster already, e.g. one built from another commit,
set `IMAGE_TAG` and `SKIP_IMAGE_BUILD=1`:

```sh
SKIP_IMAGE_BUILD=1 IMAGE_TAG=my-build bats -T tests/e2e_allocation.bats
```

Run a single test with `--filter`:

```sh
bats -x --print-output-on-failure --filter "terminating" tests/e2e_allocation.bats
```
