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

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/jessevdk/go-flags"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/component-base/logs"

	"sigs.k8s.io/node-ipam-controller/pkg/leaderelection"
	"sigs.k8s.io/node-ipam-controller/pkg/signals"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubeinformers "k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	logsapi "k8s.io/component-base/logs/api/v1"
	clientset "sigs.k8s.io/node-ipam-controller/pkg/client/clientset/versioned"
	informers "sigs.k8s.io/node-ipam-controller/pkg/client/informers/externalversions"
	"sigs.k8s.io/node-ipam-controller/pkg/controller/ipam"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	_ "k8s.io/component-base/logs/json/register"
	"k8s.io/klog/v2"
)

type Config struct {
	ApiServerURL         string        `long:"apiserver" description:"The address of the Kubernetes API server. Overrides any value in kubeconfig. Only required if out-of-cluster." env:"IPAM_API_SERVER_URL"`
	Kubeconfig           string        `long:"kubeconfig" description:"Path to a kubeconfig. Only required if out-of-cluster." env:"IPAM_KUBECONFIG"`
	HealthProbeAddr      string        `long:"health-probe-address" default:":8081" description:"Specifies the TCP address for the health server to listen on." env:"IPAM_HEALTH_PROBE_ADDR"`
	EnableLeaderElection bool          `long:"enable-leader-election" description:"Enable leader election for the controller manager. Ensures there is only one active controller manager." env:"IPAM_ENABLE_LEADER_ELECTION"`
	LeaseDuration        time.Duration `long:"leader-elect-lease-duration" default:"15s" description:"Duration that non-leader candidates will wait to force acquire leadership (duration string)." env:"IPAM_LEASE_DURATION"`
	RenewDeadline        time.Duration `long:"leader-elect-renew-deadline" default:"10s" description:"Interval between attempts by the acting master to renew a leadership slot before it stops leading (duration string)." env:"IPAM_RENEW_DEADLINE"`
	RetryPeriod          time.Duration `long:"leader-elect-retry-period" default:"2s" description:"Duration the clients should wait between attempting acquisition and renewal of a leadership (duration string)." env:"IPAM_RESOURCE_LOCK"`
	ResourceLock         string        `long:"leader-elect-resource-lock" default:"leases" description:"The type of resource object that is used for locking. Supported options are 'leases', 'endpoints', 'configmaps'." env:"IPAM_RESOURCE_LOCK_NAME"`
	ResourceName         string        `long:"leader-elect-resource-name" default:"node-ipam-controller" description:"The name of the resource object that is used for locking." env:"IPAM_RESOURCE_NAME"`
}

func (c *Config) Load() error {
	// allows using true/false in the parameters
	_, err := flags.NewParser(c, flags.Default|flags.AllowBoolValues).ParseArgs(os.Args)
	if err != nil {
		return err
	}
	return nil
}

func main() {
	c := logsapi.NewLoggingConfiguration()
	logsapi.AddGoFlags(c, flag.CommandLine)

	config := Config{EnableLeaderElection: true}
	err := config.Load()
	if err != nil {
		var flagError *flags.Error
		errors.As(err, &flagError)
		if flagError.Type == flags.ErrHelp {
			os.Exit(0)
		}
		os.Exit(22)
	}

	logs.InitLogs()
	if err := logsapi.ValidateAndApply(c, nil); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
	ctx, cancel := context.WithCancel(signals.SetupSignalHandler())
	defer cancel()
	logger := klog.FromContext(ctx)

	server := startHealthProbeServer(config.HealthProbeAddr, logger)
	cfg, err := clientcmd.BuildConfigFromFlags(config.ApiServerURL, config.Kubeconfig)
	if err != nil {
		logger.Error(err, "failed to build kubeconfig")
		klog.FlushAndExit(klog.ExitFlushTimeout, 1)
	}

	kubeClient, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		logger.Error(err, "failed to build kubernetes clientset")
		klog.FlushAndExit(klog.ExitFlushTimeout, 1)
	}

	if config.EnableLeaderElection {
		logger.Info("Leader election is enabled.")
		leaderelection.StartLeaderElection(ctx, kubeClient, cfg, logger, cancel, runControllers, leaderelection.Config{
			LeaseDuration: config.LeaseDuration,
			RenewDeadline: config.RenewDeadline,
			RetryPeriod:   config.RetryPeriod,
			ResourceLock:  config.ResourceLock,
			ResourceName:  config.ResourceName,
		})
	} else {
		logger.Info("Leader election is disabled.")
		runControllers(ctx, kubeClient, cfg, logger)
	}

	if err := server.Shutdown(ctx); err != nil {
		logger.Error(err, "failed to shut down health server")
	}
}

func runControllers(ctx context.Context, kubeClient kubernetes.Interface, cfg *rest.Config, logger klog.Logger) {
	cidrClient, err := clientset.NewForConfig(cfg)
	if err != nil {
		logger.Error(err, "failed to build kubernetes clientset")
		klog.FlushAndExit(klog.ExitFlushTimeout, 1)
	}

	const defaultResync = 30 * time.Second
	kubeInformerFactory := kubeinformers.NewSharedInformerFactory(kubeClient, defaultResync)
	sharedInformerFactory := informers.NewSharedInformerFactory(cidrClient, defaultResync)

	nodes, err := kubeClient.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		logger.Error(err, "failed to list existing nodes")
		klog.FlushAndExit(klog.ExitFlushTimeout, 1)
	}

	nodeIpamController, err := ipam.NewMultiCIDRRangeAllocator(
		ctx,
		kubeClient,
		cidrClient.NetworkingV1().ClusterCIDRs(),
		kubeInformerFactory.Core().V1().Nodes(),
		sharedInformerFactory.Networking().V1().ClusterCIDRs(),
		ipam.CIDRAllocatorParams{},
		nodes,
		nil,
	)
	if err != nil {
		logger.Error(err, "failed to create Node IPAM controller")
		klog.FlushAndExit(klog.ExitFlushTimeout, 1)
	}

	kubeInformerFactory.Start(ctx.Done())
	sharedInformerFactory.Start(ctx.Done())

	nodeIpamController.Run(ctx)
}

// startHealthProbeServer starts a web server that has two endpoints `/readyz` and `/healthz` and always responds
// 200 OK.
func startHealthProbeServer(addr string, logger klog.Logger) *http.Server {
	const defaultTimeout = 30 * time.Second
	mux := http.NewServeMux()
	server := &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  defaultTimeout,
		WriteTimeout: defaultTimeout,
		IdleTimeout:  defaultTimeout,
	}

	mux.Handle("/readyz", makeHealthHandler())
	mux.Handle("/healthz", makeHealthHandler())

	go func() {
		err := server.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error(err, "an error occurred after stopping the health server")
		}
	}()

	return server
}

// makeHealthHandler returns 200/OK when healthy.
func makeHealthHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()

		w.WriteHeader(http.StatusOK)
	}
}
