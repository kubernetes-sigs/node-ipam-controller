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
	"os"
	"time"

	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/component-base/logs"

	"sigs.k8s.io/node-ipam-controller/pkg/leaderelection"
	"sigs.k8s.io/node-ipam-controller/pkg/signals"
	"sigs.k8s.io/node-ipam-controller/pkg/util/server"

	"github.com/jessevdk/go-flags"
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

type config struct {
	ApiServerURL string `long:"apiserver" description:"The address of the Kubernetes API server. Overrides any value in kubeconfig. Only required if out-of-cluster." env:"IPAM_API_SERVER_URL"`
	Kubeconfig   string `long:"kubeconfig" description:"Path to a kubeconfig. Only required if out-of-cluster." env:"IPAM_KUBECONFIG"`
	// deprecated, use BindingAddr. Will be removed in future release.
	HealthProbeAddr   string `long:"health-probe-address" default:"" description:"Specifies the TCP address for the health server to listen on." env:"IPAM_HEALTH_PROBE_ADDR"`
	WebserverBindAddr string `long:"webserver-bind-address" default:":8081" description:"Specifies the TCP address for the probes and metric server to listen on." env:"IPAM_WEBSERVER_BIND_ADDR"`
	LeaderElectionCfg leaderelection.Config
}

func (c *config) load() error {
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

	nodeIpamCfg := config{LeaderElectionCfg: leaderelection.Config{
		EnableLeaderElection: true,
	}}
	err := nodeIpamCfg.load()
	if err != nil {
		var flagError *flags.Error
		if errors.As(err, &flagError) {
			if flagError.Type == flags.ErrHelp {
				os.Exit(0)
			}
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "unable to parse config: %q", err)
		os.Exit(1)
	}

	logs.InitLogs()
	if err := logsapi.ValidateAndApply(c, nil); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(signals.SetupSignalHandler())
	defer cancel()
	logger := klog.FromContext(ctx)

	server.StartWebServer(ctx, bindingAddress(nodeIpamCfg))

	kubeClientCfg, err := clientcmd.BuildConfigFromFlags(nodeIpamCfg.ApiServerURL, nodeIpamCfg.Kubeconfig)
	if err != nil {
		logger.Error(err, "failed to build kubeconfig")
		klog.FlushAndExit(klog.ExitFlushTimeout, 1)
	}

	kubeClient, err := kubernetes.NewForConfig(kubeClientCfg)
	if err != nil {
		logger.Error(err, "failed to build kubernetes clientset")
		klog.FlushAndExit(klog.ExitFlushTimeout, 1)
	}

	if nodeIpamCfg.LeaderElectionCfg.EnableLeaderElection {
		logger.Info("Leader election is enabled.")
		leaderelection.StartLeaderElection(
			ctx, kubeClient, nodeIpamCfg.LeaderElectionCfg, cancel, runControllers(kubeClient, kubeClientCfg),
		)
	} else {
		logger.Info("Leader election is disabled.")
		runControllers(kubeClient, kubeClientCfg)(ctx)
	}
}

// runControllers creates a function that starts Node Ipam Controller.
func runControllers(kubeClient kubernetes.Interface, cfg *rest.Config) func(context.Context) {
	return func(ctx context.Context) {
		logger := klog.FromContext(ctx)
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
}

func bindingAddress(cfg config) string {
	if cfg.HealthProbeAddr != "" {
		return cfg.HealthProbeAddr
	}

	return cfg.WebserverBindAddr
}
