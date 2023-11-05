package webhooks

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/mneverov/cluster-cidr-controller/pkg/api/v1"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
)

// Only testing happy path and failure to make sure that the webhook is invoked.
// ClusterCIDR validation is actually tested in /pkg/api/v1/validation.

var _ = Describe("ClusterCIDRValidator", func() {
	It("should allow to create a valid ClusterCIDR", func(ctx context.Context) {
		clusterCIDR := makeClusterCIDR(8, "10.1.0.0/16", "", makeNodeSelector("foo", corev1.NodeSelectorOpIn, []string{"bar"}))
		By("creating clusterCIDR")
		Expect(k8sClient.Create(ctx, clusterCIDR)).To(Succeed())

		By("deleting clusterCIDR")
		Eventually(func() bool {
			err := k8sClient.Delete(ctx, clusterCIDR)
			return err == nil
		}, timeout, interval).Should(BeTrue())
	})

	It("should allow to update a ClusterCIDR with no changes", func(ctx context.Context) {
		clusterCIDR := makeClusterCIDR(8, "10.1.0.0/16", "", makeNodeSelector("foo", corev1.NodeSelectorOpIn, []string{"bar"}))
		By("creating clusterCIDR")
		Expect(k8sClient.Create(ctx, clusterCIDR)).To(Succeed())

		updateClusterCIDR := clusterCIDR.DeepCopy()
		By("updating clusterCIDR")
		Expect(k8sClient.Update(ctx, updateClusterCIDR)).To(Succeed())

		By("deleting clusterCIDR")
		Eventually(func() bool {
			err := k8sClient.Delete(ctx, clusterCIDR)
			return err == nil
		}, timeout, interval).Should(BeTrue())
	})

	It("should reject ClusterCIDR creation with no IPv4 and IPv6", func(ctx context.Context) {
		clusterCIDR := makeClusterCIDR(8, "", "", nil)
		By("creating clusterCIDR")
		err := k8sClient.Create(ctx, clusterCIDR)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("one or both of `ipv4` and `ipv6` must be specified"))
	})

	It("should reject ClusterCIDR immutable spec.IPv4 update", func(ctx context.Context) {
		clusterCIDR := makeClusterCIDR(8, "10.1.0.0/16", "", makeNodeSelector("foo", corev1.NodeSelectorOpIn, []string{"bar"}))
		By("creating clusterCIDR")
		Expect(k8sClient.Create(ctx, clusterCIDR)).To(Succeed())

		updateClusterCIDR := clusterCIDR.DeepCopy()
		updateClusterCIDR.Spec.IPv4 = "10.2.0.0/16"
		By("updating clusterCIDR")
		err := k8sClient.Update(ctx, updateClusterCIDR)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("field is immutable"))
	})
})

func makeClusterCIDR(perNodeHostBits int32, ipv4, ipv6 string, nodeSelector *corev1.NodeSelector) *v1.ClusterCIDR {
	return &v1.ClusterCIDR{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-clustercidr",
		},
		Spec: v1.ClusterCIDRSpec{
			PerNodeHostBits: perNodeHostBits,
			IPv4:            ipv4,
			IPv6:            ipv6,
			NodeSelector:    nodeSelector,
		},
	}
}

func makeNodeSelector(key string, op corev1.NodeSelectorOperator, values []string) *corev1.NodeSelector {
	return &corev1.NodeSelector{
		NodeSelectorTerms: []corev1.NodeSelectorTerm{{
			MatchExpressions: []corev1.NodeSelectorRequirement{{
				Key:      key,
				Operator: op,
				Values:   values,
			}},
		}},
	}
}
