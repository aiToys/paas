// Package v1alpha1 包含平台控制面下发的 CRD 类型（Workload / DataService）。
//
// +kubebuilder:object:generate=true
// +groupName=core.aitoys.github.com
package v1alpha1

import (
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// GroupVersion 是 Workload CRD 的 group/version。
var GroupVersion = schema.GroupVersion{Group: "core.aitoys.github.com", Version: "v1alpha1"}

// SchemeBuilder 注册本 group 的类型。
var SchemeBuilder = runtime.NewSchemeBuilder(registerFuncs)

// AddToScheme 把本 group 类型注册进 scheme。
var AddToScheme = SchemeBuilder.AddToScheme

func registerFuncs(scheme *runtime.Scheme) error {
	scheme.AddKnownTypes(GroupVersion, &Workload{}, &WorkloadList{}, &DataService{}, &DataServiceList{})
	return nil
}
