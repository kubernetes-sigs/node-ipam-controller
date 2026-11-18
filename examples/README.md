# Example files

There are several example yaml files in this folder, each of them configures
the node-ipam-controller to handle IPv4, IPv6, or both IP families.

Use below commands to create / delete one of the example:

```sh
kubectl create -f clustercidr-dual.yaml
...
kubectl delete -f clustercidr-dual.yaml
```

The `nodeSelector` of a ClusterCIDR follows the same rules as a Pod's node
affinity: the terms under `nodeSelectorTerms` are ORed, and the requirements
within a term are ANDed. `matchExpressions` match node labels, and
`matchFields` match the node name (`metadata.name`). A ClusterCIDR without a
`nodeSelector` applies to every node, after the ClusterCIDRs that select the
node. When several ClusterCIDRs select a node, the one whose matching term has
the most requirements is used first.

Note that to delete a ClusterCIDR, the corresponding nodes need to be deleted first as they are still using the CIDR
range. This ensures that no network conflicts occur after the deletion.

Note that ClusterCIDRs are immutable.

