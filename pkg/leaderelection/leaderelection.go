package leaderelection

import (
	"context"
	"os"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
	"k8s.io/klog/v2"
)

// StartLeaderElection starts the leader election process
func StartLeaderElection(ctx context.Context, kubeClient kubernetes.Interface, runFunc func(ctx context.Context)) {
	id := os.Getenv("POD_NAME")
	if id == "" {
		klog.Fatalf("POD_NAME environment variable not set")
	}

	namespace := os.Getenv("POD_NAMESPACE")
	if namespace == "" {
		klog.Fatalf("POD_NAMESPACE environment variable not set")
	}

	rl, err := resourcelock.New(
		resourcelock.LeasesResourceLock,
		namespace,
		"node-ipam-controller",
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
		Lock:          rl,
		LeaseDuration: 15 * time.Second,
		RenewDeadline: 10 * time.Second,
		RetryPeriod:   2 * time.Second,
		Callbacks: leaderelection.LeaderCallbacks{
			OnStartedLeading: func(ctx context.Context) {
				klog.Info("Started leading")
				runFunc(ctx)
			},
			OnStoppedLeading: func() {
				klog.Info("Stopped leading")
				klog.FlushAndExit(klog.ExitFlushTimeout, 1)
			},
			OnNewLeader: func(identity string) {
				if identity == id {
					klog.Info("I am the new leader")
				} else {
					klog.Infof("New leader elected: %s", identity)
				}
			},
		},
	})
}
