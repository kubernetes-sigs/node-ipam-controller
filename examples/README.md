# Example files

There are several example yaml files in this folder, each of them configures
the node-ipam-controller to handle IPv4, IPv6, or both IP families.

Use below commands to create / delete one of the example:

```sh
kubectl create -f clustercidr-dual.yaml
...
kubectl delete -f clustercidr-dual.yaml
```

Note that to delete a ClusterCIDR, the corresponding nodes need to be deleted first as they are still using the CIDR
range. This ensures that no network conflicts occur after the deletion.

Note that ClusterCIDRs are immutable.

