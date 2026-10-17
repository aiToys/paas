// Package workload —— 容器终端 WebSocket 桥（spec 2026-10-17 §6 终端）。
//
// GET /api/terminal-sessions/{id}/stream：同源 Origin + 登录态 + workload:exec 复核后
// 升级 WS，消费一次性握手会话并桥接 exec 流。URL 不携带 token（cookie 鉴权）。
// DELETE /api/terminal-sessions/{id}：关闭会话（仅所有者——会话绑定创建者用户）。
//
// 消息协议（JSON 文本帧）：
//
//	客户端→服务端：{"type":"input","data":"…"} | {"type":"resize","cols":N,"rows":N}
//	服务端→客户端：{"type":"output","data":"…"} | {"type":"exit"} | {"type":"error","message":"…"}
package workload

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"github.com/aitoys/paas/internal/httputil"
)

// 消息大小护栏：单帧输入上限（命令行粘贴一般 <32KB）；输出按 4KB 块拆帧。
const (
	terminalMaxInputBytes = 32 * 1024
	terminalReadChunk     = 4 * 1024
	terminalPongWait      = 60 * time.Second
)

// TerminalWSHandler 处理终端会话的 WS 流与关闭端点。
type TerminalWSHandler struct {
	Terminals *TerminalManager
	// Authorize 升级前复核 workload:exec（角色撤销即拒绝/断开；不能只信受理时的权限）。
	Authorize func(r *http.Request, perm string) bool
	// UserFn 取当前用户（会话所有者校验）。
	UserFn func(r *http.Request) string
	// Audit 终端连接结束审计（connect 已在 create 记录；此处记 close 带原因）。
	Audit func(r *http.Request, action, resourceID, detail string)
}

var terminalUpgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	// Origin 显式同源校验（CheckOrigin 返 false 由我们自己的 sameOrigin 做）。
	CheckOrigin: func(r *http.Request) bool { return true },
}

// sameOrigin 校验 Origin 头与请求 Host 同源（防跨站 WS 劫持；无 Origin 头的非浏览器客户端放行）。
func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u := strings.TrimPrefix(strings.TrimPrefix(origin, "https://"), "http://")
	u = strings.SplitN(u, "/", 2)[0]
	return strings.EqualFold(u, r.Host)
}

// ServeHTTP 处理 /api/terminal-sessions/{id}/stream（WS）与 /{id}（DELETE）。
func (h *TerminalWSHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/terminal-sessions"), "/")
	parts := strings.Split(rest, "/")
	if len(parts) != 2 || parts[1] != "stream" {
		if r.Method == http.MethodDelete && len(parts) == 1 && parts[0] != "" {
			h.closeSession(w, r, parts[0])
			return
		}
		httputil.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	if r.Method != http.MethodGet {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	h.stream(w, r, parts[0])
}

// stream WS 升级 + 会话桥接。
func (h *TerminalWSHandler) stream(w http.ResponseWriter, r *http.Request, id string) {
	// 升级前复核：登录态由挂载点 auth 中间件保证；此处查权限 + 同源。
	if h.Authorize != nil && !h.Authorize(r, PermWorkloadExec) {
		httputil.WriteError(w, http.StatusForbidden, "forbidden: missing "+PermWorkloadExec)
		return
	}
	if !sameOrigin(r) {
		httputil.WriteError(w, http.StatusForbidden, "跨源 WebSocket 被拒绝")
		return
	}
	sess, err := h.Terminals.Consume(r.Context(), id) // 单次消费（过期/已用 → 410 语义）
	if err != nil {
		httputil.WriteError(w, http.StatusGone, "会话已过期或已被使用，请重新发起")
		return
	}
	// 会话所有者校验：他人拿到不透明 ID 也不能连（防枚举）。
	if h.UserFn != nil && sess.UserID != "" && h.UserFn(r) != sess.UserID {
		h.Terminals.Close(id)
		httputil.WriteError(w, http.StatusForbidden, "会话不属于当前用户")
		return
	}
	conn, err := terminalUpgrader.Upgrade(w, r, nil)
	if err != nil {
		h.Terminals.Close(id) // 升级失败释放会话与 exec 流
		return                // Upgrade 已写错误响应
	}
	defer func() {
		_ = conn.Close()
		h.Terminals.Close(id)
		h.auditClose(r, sess, "连接关闭")
	}()
	h.pump(r, conn, sess)
}

// pump 双向桥：stdin 从 WS 读，stdout 向 WS 写；心跳/空闲/上限超时关闭。
func (h *TerminalWSHandler) pump(r *http.Request, conn *websocket.Conn, sess *TerminalSession) {
	writeMsg := func(v any) bool {
		_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		if err := conn.WriteJSON(v); err != nil {
			return false
		}
		return true
	}
	// 输出泵：exec stdout → WS output 帧。
	outDone := make(chan struct{})
	go func() {
		defer close(outDone)
		buf := make([]byte, terminalReadChunk)
		for {
			n, err := sess.Exec.Stdout().Read(buf)
			if n > 0 {
				if !writeMsg(map[string]any{"type": "output", "data": string(buf[:n])}) {
					return
				}
				h.Terminals.Touch(sess)
			}
			if err != nil {
				return // 流关闭（shell 退出/会话终止）
			}
		}
	}()
	// 输入泵：WS 帧 → stdin/resize。
	for {
		_ = conn.SetReadDeadline(time.Now().Add(terminalPongWait))
		conn.SetPongHandler(func(string) error { _ = conn.SetReadDeadline(time.Now().Add(terminalPongWait)); return nil })
		mt, data, err := conn.ReadMessage()
		if err != nil {
			sess.Exec.Close() // 对端断开：终止 exec
			<-outDone
			return
		}
		if mt != websocket.TextMessage {
			continue
		}
		_ = conn.SetReadDeadline(time.Now().Add(terminalPongWait))
		var msg struct {
			Type string `json:"type"`
			Data string `json:"data"`
			Cols uint16 `json:"cols"`
			Rows uint16 `json:"rows"`
		}
		if err := json.Unmarshal(data, &msg); err != nil || len(data) > terminalMaxInputBytes {
			if !writeMsg(map[string]any{"type": "error", "message": "非法消息"}) {
				return
			}
			continue
		}
		h.Terminals.Touch(sess)
		switch msg.Type {
		case "input":
			if _, err := sess.Exec.Stdin().Write([]byte(msg.Data)); err != nil {
				writeMsg(map[string]any{"type": "error", "message": "输入通道已关闭"})
				return
			}
		case "resize":
			sess.Exec.Resize(msg.Cols, msg.Rows)
		case "ping":
			if !writeMsg(map[string]any{"type": "pong"}) {
				return
			}
		default: // 未知类型忽略（向前兼容）
		}
		// 空闲/上限超时检查：超时即关（会话最长 30min，空闲 5min）。
		if h.Terminals.IdleExpired(sess, time.Now()) {
			writeMsg(map[string]any{"type": "error", "message": "会话超时关闭（空闲或超过最长时长）"})
			return
		}
	}
}

// closeSession DELETE /api/terminal-sessions/{id}：所有者或权限持有者关闭。
func (h *TerminalWSHandler) closeSession(w http.ResponseWriter, r *http.Request, id string) {
	if h.Authorize != nil && !h.Authorize(r, PermWorkloadExec) {
		httputil.WriteError(w, http.StatusForbidden, "forbidden: missing "+PermWorkloadExec)
		return
	}
	sess, ok := h.Terminals.Get(id)
	if !ok {
		httputil.WriteError(w, http.StatusNotFound, "会话不存在或已结束")
		return
	}
	// 仅创建者可关闭自己的会话（admin 通配由 Authorize 覆盖）。
	if h.UserFn != nil && sess.UserID != "" && h.UserFn(r) != sess.UserID {
		httputil.WriteError(w, http.StatusForbidden, "只能关闭自己发起的终端会话")
		return
	}
	h.Terminals.Close(id)
	if h.Audit != nil {
		h.Audit(r, "workload_terminal_close", sess.WorkloadID, "pod="+sess.PodName+" container="+sess.Container+" reason=user-close")
	}
	httputil.WriteData(w, map[string]string{"closed": id})
}

// auditClose 连接结束审计（失败静默——不影响主流程）。
func (h *TerminalWSHandler) auditClose(r *http.Request, sess *TerminalSession, why string) {
	if h.Audit == nil {
		return
	}
	h.Audit(r, "workload_terminal_close", sess.WorkloadID, "pod="+sess.PodName+" container="+sess.Container+" reason="+why)
}
