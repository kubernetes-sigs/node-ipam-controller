package ipam

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	v1 "github.com/mneverov/cluster-cidr-controller/pkg/apis/clustercidr/v1"
	clientset "github.com/mneverov/cluster-cidr-controller/pkg/client/clientset/versioned"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"github.com/onsi/gomega/format"
	"go.uber.org/zap/zapcore"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

var (
	cfg        *rest.Config
	testEnv    *envtest.Environment
	ctx        context.Context
	cancel     context.CancelFunc
	k8sClient  client.Client
	cidrClient *clientset.Clientset
	kubeClient *kubernetes.Clientset
)

func TestAPIs(t *testing.T) {
	gomega.RegisterFailHandler(ginkgo.Fail)
	ginkgo.RunSpecs(t, "Reset Controller Suite")
}

var _ = ginkgo.BeforeSuite(func() {
	format.TruncatedDiff = false
	logf.SetLogger(zap.New(zap.WriteTo(ginkgo.GinkgoWriter), zap.UseDevMode(true), func(o *zap.Options) {
		o.TimeEncoder = func(t time.Time, pae zapcore.PrimitiveArrayEncoder) {
			pae.AppendString(t.UTC().Format(time.RFC3339Nano))
		}
	}))

	ginkgo.By("bootstrapping test environment")
	testEnv = &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("../../..", "charts", "cluster-cidr-controller", "gen", "crds")},
		ErrorIfCRDPathMissing: true,
	}

	var err error
	cfg, err = testEnv.Start()
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(k8sClient).NotTo(gomega.BeNil())
	cidrClient = clientset.NewForConfigOrDie(cfg)
	kubeClient = kubernetes.NewForConfigOrDie(cfg)

	err = v1.AddToScheme(scheme.Scheme)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
})

var _ = ginkgo.AfterSuite(func() {
	ginkgo.By("tearing down the test environment")
	err := testEnv.Stop()
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
})

// makeClusterCIDR returns a ClusterCIDR object.
func makeClusterCIDR(name, ipv4CIDR, ipv6CIDR string, perNodeHostBits int32, nodeSelector *corev1.NodeSelector) *v1.ClusterCIDR {
	return &v1.ClusterCIDR{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: v1.ClusterCIDRSpec{
			PerNodeHostBits: perNodeHostBits,
			IPv4:            ipv4CIDR,
			IPv6:            ipv6CIDR,
			NodeSelector:    nodeSelector,
		},
	}
}
