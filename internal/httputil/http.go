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
