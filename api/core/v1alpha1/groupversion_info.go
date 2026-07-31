// Package v1alpha1 包含平台控制面下发的 CRD 类型（Workload / DataService）。
//
// +kubebuilder:object:generate=true
// +groupName=core.aitoys.github.com
package v1alpha1

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

// GroupVersion 是本组 CRD 的 group/version。
var GroupVersion = schema.GroupVersion{Group: "core.aitoys.github.com", Version: "v1alpha1"}

// SchemeBuilder 注册本 group 的类型。用 controller-runtime 的 scheme.Builder（而非裸
// runtime.NewSchemeBuilder）：它会在 AddToScheme 时一并注册 metav1 参数类型（ListOptions
// 等）到本 GroupVersion，使 client list/watch 的 parameter codec 能识别本 GV——
// 否则真实集群 list 报 "ListOptions is not suitable for converting to core.aitoys/v1alpha1"
// （fake client 不走 parameter codec，故单元测试无法暴露此问题）。
var SchemeBuilder = &scheme.Builder{GroupVersion: GroupVersion}

// AddToScheme 把本 group 类型注册进 scheme。
var AddToScheme = SchemeBuilder.AddToScheme

func init() {
	SchemeBuilder.Register(&Workload{}, &WorkloadList{}, &DataService{}, &DataServiceList{})
}
