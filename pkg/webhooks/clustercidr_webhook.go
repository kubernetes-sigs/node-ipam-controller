package webhooks

import (
	"context"

	"sigs.k8s.io/node-ipam-controller/pkg/apis/clustercidr"
	v1 "sigs.k8s.io/node-ipam-controller/pkg/apis/clustercidr/v1"
	"sigs.k8s.io/node-ipam-controller/pkg/apis/clustercidr/v1/validator"

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

var clusterCidrLogger = logf.Log.WithName("clustercidr-resource")

// ClusterCIDRValidator validates a ClusterCIDR.
type ClusterCIDRValidator struct{}

// SetupWebhookWithManager registers the webhook.
func (v *ClusterCIDRValidator) SetupWebhookWithManager(mgr manager.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(&v1.ClusterCIDR{}).
		WithValidator(v).
		Complete()
}

var _ webhook.CustomValidator = &ClusterCIDRValidator{}

//+kubebuilder:webhook:groups=networking.x-k8s.io,versions=v1,resources=clustercidrs,verbs=create;update,name=validate-clustercidr.networking.x-k8s.io,path=/validate-networking-x-k8s-io-v1-clustercidr,mutating=false,failurePolicy=fail,sideEffects=None,admissionReviewVersions=v1

// ValidateCreate implements webhook.CustomValidator.
func (v *ClusterCIDRValidator) ValidateCreate(_ context.Context, obj runtime.Object) (warnings admission.Warnings, err error) {
	clusterCIDR := obj.(*v1.ClusterCIDR)
	clusterCidrLogger.Info("validate create", "name", clusterCIDR.Name)

	validationErrorList := validator.ValidateClusterCIDR(clusterCIDR)

	return nil, convertToError(clusterCIDR.Name, validationErrorList)
}

// ValidateUpdate implements webhook.CustomValidator.
func (v *ClusterCIDRValidator) ValidateUpdate(_ context.Context, oldObj, newObj runtime.Object) (warnings admission.Warnings, err error) {
	oldClusterCIDR := oldObj.(*v1.ClusterCIDR)
	newClusterCIDR := newObj.(*v1.ClusterCIDR)
	clusterCidrLogger.Info("validate create", "name", newClusterCIDR.Name)

	validationErrorList := validator.ValidateClusterCIDR(newClusterCIDR)
	updateErrorList := validator.ValidateClusterCIDRUpdate(newClusterCIDR, oldClusterCIDR)
	validationErrorList = append(validationErrorList, updateErrorList...)

	return nil, convertToError(newClusterCIDR.Name, validationErrorList)
}

// ValidateDelete implements webhook.CustomValidator.
func (v *ClusterCIDRValidator) ValidateDelete(_ context.Context, obj runtime.Object) (warnings admission.Warnings, err error) {
	return nil, nil
}

func convertToError(name string, errors field.ErrorList) error {
	if len(errors) == 0 {
		return nil
	}

	return apierrors.NewInvalid(
		schema.GroupKind{Group: clustercidr.GroupName, Kind: "ClusterCIDR"},
		name, errors)
}
