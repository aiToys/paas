// Package application 是 Platform Core 的「应用」领域模型。
// 应用是统一控制台的主线抽象：各类资源（模型推理/MQ/DAL/治理）以绑定形式归属应用，
// 随应用生命周期联动。对齐前端 console-user 的 Application 契约。
package application

// ResourceCount 记录应用绑定的各类资源数量。
type ResourceCount struct {
	Models int `json:"models"`
	MQ     int `json:"mq"`
	DAL    int `json:"dal"`
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
	Resources ResourceCount `json:"resources"`
	Replicas  string        `json:"replicas"`
	RPS       string        `json:"rps"`
}
