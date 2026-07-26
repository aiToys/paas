// Package application 是 Platform Core 的「应用」领域模型。
// 应用是统一控制台的主线抽象：各类资源（模型推理/MQ/DAL/治理）以绑定形式归属应用，
// 随应用生命周期联动。对齐前端 console-user 的 Application 契约。
package application

// ResourceCount 记录应用绑定的各类资源数量。
// 由 Bindings 派生（recount），不作为独立真源，避免计数与绑定项不一致。
type ResourceCount struct {
	Models int `json:"models"`
	MQ     int `json:"mq"`
	DAL    int `json:"dal"`
}

// Binding 是应用与单个资源的绑定关系。
// Type 取值：models / mq / dal / gov（服务治理）。
type Binding struct {
	Type string `json:"type"`
	Name string `json:"name"`
	Note string `json:"note,omitempty"` // 备注，如规格、副本等
}

// Application 是平台应用实体。
type Application struct {
	ID        string        `json:"id"`
	Name      string        `json:"name"`
	Initial   string        `json:"initial"`
	Env       string        `json:"env"`    // 生产 / 预发 / 开发
	Status    string        `json:"status"` // healthy / degraded / idle
	Gradient  string        `json:"gradient"`
	Desc      string        `json:"desc"`
	Resources ResourceCount `json:"resources"` // 派生：各类资源计数
	Bindings  []Binding     `json:"bindings"`  // 真源：具体绑定项
	Replicas  string        `json:"replicas"`
	RPS       string        `json:"rps"`
}

// Recount 根据 Bindings 重算 ResourceCount。
// 仅统计 models/mq/dal 三类（列表页展示维度）；gov 等其它类型不计入计数但仍保留在 Bindings 中。
func (a *Application) Recount() {
	a.Resources = ResourceCount{}
	for _, b := range a.Bindings {
		switch b.Type {
		case "models":
			a.Resources.Models++
		case "mq":
			a.Resources.MQ++
		case "dal":
			a.Resources.DAL++
		}
	}
}
