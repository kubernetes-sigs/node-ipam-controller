package main

import (
	"flag"
	"time"

	clientset "github.com/mneverov/cluster-cidr-controller/pkg/client/clientset/versioned"
	informers "github.com/mneverov/cluster-cidr-controller/pkg/client/informers/externalversions"
	"github.com/mneverov/cluster-cidr-controller/pkg/controller/ipam"
	"github.com/mneverov/cluster-cidr-controller/pkg/signals"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kubeinformers "k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"
)

const defaultResync = 30 * time.Second

var (
	apiServerURL string
	kubeconfig   string
)

func init() {
	flag.StringVar(&kubeconfig, "kubeconfig", "", "Path to a kubeconfig. Only required if out-of-cluster.")
	flag.StringVar(&apiServerURL, "apiserver", "", "The address of the Kubernetes API server. Overrides any value in kubeconfig. Only required if out-of-cluster.")
}

func main() {
	klog.InitFlags(nil)
	flag.Parse()

	ctx := signals.SetupSignalHandler()
	logger := klog.FromContext(ctx)

	cfg, err := clientcmd.BuildConfigFromFlags(apiServerURL, kubeconfig)
	if err != nil {
		logger.Error(err, "failed to build kubeconfig")
		klog.FlushAndExit(klog.ExitFlushTimeout, 1)
	}

	kubeClient, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		logger.Error(err, "failed to build kubernetes clientset")
		klog.FlushAndExit(klog.ExitFlushTimeout, 1)
	}

	cidrClient, err := clientset.NewForConfig(cfg)
	if err != nil {
		logger.Error(err, "failed to build kubernetes clientset")
		klog.FlushAndExit(klog.ExitFlushTimeout, 1)
	}

	kubeInformerFactory := kubeinformers.NewSharedInformerFactory(kubeClient, defaultResync)
	sharedInformerFactory := informers.NewSharedInformerFactory(cidrClient, defaultResync)

	nodes, err := kubeClient.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		logger.Error(err, "failed to list existing nodes")
		klog.FlushAndExit(klog.ExitFlushTimeout, 1)
	}

	cidrController, err := ipam.NewMultiCIDRRangeAllocator(ctx, kubeClient, cidrClient.NetworkingV1().ClusterCIDRs(),
		kubeInformerFactory.Core().V1().Nodes(),
		sharedInformerFactory.Networking().V1().ClusterCIDRs(),
		ipam.CIDRAllocatorParams{},
		nodes,
		nil,
	)
	if err != nil {
		logger.Error(err, "failed to create CIDR controller")
		klog.FlushAndExit(klog.ExitFlushTimeout, 1)
	}

	kubeInformerFactory.Start(ctx.Done())
	sharedInformerFactory.Start(ctx.Done())

	cidrController.Run(ctx)
}
