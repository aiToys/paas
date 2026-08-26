package paasregistry

import (
	"context"
	"net/http"
	"os"
	"sync/atomic"
)

// LaneHeader 流量染色 HTTP header 名。服务间调用透传此 header 即得联调泳道上下文。
//
// 染色语义（诚实边界）：SDK 的服务发现与出站注入按「ctx lane > 自身部署泳道（PAAS_LANE_ID env）
// > 无」解析。非 SDK 应用（裸 http client 直连 Service DNS）不经此链路，不自动染色——这类应用
// 跨泳道联调仍需入口侧显式指定（部署隔离仍生效，只是不降级发现）。
const LaneHeader = "x-paas-lane"

// laneKey 是 ctx 携带 lane 的 key（私有类型避免与其他包冲突）。
type laneKey struct{}

// LaneAttrKey span 属性名（LaneSpanHook 写入的 OTel attribute key）。
// 排障约定：trace 中 paas.lane=feature-x 即该请求走的泳道；default 基线不写（减少噪音）。
const LaneAttrKey = "paas.lane"

// LaneSpanHook 可选 span 桥接：应用侧注入（如 otel 版 hook 把 lane 写进当前 span 属性），
// SDK 不引 OTel 依赖（不想接 trace 的应用零成本使用 SDK）。
// 返回值是结束回调（请求 span 结束时调，可 nil）——hook 实现自行决定是否用。
type LaneSpanHook func(ctx context.Context, lane string) func()

// defaultLaneHook 默认无操作（未注入时不产生任何开销）。
var defaultLaneHook LaneSpanHook = func(context.Context, string) func() { return nil }

// SetLaneSpanHook 注入 span 桥接（应用启动时调一次）。nil 恢复默认。
func SetLaneSpanHook(h LaneSpanHook) {
	if h == nil {
		defaultLaneHook = func(context.Context, string) func() { return nil }
		return
	}
	defaultLaneHook = h
}

// WithLane 将 lane 注入 ctx。lane 空 / "default" 视为基线，仍存入 ctx（下游统一判空）。
// 应用 HTTP server 在 LaneMiddleware 中调用；业务代码也可显式注入（如测试触发器）。
func WithLane(ctx context.Context, lane string) context.Context {
	return context.WithValue(ctx, laneKey{}, lane)
}

// LaneFromCtx 从 ctx 取 lane；不存在或空返 ""。
func LaneFromCtx(ctx context.Context) string {
	v, _ := ctx.Value(laneKey{}).(string)
	return v
}

// LaneMiddleware 入口中间件：从 incoming 请求的 x-paas-lane header 取 lane 存入 r.Context()。
// 应用 http server 挂载此中间件后，下游 handler 链路即可经 LaneFromCtx 取到 lane。
// header 缺省时 ctx 不含 lane（视为 default 基线，向后兼容）。
// 注入 LaneSpanHook 时（见 SetLaneSpanHook）lane 同步写进当前请求 span 属性，
// trace 中可按 paas.lane 过滤——泳道排障（同一服务多泳道并行时区分流量归属）。
func LaneMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lane := r.Header.Get(LaneHeader)
		if lane == "" || lane == "default" {
			next.ServeHTTP(w, r) // 基线无 lane 属性，减少 span 噪音
			return
		}
		end := defaultLaneHook(r.Context(), lane)
		if end != nil {
			defer end()
		}
		next.ServeHTTP(w, r.WithContext(WithLane(r.Context(), lane)))
	})
}

// ApplyLaneHeader 注入 outgoing 请求的 x-paas-lane header（lane 解析经 resolveLane：
// ctx lane 优先，缺失回落自身部署泳道 SelfLane env）。
// 应用调下游服务前调用（与 paas-registry GetService 配套，完成跨服务染色透传）。
// lane 解析为空时不注入（保持 default 行为，向后兼容）。
func ApplyLaneHeader(ctx context.Context, req *http.Request) {
	lane := resolveLane(ctx)
	if lane == "" {
		return
	}
	req.Header.Set(LaneHeader, lane)
}

// selfLane 进程级缓存自身部署泳道（sync.OnceValue 语义，手动 atomic 兜空重读）。
var selfLane atomic.Value // string

// SelfLane 返回自身部署泳道（reconciler 注入的 PAAS_LANE_ID env）。
// 空缺 / "default" 返 ""（基线）。首次调用读取并缓存。
// 这是「应用零改动染色」核心：应用不挂 LaneMiddleware 时，出站调用回落到自身泳道。
func SelfLane() string {
	if v, ok := selfLane.Load().(string); ok && v != "" {
		return v
	}
	v := os.Getenv("PAAS_LANE_ID")
	if v == "" || v == "default" {
		v = "" // 基线统一空串
	}
	selfLane.Store(v)
	return v
}

// resolveLane lane 解析优先级：显式 ctx lane > SelfLane env > ""。
// ctx lane 是入口/上游指定的染色（如网关注入 header）；env 是自身部署泳道。
// 字面 "default" 归一为 ""（基线不上线：不注入 header、URL 不带 lane query，减噪）。
func resolveLane(ctx context.Context) string {
	if lane := LaneFromCtx(ctx); lane != "" && lane != "default" {
		return lane
	}
	return SelfLane()
}
