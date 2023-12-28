package ipam

import (
	"context"
	"net"
	"time"

	v1 "github.com/mneverov/cluster-cidr-controller/pkg/apis/clustercidr/v1"
	clustercidrclient "github.com/mneverov/cluster-cidr-controller/pkg/client/clientset/versioned/typed/clustercidr/v1"
	clustercidrinformers "github.com/mneverov/cluster-cidr-controller/pkg/client/informers/externalversions"
	clustercidrinformersv1 "github.com/mneverov/cluster-cidr-controller/pkg/client/informers/externalversions/clustercidr/v1"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubeinformers "k8s.io/client-go/informers"
	informers "k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/envtest/komega"
)

const (
	timeout  = 2 * time.Second
	interval = 100 * time.Millisecond
	resync   = 1 * time.Hour
)

var _ = ginkgo.Describe("Pod CIDRs", ginkgo.Ordered, func() {
	ginkgo.BeforeAll(func() {
		ctx, cancel = context.WithTimeout(context.Background(), 15*time.Second)

		gomega.SetDefaultConsistentlyDuration(timeout)
		gomega.SetDefaultConsistentlyPollingInterval(interval)
		gomega.SetDefaultEventuallyTimeout(timeout)
		gomega.SetDefaultEventuallyPollingInterval(interval)

		kubeInformerFactory := kubeinformers.NewSharedInformerFactory(kubeClient, resync)
		sharedInformerFactory := clustercidrinformers.NewSharedInformerFactory(cidrClient, resync)

		ipamController := booststrapMultiCIDRRangeAllocator(
			ctx,
			kubeClient,
			cidrClient.NetworkingV1().ClusterCIDRs(),
			kubeInformerFactory.Core().V1().Nodes(),
			sharedInformerFactory.Networking().V1().ClusterCIDRs(),
		)

		go ipamController.Run(ctx)
		sharedInformerFactory.Start(ctx.Done())
		kubeInformerFactory.Start(ctx.Done())

		ginkgo.DeferCleanup(ginkgo.GinkgoRecover)
		komega.SetClient(k8sClient)
		komega.SetContext(ctx)
	})
	ginkgo.AfterAll(func() { cancel() })

	ginkgo.DescribeTable("should allocate Pod CIDRs",
		func(clusterCIDR *v1.ClusterCIDR, node *corev1.Node, expectedPodCIDRs []string) {
			if clusterCIDR != nil {
				ginkgo.By("creating a clusterCIDR")
				_, err := cidrClient.NetworkingV1().ClusterCIDRs().Create(ctx, clusterCIDR, metav1.CreateOptions{})
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
			}

			ginkgo.By("creating a node")
			_, err := kubeClient.CoreV1().Nodes().Create(ctx, node, metav1.CreateOptions{})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Eventually(komega.Object(node)).Should(gomega.WithTransform(func(n *corev1.Node) []string {
				return n.Spec.PodCIDRs
			}, gomega.Equal(expectedPodCIDRs)))
		},
		ginkgo.Entry("Default dualstack Pod CIDRs assigned to a node, node labels matching no ClusterCIDR nodeSelectors",
			nil,
			makeNode("default-node", map[string]string{"label": "unmatched"}),
			[]string{"10.96.0.0/24", "fd00:10:96::/120"},
		),
		ginkgo.Entry("Dualstack Pod CIDRs assigned to a node from a CC created during bootstrap",
			nil,
			makeNode("bootstrap-node", map[string]string{"bootstrap": "true"}),
			[]string{"10.2.1.0/24", "fd00:20:96::100/120"},
		),
		ginkgo.Entry("Single stack IPv4 Pod CIDR assigned to a node",
			makeClusterCIDR("ipv4-cc", "10.0.0.0/16", "", 8, nodeSelector(map[string][]string{"ipv4": {"true"}, "singlestack": {"true"}})),
			makeNode("ipv4-node", map[string]string{"ipv4": "true", "singlestack": "true"}),
			[]string{"10.0.0.0/24"},
		),
		ginkgo.Entry("Single stack IPv6 Pod CIDR assigned to a node",
			makeClusterCIDR("ipv6-cc", "", "fd00:20:100::/112", 8, nodeSelector(map[string][]string{"ipv6": {"true"}})),
			makeNode("ipv6-node", map[string]string{"ipv6": "true"}),
			[]string{"fd00:20:100::/120"},
		),
		ginkgo.Entry("DualStack Pod CIDRs assigned to a node",
			makeClusterCIDR("dualstack-cc", "192.168.0.0/16", "fd00:30:100::/112", 8, nodeSelector(map[string][]string{"ipv4": {"true"}, "ipv6": {"true"}})),
			makeNode("dualstack-node", map[string]string{"ipv4": "true", "ipv6": "true"}),
			[]string{"192.168.0.0/24", "fd00:30:100::/120"},
		),
	)
})

func booststrapMultiCIDRRangeAllocator(
	ctx context.Context,
	client *kubernetes.Clientset,
	networkClient clustercidrclient.ClusterCIDRInterface,
	nodeInformer informers.NodeInformer,
	clusterCIDRInformer clustercidrinformersv1.ClusterCIDRInformer,
) CIDRAllocator {
	_, clusterCIDRv4, _ := net.ParseCIDR("10.96.0.0/12")     // allows up to 8K nodes
	_, clusterCIDRv6, _ := net.ParseCIDR("fd00:10:96::/112") // allows up to 8K nodes
	_, serviceCIDR, _ := net.ParseCIDR("10.94.0.0/24")       // does not matter for test - pick upto  250 services
	_, secServiceCIDR, _ := net.ParseCIDR("2001:db2::/120")  // does not matter for test - pick upto  250 services

	// order is ipv4 - ipv6 by convention for dual stack
	clusterCIDRs := []*net.IPNet{clusterCIDRv4, clusterCIDRv6}
	nodeMaskCIDRs := []int{24, 120}

	// set the current state of the informer, we can pre-seed nodes and ClusterCIDRs, so that we
	// can simulate the bootstrap
	initialCC := makeClusterCIDR("initial-cc", "10.2.0.0/16", "fd00:20:96::/112", 8, nodeSelector(map[string][]string{"bootstrap": {"true"}}))
	_, err := networkClient.Create(ctx, initialCC, metav1.CreateOptions{})
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	initialNode := makeNode("initial-node", map[string]string{"bootstrap": "true"})
	_, err = client.CoreV1().Nodes().Create(ctx, initialNode, metav1.CreateOptions{})
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	nodes, err := client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	allocatorParams := CIDRAllocatorParams{
		ClusterCIDRs:         clusterCIDRs,
		ServiceCIDR:          serviceCIDR,
		SecondaryServiceCIDR: secServiceCIDR,
		NodeCIDRMaskSizes:    nodeMaskCIDRs,
	}

	ipamController, err := NewMultiCIDRRangeAllocator(
		ctx,
		client,
		networkClient,
		nodeInformer,
		clusterCIDRInformer,
		allocatorParams,
		nodes,
		nil,
	)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	return ipamController
}

func makeNode(name string, labels map[string]string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: labels,
		},
		Status: corev1.NodeStatus{
			Capacity: corev1.ResourceList{
				corev1.ResourcePods:   *resource.NewQuantity(10, resource.DecimalSI),
				corev1.ResourceCPU:    resource.MustParse("1"),
				corev1.ResourceMemory: resource.MustParse("1Gi"),
			},
			Phase: corev1.NodeRunning,
			Conditions: []corev1.NodeCondition{
				{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
			},
		},
	}
}

func nodeSelector(labels map[string][]string) *corev1.NodeSelector {
	testNodeSelector := &corev1.NodeSelector{}

	for key, values := range labels {
		nst := corev1.NodeSelectorTerm{
			MatchExpressions: []corev1.NodeSelectorRequirement{
				{
					Key:      key,
					Operator: corev1.NodeSelectorOpIn,
					Values:   values,
				},
			},
		}
		testNodeSelector.NodeSelectorTerms = append(testNodeSelector.NodeSelectorTerms, nst)
	}

	return testNodeSelector
}
