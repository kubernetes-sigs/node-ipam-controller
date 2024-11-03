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
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
	"k8s.io/klog/v2"
)

// Config holds the configuration parameters for leader election
type Config struct {
	LeaseDuration time.Duration
	RenewDeadline time.Duration
	RetryPeriod   time.Duration
	ResourceLock  string
	ResourceName  string
}

// StartLeaderElection starts the leader election process
func StartLeaderElection(ctx context.Context, kubeClient kubernetes.Interface, cfg *rest.Config, logger klog.Logger, cancel context.CancelFunc, runFunc func(ctx context.Context, kubeClient kubernetes.Interface, cfg *rest.Config, logger klog.Logger), config Config) {
	id := os.Getenv("POD_NAME")
	if id == "" {
		klog.Fatalf("POD_NAME environment variable not set")
	}

	namespace := os.Getenv("POD_NAMESPACE")
	if namespace == "" {
		klog.Fatalf("POD_NAMESPACE environment variable not set")
	}

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
				runFunc(ctx, kubeClient, cfg, logger)
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
