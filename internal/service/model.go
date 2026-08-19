// Package service 是服务实体（用户声明的服务定义）。
// 应用 → 服务 → 环境：服务是用户心智的一等实体，部署（Workload）是服务 × 环境 × 泳道的实例化。
package service

import (
	"errors"
	"fmt"
	"regexp"
	"time"
)

// 服务类型。web=有对外域名的前端入口服务；backend=后端 API；
// agent=AI Agent（注入模型/工具配置）；static=静态站点（StaticSite，不产 Workload）；cron=定时任务。
const (
	TypeWeb     = "web"
	TypeBackend = "backend"
	TypeAgent   = "agent"
	TypeStatic  = "static"
	TypeCron    = "cron"
)

var validTypes = map[string]bool{TypeWeb: true, TypeBackend: true, TypeAgent: true, TypeStatic: true, TypeCron: true}

// Sentinel 错误。
var (
	ErrNotFound = errors.New("service not found")
	ErrExists   = errors.New("service already exists")
	ErrInvalid  = errors.New("invalid service")
)

var nameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{1,62}$`) // DNS-1035（作 K8s 资源名前缀）

// Service 是应用内一个服务的声明式定义（不含运行态）。
type Service struct {
	ID        string    `json:"id"`
	TenantID  string    `json:"tenantId,omitempty"` // ctx 写入，请求体忽略
	AppID     string    `json:"appId"`
	Name      string    `json:"name"`               // 应用内唯一，DNS-1035
	Type      string    `json:"type"`               // web/backend/agent/static/cron
	RepoID    string    `json:"repoId,omitempty"`   // 关联 CodeRepo（static 可空）
	RepoPath  string    `json:"repoPath,omitempty"` // 仓库内路径（monorepo 多服务）
	Port      int       `json:"port,omitempty"`     // web/backend/agent 对外端口（0=不建 Service）
	Replicas  int       `json:"replicas,omitempty"` // 期望副本（部署默认值）
	CreatedAt time.Time `json:"createdAt"`
	// BuildArgs 是多服务构建参数（如 SERVICE=bff），部署/构建时注入流水线。
	BuildArgs map[string]string `json:"buildArgs,omitempty"`
	// Env 是服务级环境变量（部署时与 appconfig 合并注入）。
	Env map[string]string `json:"env,omitempty"`
	// ModelRef 是 agent 类型绑定的模型 ID。
	ModelRef string `json:"modelRef,omitempty"`
	// Tools 是 agent 类型的 MCP 工具名列表。
	Tools []string `json:"tools,omitempty"`
	// Schedule 是 cron 类型的 cron 表达式。
	Schedule string `json:"schedule,omitempty"`
}

// Validate 校验服务字段：type/name/appId 必填且合法；cron 须有 schedule；static 不需要 Port。
func (s Service) Validate() error {
	if !validTypes[s.Type] {
		return fmt.Errorf("%w: type", ErrInvalid)
	}
	if s.AppID == "" {
		return fmt.Errorf("%w: appId", ErrInvalid)
	}
	if !nameRe.MatchString(s.Name) {
		return fmt.Errorf("%w: name 须为小写字母数字连字符（DNS-1035）", ErrInvalid)
	}
	if s.Type == TypeCron && s.Schedule == "" {
		return fmt.Errorf("%w: cron 类型须填 schedule", ErrInvalid)
	}
	return nil
}
