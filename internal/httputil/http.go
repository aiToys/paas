// Package httputil 提供各 HTTP handler 共享的 JSON 响应工具，消除 15+ 个 handler
// 包重复定义 writeErr/writeData 的冗余（DRY）。统一响应契约：
//
//   - 成功（{data:T}）：WriteData(w, v)
//   - 错误（{error:msg}）：WriteError(w, code, msg)
//
// 所有函数显式设置 Content-Type（幂等，handler 开头的设置不受影响），
// 保证非 2xx 分支也返回正确 JSON 头（原各包 writeErr 依赖 handler 前置设置，
// 漏设时返回 text/plain，浏览器 devtools 难辨）。
package httputil

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
)

// WriteJSON 以指定状态码写裸 JSON 响应（不包裹 data/error）。
func WriteJSON(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// WriteData 写成功响应，统一 {data:T} 契约（与下游 fetchJSON<T> / 前端 json.data 对齐）。
func WriteData(w http.ResponseWriter, v interface{}) {
	WriteJSON(w, http.StatusOK, map[string]interface{}{"data": v})
}

// WriteDataCreated 同 WriteData，状态码 201（资源创建）。
func WriteDataCreated(w http.ResponseWriter, v interface{}) {
	WriteJSON(w, http.StatusCreated, map[string]interface{}{"data": v})
}

// WriteError 写错误响应，统一 {error:msg} 契约。
func WriteError(w http.ResponseWriter, code int, msg string) {
	WriteJSON(w, code, map[string]string{"error": msg})
}

// WriteInternalError 写 500 响应（统一 "internal error"，不泄漏 store 内部错误如 SQL 语句/表名/连接串），
// 原始错误入服务端日志供运维排查。替换散落各 handler 的 WriteError(w, 500, err.Error()) 模式。
func WriteInternalError(w http.ResponseWriter, err error) {
	log.Printf("[internal-error] %v", err)
	WriteError(w, http.StatusInternalServerError, "internal error")
}

// internalErrorMarkers 是底层技术错误的特征子串（PG/网络/内部 FQDN/约束名）。
// 项目业务错误统一中文（Validate fieldErr / sentinel / fmt.Errorf 中文包装），
// 命中这些英文特征几乎必为未包装的底层错误，应脱敏而非回显客户端。
var internalErrorMarkers = []string{
	"SQLSTATE", "ERROR: ", "pgx", "dial tcp", "connection refused", "connection reset",
	"no such host", ".svc.cluster.local", "duplicate key value", "violates foreign key",
	"violates unique constraint", "value too long for type", "_pkey", "_fkey",
	"connection reset by peer", "i/o timeout", "TLS handshake",
}

// isInternalErrorText 判定错误消息是否含底层技术特征（需脱敏）。
func isInternalErrorText(msg string) bool {
	for _, m := range internalErrorMarkers {
		if strings.Contains(msg, m) {
			return true
		}
	}
	return false
}

// WriteServiceError 统一 handler 的 repo/store 错误响应，消除「err.Error() 直接回客户端」泄漏。
//
// 分流逻辑：
//   - err 含底层技术特征（PG SQLSTATE/连接错误/内部 FQDN/约束名等）→ 500 脱敏（WriteInternalError），
//     防泄漏 SQL 语句/表名/连接串/集群内部地址给前端
//   - 否则视为业务错误（Validate/sentinel/中文 fmt.Errorf，消息安全）→ 按传入 status 返回 err.Error()
//
// 用法：handler 中 repo 调用失败处，由 WriteError(w, code, err.Error()) 改为 WriteServiceError(w, code, err)。
// 业务 sentinel 已提前 errors.Is 分流为固定中文 4xx 的调用点不受影响。
func WriteServiceError(w http.ResponseWriter, status int, err error) {
	if err == nil {
		return
	}
	msg := err.Error()
	if isInternalErrorText(msg) {
		WriteInternalError(w, err)
		return
	}
	WriteError(w, status, msg)
}
