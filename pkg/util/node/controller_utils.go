/*
Copyright 2016 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package node

import (
	"k8s.io/client-go/tools/record"

	v1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
)

// todo(mneverov): copied from k8s.io/kubernetes/pkg/controller/util/node
//  should probably be under /controller/util/node too?

// RecordNodeStatusChange records a event related to a node status change. (Common to lifecycle and ipam).
func RecordNodeStatusChange(logger klog.Logger, recorder record.EventRecorder, node *v1.Node, newStatus string) {
	ref := &v1.ObjectReference{
		APIVersion: "v1",
		Kind:       "Node",
		Name:       node.Name,
		UID:        node.UID,
		Namespace:  "",
	}
	logger.V(2).Info("Recording status change event message for node", "status", newStatus, "node", node.Name)
	// TODO: This requires a transaction, either both node status is updated
	//  and event is recorded or neither should happen, see issue #6055.
	recorder.Eventf(ref, v1.EventTypeNormal, newStatus, "Node %s status is now: %s", node.Name, newStatus)
}
