/*
Copyright 2023 The Kubernetes Authors.

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
package leaderelection

import (
	"context"
	"os"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
	nodeutil "k8s.io/component-helpers/node/util"
	"k8s.io/klog/v2"
)

// Config holds the configuration parameters for leader election
type Config struct {
	EnableLeaderElection    bool          `long:"enable-leader-election" description:"Enable leader election for the controller manager. Ensures there is only one active controller manager." env:"IPAM_ENABLE_LEADER_ELECTION"`
	LeaderElectionID        string        `long:"leader-elect-id" description:"The name of the resource that leader election will use for holding the leader lock." env:"IPAM_LEADER_ELECT_ID"`
	LeaderElectionNamespace string        `long:"leader-elect-namespace" description:"The namespace in which the leader election resource will be created." env:"IPAM_LEADER_ELECT_NAMESPACE"`
	LeaseDuration           time.Duration `long:"leader-elect-lease-duration" default:"15s" description:"Duration that non-leader candidates will wait to force acquire leadership (duration string)." env:"IPAM_LEASE_DURATION"`
	RenewDeadline           time.Duration `long:"leader-elect-renew-deadline" default:"10s" description:"Interval between attempts by the acting master to renew a leadership slot before it stops leading (duration string)." env:"IPAM_RENEW_DEADLINE"`
	RetryPeriod             time.Duration `long:"leader-elect-retry-period" default:"2s" description:"Duration the clients should wait between attempting acquisition and renewal of a leadership (duration string)." env:"IPAM_LEADER_ELECT_RETRY_PERIOD"`
	ResourceLock            string        `long:"leader-elect-resource-lock" default:"leases" description:"The type of resource object that is used for locking. Supported options are 'leases', 'endpoints', 'configmaps'." env:"IPAM_RESOURCE_LOCK_NAME"`
	ResourceName            string        `long:"leader-elect-resource-name" default:"node-ipam-controller" description:"The name of the resource object that is used for locking." env:"IPAM_RESOURCE_NAME"`
}

// StartLeaderElection starts the leader election process
func StartLeaderElection(
	ctx context.Context, kubeClient kubernetes.Interface, config Config,
	cancel context.CancelFunc, runFunc func(context.Context),
) {
	id := lockID(config.LeaderElectionID)
	namespace := lockNamespace(config.LeaderElectionNamespace)
	klog.Infof("leader election id: %s, namespace: %s", id, namespace)

	rl, err := resourcelock.New(
		config.ResourceLock,
		namespace,
		config.ResourceName,
		kubeClient.CoreV1(),
		kubeClient.CoordinationV1(),
		resourcelock.ResourceLockConfig{
			Identity: id,
		},
	)
	if err != nil {
		klog.Fatalf("failed to create leader election lock: %v", err)
	}

	leaderelection.RunOrDie(ctx, leaderelection.LeaderElectionConfig{
		Lock:            rl,
		LeaseDuration:   config.LeaseDuration,
		RenewDeadline:   config.RenewDeadline,
		RetryPeriod:     config.RetryPeriod,
		ReleaseOnCancel: true,
		Callbacks: leaderelection.LeaderCallbacks{
			OnStartedLeading: func(ctx context.Context) {
				klog.Infof("Started leading as %s", id)
				runFunc(ctx)
			},
			OnStoppedLeading: func() {
				klog.Infof("%s stopped leading", id)
				// Instead of exiting, cancel the context to trigger the shutdown sequence
				cancel()
			},
			OnNewLeader: func(identity string) {
				if identity == id {
					klog.Infof("I am the new leader: %s", id)
				} else {
					klog.Infof("New leader elected: %s", identity)
				}
			},
		},
	})
}

func lockID(leaderElectionID string) string {
	if len(leaderElectionID) > 0 {
		return leaderElectionID
	}

	id := os.Getenv("POD_NAME")
	hostname, err := nodeutil.GetHostname(id)
	if err != nil {
		klog.Fatalf("failed to get leader election id: %s", err)
	}
	id = hostname

	return id
}

func lockNamespace(leaderElectionNamespace string) string {
	if len(leaderElectionNamespace) > 0 {
		return leaderElectionNamespace
	}
	ns := os.Getenv("POD_NAMESPACE")
	if ns == "" {
		klog.Fatalf("leader election namespace should be provided")
	}

	return ns
}
