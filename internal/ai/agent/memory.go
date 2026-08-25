package agent

import (
	"sort"
	"sync"

	"github.com/aitoys/paas/pkg/provider"
)

// 对话记忆（多轮）：conversationId → 环形历史（per agent 进程内）。
// MVP 取舍：内存实现（重启即失，会话是易变数据，与 Coze 内存变量同语义）；
// PG 持久化 + TTL 清理留后续。maxHistoryPerConv 截断单会话防 token 膨胀；
// maxConversations 全局上限 + 惰性清扫防会话数无界增长（每请求新 convId 的滥用场景）。
const (
	maxHistoryPerConv = 20
	maxConversations  = 1000
	sweepEvery        = 100 // 每 N 次 append 做一次全量清扫
)

type convKey struct{ agentID, convID string }

type conversationStore struct {
	mu     sync.Mutex
	hist   map[convKey][]provider.Message
	appends int
}

var conversations = &conversationStore{hist: map[convKey][]provider.Message{}}

// loadHistory 取会话历史（返回副本，防调用方改入参污染）。
func (c *conversationStore) loadHistory(agentID, convID string) []provider.Message {
	c.mu.Lock()
	defer c.mu.Unlock()
	h := c.hist[convKey{agentID, convID}]
	out := make([]provider.Message, len(h))
	copy(out, h)
	return out
}

// appendHistory 追加本轮 user/assistant 消息并截断。
func (c *conversationStore) appendHistory(agentID, convID string, user, assistant provider.Message) {
	c.mu.Lock()
	defer c.mu.Unlock()
	k := convKey{agentID, convID}
	h := append(c.hist[k], user, assistant)
	if len(h) > maxHistoryPerConv {
		h = h[len(h)-maxHistoryPerConv:]
	}
	c.hist[k] = h
	// 惰性清扫：超全局上限时按「最后活动」近似（map 遍历序随机）丢弃一半最旧会话
	c.appends++
	if c.appends >= sweepEvery && len(c.hist) > maxConversations {
		c.appends = 0
		// 无时间戳——按消息数排序近似 LRU（少的先丢；精确 LRU 需时间戳，收益不抵复杂度）
		type entry struct {
			k  convKey
			nz int
		}
		entries := make([]entry, 0, len(c.hist))
		for key, msgs := range c.hist {
			entries = append(entries, entry{key, len(msgs)})
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].nz < entries[j].nz })
		for _, e := range entries[:len(entries)-maxConversations/2] {
			delete(c.hist, e.k)
		}
	}
}
