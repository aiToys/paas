// Package governance 是服务治理领域模型（平台能力横切）。
//
// 治理四件套（注册发现 / 配置中心 / API 网关 / 熔断）中，本包承载三件：
//   - 注册中心：服务定义（Service）+ 实例（Instance）
//   - API 网关：路由规则（Route）
//   - 熔断器：绑服务的熔断规则（CircuitBreaker）+ 即时评估
//
// 配置中心（运行时动态配置）因职责正交独立成 internal/configcenter 包。
// 服务治理是租户私有的横切能力，所有应用共享但不归属单一应用（即便服务定义挂靠某应用）。
//
// 与「资源中心（数据服务 Add-on）」「工作负载」正交：工作负载是应用自身运行形态，
// 服务治理是消费-提供解耦的发现基础设施。
//
// 本期进程内 mock：实例注册即 healthy，无真实健康检查；Heartbeat 仅更新时间戳。
// 真实数据面 SDK / Sidecar / K8s endpoints 接入（参考 zeus）留后续。
package governance

import "time"

// 服务协议。
const (
	ProtocolHTTP = "http"
	ProtocolGRPC = "grpc"
)

var validProtocols = map[string]struct{}{
	ProtocolHTTP: {},
	ProtocolGRPC: {},
}

// 实例状态。
const (
	StatusHealthy   = "healthy"   // 健康（可被发现）
	StatusUnhealthy = "unhealthy" // 不健康（发现时跳过）
)

var validStatus = map[string]struct{}{
	StatusHealthy:   {},
	StatusUnhealthy: {},
}

// LaneDefault 是基线泳道标识（与 workload.LaneDefault 语义一致）。
// 实例可带泳道标签；本期不实现路由，预留字段。
const LaneDefault = "default"

// Service 是租户内一个服务定义。挂靠应用（可选）+ 环境。
type Service struct {
	ID        string    `json:"id"`
	TenantID  string    `json:"tenantId,omitempty"` // ctx 写入，请求体忽略
	Name      string    `json:"name"`               // 服务名，租户内唯一
	AppID     string    `json:"appId,omitempty"`    // 归属应用（可选）
	EnvID     string    `json:"envId"`              // 归属环境
	Protocol  string    `json:"protocol"`           // http | grpc
	Port      int       `json:"port"`               // 服务端口
	Desc      string    `json:"desc,omitempty"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Validate 校验服务：name/envID/protocol 合法、port > 0。
func (s Service) Validate() error {
	if s.Name == "" {
		return errInvalid("name")
	}
	if s.EnvID == "" {
		return errInvalid("envId")
	}
	if _, ok := validProtocols[s.Protocol]; !ok {
		return errInvalid("protocol")
	}
	if s.Port <= 0 {
		return errInvalid("port")
	}
	return nil
}

// Instance 是服务的一个运行点（发现的最小单元）。
type Instance struct {
	ID        string            `json:"id"`
	TenantID  string            `json:"tenantId,omitempty"` // ctx 写入，请求体忽略
	ServiceID string            `json:"serviceId"`
	Addr      string            `json:"addr"`           // host:port
	Status    string            `json:"status"`         // healthy | unhealthy
	LaneID    string            `json:"laneId"`         // "default"=基线；其他=泳道（预留）
	Meta      map[string]string `json:"meta,omitempty"` // 扩展点（版本/权重等）
	UpdatedAt time.Time         `json:"updatedAt"`
}

// Validate 校验实例：serviceID/addr/status 合法。
func (i Instance) Validate() error {
	if i.ServiceID == "" {
		return errInvalid("serviceId")
	}
	if i.Addr == "" {
		return errInvalid("addr")
	}
	if i.Status != "" {
		if _, ok := validStatus[i.Status]; !ok {
			return errInvalid("status")
		}
	}
	return nil
}

type fieldErr struct{ field string }

func (e fieldErr) Error() string { return "字段非法或缺失: " + e.field }

func errInvalid(field string) error { return fieldErr{field: field} }

// HTTP 方法（路由允许的请求方法）。
const (
	MethodGet    = "GET"
	MethodPost   = "POST"
	MethodPut    = "PUT"
	MethodDelete = "DELETE"
	MethodAny    = "ANY" // 任意方法
)

var validMethods = map[string]struct{}{
	MethodGet: {}, MethodPost: {}, MethodPut: {}, MethodDelete: {}, MethodAny: {},
}

// ValidMethod 校验方法合法。
func ValidMethod(m string) bool {
	_, ok := validMethods[m]
	return ok
}

// Route 是 API 网关路由规则（治理四件套之 API 网关）。
// 将入口路径转发到目标服务（与 Service 解耦的逻辑配置，不绑定物理环境）。
type Route struct {
	ID        string    `json:"id"`
	TenantID  string    `json:"tenantId,omitempty"` // ctx 写入，请求体忽略
	Name      string    `json:"name"`               // 租户内唯一
	Path      string    `json:"path"`               // 入口路径，如 /api/v1/chat/*
	ServiceID string    `json:"serviceId"`          // 目标服务
	Methods   []string  `json:"methods"`            // GET/POST/PUT/DELETE/ANY
	StripPath bool      `json:"stripPath"`          // 转发时剥离前缀
	Enabled   bool      `json:"enabled"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Validate 校验路由：Name/Path/ServiceID 非空、Methods 合法且非空。
func (r Route) Validate() error {
	if r.Name == "" {
		return errInvalid("name")
	}
	if r.Path == "" {
		return errInvalid("path")
	}
	if r.ServiceID == "" {
		return errInvalid("serviceId")
	}
	if len(r.Methods) == 0 {
		return errInvalid("methods")
	}
	for _, m := range r.Methods {
		if !ValidMethod(m) {
			return errInvalid("methods")
		}
	}
	return nil
}

// 熔断策略。
const (
	StrategyErrorRate = "error_rate" // 错误率（5xx/异常占比）
	StrategySlowCall  = "slow_call"  // 慢调用率（超阈值耗时占比）
)

var validStrategies = map[string]struct{}{
	StrategyErrorRate: {},
	StrategySlowCall:  {},
}

// 熔断状态。
const (
	StateClosed   = "closed"    // 放行（健康）
	StateOpen     = "open"      // 熔断（拒绝请求）
	StateHalfOpen = "half-open" // 半开（探测恢复中）
)

// CircuitBreaker 是绑服务的熔断规则（治理四件套之熔断器）。
// 状态(State)不持久化——由 EvaluateBreaker 即时评估推导（无真实流量采集，
// 用确定性 hash + 时间桶模拟窗口统计），与 metrics/alerts 的惰性评估风格一致。
type CircuitBreaker struct {
	ID          string    `json:"id"`
	TenantID    string    `json:"tenantId,omitempty"` // ctx 写入，请求体忽略
	Name        string    `json:"name"`               // 租户内唯一
	ServiceID   string    `json:"serviceId"`          // 目标服务
	Strategy    string    `json:"strategy"`           // error_rate | slow_call
	Threshold   int       `json:"threshold"`          // 触发阈值百分比 0-100
	MinRequests int       `json:"minRequests"`        // 窗口最少样本数（不足不熔断）
	WindowSecs  int       `json:"windowSecs"`         // 统计窗口秒
	Enabled     bool      `json:"enabled"`
	UpdatedAt   time.Time `json:"updatedAt"`

	// 以下由 handler 在返回前即时评估填充（非持久化）。
	State string      `json:"state,omitempty"`
	Stats WindowStats `json:"stats,omitempty"`
}

// WindowStats 是统计窗口内的调用样本（由 EvaluateBreaker 即时生成）。
type WindowStats struct {
	Requests  int `json:"requests"`  // 窗口总请求数
	Failures  int `json:"failures"`  // 失败请求数（error_rate 策略用）
	SlowCalls int `json:"slowCalls"` // 慢调用数（slow_call 策略用）
	Rate      int `json:"rate"`      // 当前指标百分比（失败率或慢调用率）
}

// Validate 校验熔断器：Name/ServiceID/Strategy 合法、Threshold(0,100]、MinRequests>0、WindowSecs>0。
func (b CircuitBreaker) Validate() error {
	if b.Name == "" {
		return errInvalid("name")
	}
	if b.ServiceID == "" {
		return errInvalid("serviceId")
	}
	if _, ok := validStrategies[b.Strategy]; !ok {
		return errInvalid("strategy")
	}
	if b.Threshold <= 0 || b.Threshold > 100 {
		return errInvalid("threshold")
	}
	if b.MinRequests <= 0 || b.MinRequests > 10000 {
		// 上限防 windowStatsFor 整数运算溢出；熔断窗口样本过万无业务意义。
		return errInvalid("minRequests")
	}
	if b.WindowSecs <= 0 {
		return errInvalid("windowSecs")
	}
	return nil
}

// EvaluateBreaker 是纯函数：基于 breaker 配置 + 当前时间即时推导 (Stats, State)。
//
// 不接真实流量采集，用 FNV-1a(b.ID + 时间桶) 确定性生成窗口统计，保证：
//   - 同一 now 下状态稳定（可重入、可测）
//   - 跨时间桶变化（"看起来实时"）
//   - 无 goroutine，测试可控（与 metrics/logs/traces 惰性模式一致）
//
// 三态规则：
//   - 样本不足（Requests < MinRequests）→ closed
//   - 当前 rate >= Threshold → open（熔断）
//   - 当前 rate < Threshold 但上一窗口已超阈值 → half-open（探测恢复中）
//   - 否则 → closed
//
// 真实数据面（Sidecar/SDK 上报滑动窗口计数）接入后，stats 从采集数据取，本函数仅保留状态机。
func EvaluateBreaker(b CircuitBreaker, now time.Time) (WindowStats, string) {
	if !b.Enabled {
		return WindowStats{}, StateClosed
	}
	stats := windowStatsFor(b, now)
	if stats.Requests < b.MinRequests {
		return stats, StateClosed
	}
	if stats.Rate >= b.Threshold {
		return stats, StateOpen
	}
	// 当前窗口健康：检查上一窗口是否已熔断，决定 half-open 探测态。
	prev := windowStatsFor(b, now.Add(-time.Duration(b.WindowSecs)*time.Second))
	if prev.Requests >= b.MinRequests && prev.Rate >= b.Threshold {
		return stats, StateHalfOpen
	}
	return stats, StateClosed
}

// windowStatsFor 按 breaker.ID + 时间桶确定性生成窗口统计。
// 时间桶 = now.Unix() / WindowSecs，使同一窗口内结果稳定，跨窗口演变。
func windowStatsFor(b CircuitBreaker, now time.Time) WindowStats {
	bucket := now.Unix() / int64(b.WindowSecs)
	// 主 hash：驱动 requests；副 hash：驱动 rate。
	h1 := fnvHash(b.ID, bucket)
	h2 := fnvHash(b.ID+"|rate", bucket)
	// requests 范围 [0, MinRequests*2+20)：可低于 MinRequests（样本不足→closed），
	// 也可高于（进入阈值判定），使三态 + 不足态都能在跨桶演变中出现。
	span := uint64(b.MinRequests*2 + 20) //nolint:gosec // MinRequests 已被 Validate 限制在 (0,10000]，运算无溢出
	if span <= 0 {
		span = 20
	}
	requests := int(h1 % span) //nolint:gosec // span 上界约 20020，转 int 无溢出
	rate := int(h2 % 100)      // [0,100)
	ws := WindowStats{Requests: requests, Rate: rate}
	switch b.Strategy {
	case StrategyErrorRate:
		ws.Failures = requests * rate / 100
	case StrategySlowCall:
		ws.SlowCalls = requests * rate / 100
	}
	return ws
}

// fnvHash FNV-1a 64bit，混合字符串与整数桶。
func fnvHash(s string, bucket int64) uint64 {
	const (
		offset64 = 1469598103934665603
		prime64  = 1099511628211
	)
	h := uint64(offset64)
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= prime64
	}
	for b := bucket; b > 0; b >>= 8 {
		h ^= uint64(b & 0xff)
		h *= prime64
	}
	return h
}
