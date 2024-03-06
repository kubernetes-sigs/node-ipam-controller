package webhooks

import (
	"context"

	v1 "sigs.k8s.io/node-ipam-controller/pkg/apis/clustercidr/v1"
	testutil "sigs.k8s.io/node-ipam-controller/pkg/util/test"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
)

// Only test happy path and failure to make sure that the webhook is invoked.
// ClusterCIDR validation is actually tested in /pkg/api/v1/validation.

var _ = ginkgo.Describe("ClusterCIDRValidator", func() {
	ginkgo.It("should allow to create a valid ClusterCIDR", func(ctx context.Context) {
		clusterCIDR := testutil.MakeClusterCIDR(8, "test-clustercidr", "10.1.0.0/16", "", testutil.MakeNodeSelector("foo", corev1.NodeSelectorOpIn, []string{"bar"}))
		c := v1.ClusterCIDRList{}
		err := k8sClient.List(ctx, &c)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		ginkgo.By("creating clusterCIDR")
		gomega.Expect(k8sClient.Create(ctx, clusterCIDR)).To(gomega.Succeed())

		ginkgo.By("deleting clusterCIDR")
		gomega.Eventually(func() bool {
			err := k8sClient.Delete(ctx, clusterCIDR)
			return err == nil
		}).Should(gomega.BeTrue())
	})

	ginkgo.It("should allow to update a ClusterCIDR with no changes", func(ctx context.Context) {
		clusterCIDR := testutil.MakeClusterCIDR(8, "test-clustercidr", "10.1.0.0/16", "", testutil.MakeNodeSelector("foo", corev1.NodeSelectorOpIn, []string{"bar"}))
		ginkgo.By("creating clusterCIDR")
		gomega.Expect(k8sClient.Create(ctx, clusterCIDR)).To(gomega.Succeed())

		updateClusterCIDR := clusterCIDR.DeepCopy()
		ginkgo.By("updating clusterCIDR")
		gomega.Expect(k8sClient.Update(ctx, updateClusterCIDR)).To(gomega.Succeed())

		ginkgo.By("deleting clusterCIDR")
		gomega.Eventually(func() bool {
			err := k8sClient.Delete(ctx, clusterCIDR)
			return err == nil
		}).Should(gomega.BeTrue())
	})

	ginkgo.It("should reject ClusterCIDR creation with no IPv4 and IPv6", func(ctx context.Context) {
		clusterCIDR := testutil.MakeClusterCIDR(8, "test-clustercidr", "", "", nil)
		ginkgo.By("creating clusterCIDR")
		err := k8sClient.Create(ctx, clusterCIDR)
		gomega.Expect(err).To(gomega.HaveOccurred())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("one or both of `ipv4` and `ipv6` must be specified"))
	})

	ginkgo.It("should reject ClusterCIDR immutable spec.IPv4 update", func(ctx context.Context) {
		clusterCIDR := testutil.MakeClusterCIDR(8, "test-clustercidr", "10.1.0.0/16", "", testutil.MakeNodeSelector("foo", corev1.NodeSelectorOpIn, []string{"bar"}))
		ginkgo.By("creating clusterCIDR")
		gomega.Expect(k8sClient.Create(ctx, clusterCIDR)).To(gomega.Succeed())

		updateClusterCIDR := clusterCIDR.DeepCopy()
		updateClusterCIDR.Spec.IPv4 = "10.2.0.0/16"
		ginkgo.By("updating clusterCIDR")
		err := k8sClient.Update(ctx, updateClusterCIDR)
		gomega.Expect(err).To(gomega.HaveOccurred())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("field is immutable"))
	})
})
