package main

import (
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/aitoys/paas-examples/paas-shop/internal/dpdisc"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

type serviceRoute struct {
	URL, Requested, Resolved, Reason, Instance string
}

// 平台已负责未覆盖时的基线回退；空实例和查询失败必须作为故障返回。
func resolveService(w http.ResponseWriter, r *http.Request, service string) (serviceRoute, bool) {
	lane := strings.TrimSpace(r.Header.Get("X-Paas-Lane"))
	if lane == "" {
		lane = "default"
	}
	route := serviceRoute{Requested: lane, Resolved: "unknown", Reason: "static"}
	if lane != "default" && (strings.HasPrefix(svcSuffix, "-prod") || firstEnv("PAAS_ENV_TYPE") == "prod") {
		http.Error(w, "生产环境不支持联调泳道", http.StatusBadRequest)
		return route, false
	}
	if !dp.Enabled() {
		// 静态模式只能访问基线，不能把未接入的泳道伪装成成功。
		if lane != "default" {
			http.Error(w, "未配置数据面发现，无法验证泳道路由", http.StatusServiceUnavailable)
			return route, false
		}
		route.URL = fallback[service]
		return route, true
	}
	instance, err := dp.Resolve(r.Context(), "paas-shop-"+service+svcSuffix, lane)
	if err != nil {
		slog.Warn("服务发现未选出可用实例", "service", service, "lane", lane, "err", err)
		msg := "服务发现失败，已停止转发；请检查平台连接或权限"
		if errors.Is(err, dpdisc.ErrNoReadyInstances) {
			msg = "平台未返回可用实例，已停止转发；请检查实例健康与发现状态"
		}
		http.Error(w, msg, http.StatusServiceUnavailable)
		return route, false
	}
	route.URL, route.Instance = instance.URL(), instance.Name
	route.Resolved = instance.Lane
	if route.Resolved == "" {
		route.Resolved = "unknown"
	}
	route.Reason = "matched"
	if lane != "default" && route.Resolved == "default" {
		route.Reason = "baseline-fallback"
	}
	if route.Resolved == "unknown" {
		route.Reason = "unknown"
	}
	span := trace.SpanFromContext(r.Context())
	span.SetAttributes(attribute.String("paas.requested_lane", lane), attribute.String("paas.resolved_lane", route.Resolved), attribute.String("paas.route_reason", route.Reason), attribute.String("paas.target_instance", route.Instance))
	return route, true
}

func writeRouteHeaders(w http.ResponseWriter, r *http.Request, route serviceRoute) {
	w.Header().Set("X-Paas-Requested-Lane", route.Requested)
	w.Header().Set("X-Paas-Resolved-Lane", route.Resolved)
	w.Header().Set("X-Paas-Route-Reason", route.Reason)
	w.Header().Set("X-Paas-Lane", route.Requested) // 保留旧契约：仅表示请求染色。
	instance := w.Header().Get("X-Paas-Service")
	if instance == "" {
		instance = route.Instance
	}
	w.Header().Set("X-Paas-Target-Instance", instance)
	if id := trace.SpanContextFromContext(r.Context()).TraceID(); id.IsValid() {
		w.Header().Set("X-Paas-Trace-Id", id.String())
	}
}

func firstEnv(keys ...string) string {
	for _, key := range keys {
		if v := os.Getenv(key); v != "" {
			return v
		}
	}
	return ""
}
