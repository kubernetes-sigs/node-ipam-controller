package webhooks

import (
	"context"

	v1 "github.com/mneverov/cluster-cidr/pkg/api/v1"
	"github.com/mneverov/cluster-cidr/pkg/api/v1/validation"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

var clusterCidrLogger = logf.Log.WithName("cronjob-resource")

var _ webhook.Defaulter = &v1.ClusterCIDR{}

// ClusterCIDRValidator validates a ClusterCIDR.
type ClusterCIDRValidator struct{}

// SetupWithManager registers the webhook.
func (v *ClusterCIDRValidator) SetupWithManager(mgr manager.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(&v1.ClusterCIDR{}).
		WithValidator(v).
		Complete()
}

// +kubebuilder:webhook:groups=cluster.cidr.x-k8s.io,versions=v1,resources=clustercidrs,verbs=create;update;delete,name=validate-clustercidr.cluster.cidr.x-k8s.io,path=/validate-cluster-cidr-x-k8s-io-v1-clustercidr,mutating=false,failurePolicy=fail,sideEffects=None,admissionReviewVersions=v1

// ValidateCreate implements webhook.CustomValidator.
func (v *ClusterCIDRValidator) ValidateCreate(_ context.Context, obj runtime.Object) (warnings admission.Warnings, err error) {
	clusterCIDR := obj.(*v1.ClusterCIDR)
	clusterCidrLogger.Info("validate create", "name", clusterCIDR.Name)

	validationErrorList := validation.ValidateClusterCIDR(clusterCIDR)

	return nil, convertToError(clusterCIDR.Name, validationErrorList)
}

// ValidateUpdate implements webhook.CustomValidator.
func (v *ClusterCIDRValidator) ValidateUpdate(_ context.Context, oldObj, newObj runtime.Object) (warnings admission.Warnings, err error) {
	oldClusterCIDR := oldObj.(*v1.ClusterCIDR)
	newClusterCIDR := newObj.(*v1.ClusterCIDR)
	clusterCidrLogger.Info("validate create", "name", newClusterCIDR.Name)

	validationErrorList := validation.ValidateClusterCIDR(newClusterCIDR)
	updateErrorList := validation.ValidateClusterCIDRUpdate(newClusterCIDR, oldClusterCIDR)
	validationErrorList = append(validationErrorList, updateErrorList...)

	return nil, convertToError(newClusterCIDR.Name, validationErrorList)
}

// ValidateDelete implements webhook.CustomValidator.
func (v *ClusterCIDRValidator) ValidateDelete(_ context.Context, obj runtime.Object) (warnings admission.Warnings, err error) {
	clusterCIDR := obj.(*v1.ClusterCIDR)
	clusterCidrLogger.Info("validate create", "name", clusterCIDR.Name)
	return nil, nil
}

func convertToError(name string, errors field.ErrorList) error {
	if len(errors) == 0 {
		return nil
	}

	return apierrors.NewInvalid(
		schema.GroupKind{Group: "cluster.cidr.x-k8s.io", Kind: "ClusterCIDR"},
		name, errors)
}
