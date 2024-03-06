package webhooks

import (
	"context"
	"crypto/tls"
	"fmt"
	"github.com/onsi/gomega/format"
	"go.uber.org/zap/zapcore"
	"net"
	"path/filepath"
	"testing"
	"time"

	"sigs.k8s.io/node-ipam-controller/pkg/apis/clustercidr/v1"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
)

var (
	cfg       *rest.Config
	k8sClient client.Client
	testEnv   *envtest.Environment
	ctx       context.Context
	cancel    context.CancelFunc
)

func TestClusterCIDRValidation(t *testing.T) {
	gomega.RegisterFailHandler(ginkgo.Fail)
	ginkgo.RunSpecs(t, "ClusterCIDR Validation Suite")
}

var _ = ginkgo.BeforeSuite(func() {
	format.TruncatedDiff = false
	logf.SetLogger(zap.New(zap.WriteTo(ginkgo.GinkgoWriter), zap.UseDevMode(true), func(o *zap.Options) {
		o.TimeEncoder = func(t time.Time, pae zapcore.PrimitiveArrayEncoder) {
			pae.AppendString(t.UTC().Format(time.RFC3339Nano))
		}
	}))

	ctx, cancel = context.WithCancel(context.TODO())

	ginkgo.By("bootstrapping test environment")
	testEnv = &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("../..", "charts", "node-ipam-controller", "gen", "crds")},
		ErrorIfCRDPathMissing: true,
		WebhookInstallOptions: envtest.WebhookInstallOptions{
			Paths: []string{filepath.Join("../..", "charts", "node-ipam-controller", "gen", "webhooks")},
		},
	}

	var err error

	cfg, err = testEnv.Start()
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(cfg).NotTo(gomega.BeNil())

	scheme := runtime.NewScheme()
	err = v1.AddToScheme(scheme)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	err = admissionv1.AddToScheme(scheme)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	err = corev1.AddToScheme(scheme)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme})
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(k8sClient).NotTo(gomega.BeNil())

	webhookInstallOptions := &testEnv.WebhookInstallOptions
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme: scheme,
		WebhookServer: webhook.NewServer(webhook.Options{
			Host:    webhookInstallOptions.LocalServingHost,
			Port:    webhookInstallOptions.LocalServingPort,
			CertDir: webhookInstallOptions.LocalServingCertDir,
		}),
		LeaderElection: false,
		Metrics:        metricsserver.Options{BindAddress: "0"},
	})
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	err = (&ClusterCIDRValidator{}).SetupWebhookWithManager(mgr)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	go func() {
		defer ginkgo.GinkgoRecover()
		err = mgr.Start(ctx)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
	}()

	// wait for the webhook server to get ready
	dialer := &net.Dialer{Timeout: time.Second}
	addrPort := fmt.Sprintf("%s:%d", webhookInstallOptions.LocalServingHost, webhookInstallOptions.LocalServingPort)
	gomega.Eventually(func() error {
		conn, err := tls.DialWithDialer(dialer, "tcp", addrPort, &tls.Config{InsecureSkipVerify: true})
		if err != nil {
			return err
		}
		return conn.Close()
	}).Should(gomega.Succeed())
})

var _ = ginkgo.AfterSuite(func() {
	cancel()
	ginkgo.By("tearing down the test environment")
	err := testEnv.Stop()
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
})
