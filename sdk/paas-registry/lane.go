package paasregistry

import (
	"context"
	"net/http"
)

// LaneHeader 流量染色 HTTP header 名。服务间调用透传此 header 即得联调泳道上下文。
const LaneHeader = "x-paas-lane"

// laneKey 是 ctx 携带 lane 的 key（私有类型避免与其他包冲突）。
type laneKey struct{}

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
func LaneMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if lane := r.Header.Get(LaneHeader); lane != "" {
			r = r.WithContext(WithLane(r.Context(), lane))
		}
		next.ServeHTTP(w, r)
	})
}

// ApplyLaneHeader 从 ctx 取 lane 注入 outgoing 请求的 x-paas-lane header。
// 应用调下游服务前调用（与 paas-registry GetService 配套，完成跨服务染色透传）。
// lane 空 / ctx 无 lane 时不注入（保持 default 行为，向后兼容）。
func ApplyLaneHeader(ctx context.Context, req *http.Request) {
	lane := LaneFromCtx(ctx)
	if lane == "" {
		return
	}
	req.Header.Set(LaneHeader, lane)
}
