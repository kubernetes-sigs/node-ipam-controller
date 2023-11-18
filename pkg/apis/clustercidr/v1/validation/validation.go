/*
Copyright 2017 The Kubernetes Authors.
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

package validation

import (
	"fmt"

	"github.com/mneverov/cluster-cidr-controller/pkg/apis/clustercidr/v1"

	corev1 "k8s.io/api/core/v1"
	apimachineryvalidation "k8s.io/apimachinery/pkg/api/validation"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	unversionedvalidation "k8s.io/apimachinery/pkg/apis/meta/v1/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
	netutils "k8s.io/utils/net"
)

var (
	// validateClusterCIDRName validates that the given name can be used as an
	// ClusterCIDR name.
	validateClusterCIDRName = apimachineryvalidation.NameIsDNSLabel

	// validateNodeName can be used to check whether the given node name is valid.
	// Prefix indicates this name will be used as part of generation, in which case
	// trailing dashes are allowed.
	validateNodeName = apimachineryvalidation.NameIsDNSSubdomain
)

// ValidateClusterCIDR validates a ClusterCIDR.
func ValidateClusterCIDR(cc *v1.ClusterCIDR) field.ErrorList {
	allErrs := apimachineryvalidation.ValidateObjectMeta(&cc.ObjectMeta, false, validateClusterCIDRName, field.NewPath("metadata"))
	allErrs = append(allErrs, ValidateClusterCIDRSpec(&cc.Spec, field.NewPath("spec"))...)
	return allErrs
}

// ValidateClusterCIDRSpec validates ClusterCIDR Spec.
func ValidateClusterCIDRSpec(spec *v1.ClusterCIDRSpec, fldPath *field.Path) field.ErrorList {
	var allErrs field.ErrorList
	if spec.NodeSelector != nil {
		allErrs = append(allErrs, validateNodeSelector(spec.NodeSelector, fldPath.Child("nodeSelector"))...)
	}

	// Validate if CIDR is specified for at least one IP Family(IPv4/IPv6).
	if spec.IPv4 == "" && spec.IPv6 == "" {
		allErrs = append(allErrs, field.Required(fldPath, "one or both of `ipv4` and `ipv6` must be specified"))
		return allErrs
	}

	// Validate specified IPv4 CIDR and PerNodeHostBits.
	if spec.IPv4 != "" {
		allErrs = append(allErrs, validateCIDRConfig(spec.IPv4, spec.PerNodeHostBits, 32, corev1.IPv4Protocol, fldPath)...)
	}

	// Validate specified IPv6 CIDR and PerNodeHostBits.
	if spec.IPv6 != "" {
		allErrs = append(allErrs, validateCIDRConfig(spec.IPv6, spec.PerNodeHostBits, 128, corev1.IPv6Protocol, fldPath)...)
	}

	return allErrs
}

func validateCIDRConfig(configCIDR string, perNodeHostBits, maxMaskSize int32, ipFamily corev1.IPFamily, fldPath *field.Path) field.ErrorList {
	var allErrs field.ErrorList
	minPerNodeHostBits := int32(4)

	ip, ipNet, err := netutils.ParseCIDRSloppy(configCIDR)
	if err != nil {
		allErrs = append(allErrs, field.Invalid(fldPath.Child(string(ipFamily)), configCIDR, fmt.Sprintf("must be a valid CIDR: %s", configCIDR)))
		return allErrs
	}

	if ipFamily == corev1.IPv4Protocol && !netutils.IsIPv4(ip) {
		allErrs = append(allErrs, field.Invalid(fldPath.Child(string(ipFamily)), configCIDR, "must be a valid IPv4 CIDR"))
	}
	if ipFamily == corev1.IPv6Protocol && !netutils.IsIPv6(ip) {
		allErrs = append(allErrs, field.Invalid(fldPath.Child(string(ipFamily)), configCIDR, "must be a valid IPv6 CIDR"))
	}

	// Validate PerNodeHostBits
	maskSize, _ := ipNet.Mask.Size()
	maxPerNodeHostBits := maxMaskSize - int32(maskSize)

	if perNodeHostBits < minPerNodeHostBits {
		allErrs = append(allErrs, field.Invalid(fldPath.Child("perNodeHostBits"), perNodeHostBits, fmt.Sprintf("must be greater than or equal to %d", minPerNodeHostBits)))
	}
	if perNodeHostBits > maxPerNodeHostBits {
		allErrs = append(allErrs, field.Invalid(fldPath.Child("perNodeHostBits"), perNodeHostBits, fmt.Sprintf("must be less than or equal to %d", maxPerNodeHostBits)))
	}
	return allErrs
}

// ValidateClusterCIDRUpdate tests if an update to a ClusterCIDR is valid.
func ValidateClusterCIDRUpdate(update, old *v1.ClusterCIDR) field.ErrorList {
	var allErrs field.ErrorList
	allErrs = append(allErrs, apimachineryvalidation.ValidateObjectMetaUpdate(&update.ObjectMeta, &old.ObjectMeta, field.NewPath("metadata"))...)
	allErrs = append(allErrs, validateClusterCIDRUpdateSpec(&update.Spec, &old.Spec, field.NewPath("spec"))...)
	return allErrs
}

func validateClusterCIDRUpdateSpec(update, old *v1.ClusterCIDRSpec, fldPath *field.Path) field.ErrorList {
	var allErrs field.ErrorList

	allErrs = append(allErrs, apimachineryvalidation.ValidateImmutableField(update.NodeSelector, old.NodeSelector, fldPath.Child("nodeSelector"))...)
	allErrs = append(allErrs, apimachineryvalidation.ValidateImmutableField(update.PerNodeHostBits, old.PerNodeHostBits, fldPath.Child("perNodeHostBits"))...)
	allErrs = append(allErrs, apimachineryvalidation.ValidateImmutableField(update.IPv4, old.IPv4, fldPath.Child("ipv4"))...)
	allErrs = append(allErrs, apimachineryvalidation.ValidateImmutableField(update.IPv6, old.IPv6, fldPath.Child("ipv6"))...)

	return allErrs
}

// validateNodeSelector tests that the specified nodeSelector fields has valid data.
func validateNodeSelector(nodeSelector *corev1.NodeSelector, fldPath *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}

	termFldPath := fldPath.Child("nodeSelectorTerms")
	if len(nodeSelector.NodeSelectorTerms) == 0 {
		return append(allErrs, field.Required(termFldPath, "must have at least one node selector term"))
	}

	for i, term := range nodeSelector.NodeSelectorTerms {
		allErrs = append(allErrs, validateNodeSelectorTerm(term, termFldPath.Index(i))...)
	}

	return allErrs
}

// validateNodeSelectorTerm tests that the specified node selector term has valid data.
func validateNodeSelectorTerm(term corev1.NodeSelectorTerm, fldPath *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}

	for j, req := range term.MatchExpressions {
		allErrs = append(allErrs, ValidateNodeSelectorRequirement(req, fldPath.Child("matchExpressions").Index(j))...)
	}

	for j, req := range term.MatchFields {
		allErrs = append(allErrs, validateNodeFieldSelectorRequirement(req, fldPath.Child("matchFields").Index(j))...)
	}

	return allErrs
}

// ValidateNodeSelectorRequirement tests that the specified NodeSelectorRequirement fields has valid data.
func ValidateNodeSelectorRequirement(rq corev1.NodeSelectorRequirement, fldPath *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}
	switch rq.Operator {
	case corev1.NodeSelectorOpIn, corev1.NodeSelectorOpNotIn:
		if len(rq.Values) == 0 {
			allErrs = append(allErrs, field.Required(fldPath.Child("values"), "must be specified when `operator` is 'In' or 'NotIn'"))
		}
	case corev1.NodeSelectorOpExists, corev1.NodeSelectorOpDoesNotExist:
		if len(rq.Values) > 0 {
			allErrs = append(allErrs, field.Forbidden(fldPath.Child("values"), "may not be specified when `operator` is 'Exists' or 'DoesNotExist'"))
		}

	case corev1.NodeSelectorOpGt, corev1.NodeSelectorOpLt:
		if len(rq.Values) != 1 {
			allErrs = append(allErrs, field.Required(fldPath.Child("values"), "must be specified single value when `operator` is 'Lt' or 'Gt'"))
		}
	default:
		allErrs = append(allErrs, field.Invalid(fldPath.Child("operator"), rq.Operator, "not a valid selector operator"))
	}

	allErrs = append(allErrs, unversionedvalidation.ValidateLabelName(rq.Key, fldPath.Child("key"))...)

	return allErrs
}

// validateNodeFieldSelectorRequirement tests that the specified NodeSelectorRequirement fields has valid data.
func validateNodeFieldSelectorRequirement(req corev1.NodeSelectorRequirement, fldPath *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}

	switch req.Operator {
	case corev1.NodeSelectorOpIn, corev1.NodeSelectorOpNotIn:
		if len(req.Values) != 1 {
			allErrs = append(allErrs, field.Required(fldPath.Child("values"),
				"must be only one value when `operator` is 'In' or 'NotIn' for node field selector"))
		}
	default:
		allErrs = append(allErrs, field.Invalid(fldPath.Child("operator"), req.Operator, "not a valid selector operator"))
	}

	if vf, found := nodeFieldSelectorValidators[req.Key]; !found {
		allErrs = append(allErrs, field.Invalid(fldPath.Child("key"), req.Key, "not a valid field selector key"))
	} else {
		for i, v := range req.Values {
			for _, msg := range vf(v, false) {
				allErrs = append(allErrs, field.Invalid(fldPath.Child("values").Index(i), v, msg))
			}
		}
	}

	return allErrs
}

var nodeFieldSelectorValidators = map[string]func(string, bool) []string{
	metav1.ObjectNameField: validateNodeName,
}
