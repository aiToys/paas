package assistant

import (
	"context"
	"time"
)

// Settings 区分会话保留与原始证据保留，不因日志片段过期而删掉整个对话。
type Settings struct {
	Persistent   bool `json:"persistent"`
	HistoryDays  int  `json:"historyDays"`
	EvidenceDays int  `json:"evidenceDays"`
}

type sweeper interface {
	Sweep(context.Context, time.Time, time.Time) error
}

// SweepHistory 只清理过期且无活动任务的会话；片段过期仍保留证据 ID、状态与哈希。
func (s *Service) SweepHistory(ctx context.Context) error {
	if repo, ok := s.repo.(sweeper); ok {
		return repo.Sweep(ctx, time.Now().AddDate(0, 0, -s.Settings.HistoryDays), time.Now().AddDate(0, 0, -s.Settings.EvidenceDays))
	}
	return nil
}

func (m *memRepo) Sweep(_ context.Context, historyBefore, evidenceBefore time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.runs {
		if r.FinishedAt != nil && r.FinishedAt.Before(evidenceBefore) {
			for i := range r.Evidence {
				if r.Evidence[i].Snippet != "" {
					r.Evidence[i].Snippet = ""
					r.Evidence[i].ReasonCode = "expired"
				}
			}
		}
	}
	for id, s := range m.sessions {
		if !s.UpdatedAt.Before(historyBefore) {
			continue
		}
		active := false
		for _, r := range m.runs {
			if r.SessionID == id && r.Active() {
				active = true
			}
		}
		if active {
			continue
		}
		for rid, r := range m.runs {
			if r.SessionID == id {
				delete(m.runs, rid)
				delete(m.events, rid)
			}
		}
		delete(m.sessions, id)
	}
	return nil
}
