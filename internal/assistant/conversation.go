package assistant

import (
	"context"
	"encoding/json"
	"sort"
	"time"
)

type ownerKey struct{}

// WithOwner 为仓储与后台任务携带已认证的用户身份，不允许空身份扩展权限。
func WithOwner(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, ownerKey{}, userID)
}

// OwnerFrom 返回助手私有会话的用户边界。
func OwnerFrom(ctx context.Context) string { v, _ := ctx.Value(ownerKey{}).(string); return v }

// Message 是所有官方 Agent 共用的消息协议。消息 ID 稳定，刷新、重试不会重复追加。
// 文本和结构化结果由同一持久化 run 原子产生，兼容历史记录，避免双写两份消息真源。
type Message struct {
	ID         string    `json:"id"`
	SessionID  string    `json:"sessionId"`
	RunID      string    `json:"runId"`
	Role       string    `json:"role"`
	Text       string    `json:"text"`
	Status     string    `json:"status"`
	CreatedAt  time.Time `json:"createdAt"`
	ResultKind string    `json:"resultKind,omitempty"`
	Run        *RunView  `json:"run,omitempty"`
}

// Conversation 返回会话中的连续问答，分页按轮次截断，始终保留成对消息。
type Conversation struct {
	Session  Session   `json:"session"`
	Messages []Message `json:"messages"`
	HasMore  bool      `json:"hasMore"`
	Before   string    `json:"before,omitempty"`
}

// Messages 读取当前用户会话。UI 历史长度与模型上下文长度互不影响。
func (s *Service) Messages(ctx context.Context, id string, before time.Time, limit int) (Conversation, error) {
	sess, err := s.GetSession(ctx, id)
	if err != nil {
		return Conversation{}, err
	}
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	rows, err := s.repo.ConversationRuns(WithOwner(ctx, s.ownerFrom(ctx)), id, before, limit+1)
	if err != nil {
		return Conversation{}, err
	}
	out := Conversation{Session: sess, Messages: []Message{}, HasMore: len(rows) > limit}
	if len(rows) > limit {
		rows = rows[:limit]
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].CreatedAt.Before(rows[j].CreatedAt) })
	if len(rows) > 0 {
		out.Before = rows[0].CreatedAt.Format(time.RFC3339Nano)
	}
	for _, r := range rows {
		view := RunViewOf(r)
		out.Messages = append(out.Messages, Message{ID: r.ID + ":user", SessionID: id, RunID: r.ID, Role: "user", Text: r.Question, Status: "completed", CreatedAt: r.CreatedAt})
		text := r.Reply
		if text == "" && len(r.Report) > 0 {
			var rep UserReport
			if json.Unmarshal(r.Report, &rep) == nil {
				text = rep.Verdict.Summary
			}
		}
		if text == "" && IsTerminal(r.Status) {
			text = r.ErrorMsg
			if text == "" {
				text = "本轮已结束，未生成可用回答。可以补充问题后重试。"
			}
		}
		kind := ""
		if len(r.Report) > 0 {
			var artifact struct {
				Kind string `json:"kind"`
			}
			if json.Unmarshal(r.Report, &artifact) == nil {
				kind = artifact.Kind
			}
		}
		out.Messages = append(out.Messages, Message{ID: r.ID + ":assistant", SessionID: id, RunID: r.ID, Role: "assistant", Text: text, Status: r.Status, CreatedAt: r.CreatedAt, ResultKind: kind, Run: &view})
	}
	return out, nil
}

// ConversationRuns 的筛选边界由仓储强制执行。
func conversationLimit(n int) int {
	if n <= 0 || n > 100 {
		return 100
	}
	return n
}
