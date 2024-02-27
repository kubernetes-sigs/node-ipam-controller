package ipam

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	v1 "sigs.k8s.io/node-ipam-controller/pkg/apis/clustercidr/v1"
	clientset "sigs.k8s.io/node-ipam-controller/pkg/client/clientset/versioned"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"github.com/onsi/gomega/format"
	"go.uber.org/zap/zapcore"
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
	ginkgo.RunSpecs(t, "Node IPAM Controller Suite")
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
		CRDDirectoryPaths:     []string{filepath.Join("../../..", "charts", "node-ipam-controller", "gen", "crds")},
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
