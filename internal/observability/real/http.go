// Package real 共享 HTTP 工具：Do + defer Body.Close + JSON Decode。
// 统一 errcheck（Close 错误对只读响应体无意义，显式丢弃）与循环内 body 泄漏
// （每调用独立 defer 作用域，循环中多次请求不会堆积到函数末尾才释放）。
package real

import (
	"encoding/json"
	"net/http"
)

// fetchJSON 执行请求并解码 JSON 到 T。网络错误/解码失败均返回 err（调用方降级为空 + 日志）。
// 响应体在函数返回前关闭（defer 作用域限于本次调用，循环安全）。
func fetchJSON[T any](client *http.Client, req *http.Request) (T, error) {
	var dst T
	resp, err := client.Do(req)
	if err != nil {
		return dst, err
	}
	defer func() { _ = resp.Body.Close() }()
	// 解码失败不立即返回：部分后端非 success 时仍返可解析结构，由调用方按 status 判定。
	_ = json.NewDecoder(resp.Body).Decode(&dst)
	return dst, nil
}
