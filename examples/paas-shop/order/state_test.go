package main

import "testing"

// TestTransitionLegal 合法迁移全覆盖（spec §3 状态机）。
func TestTransitionLegal(t *testing.T) {
	cases := []struct{ from, action, want string }{
		{StatusPending, "pay", StatusPaid},
		{StatusPaid, "ship", StatusShipped},
		{StatusShipped, "confirm", StatusCompleted},
		{StatusPaid, "refund", StatusRefunded},
		{StatusShipped, "refund", StatusRefunded},
		{StatusPending, "close", StatusClosed},
	}
	for _, c := range cases {
		got, err := Transition(c.from, c.action)
		if err != nil || got != c.want {
			t.Errorf("Transition(%s,%s) = (%s,%v), want (%s,nil)", c.from, c.action, got, err, c.want)
		}
	}
}

// TestTransitionIllegal 非法迁移全量枚举（含终态不可再动 + 乱序动作）。
func TestTransitionIllegal(t *testing.T) {
	statuses := []string{StatusPending, StatusPaid, StatusShipped, StatusCompleted, StatusRefunded, StatusClosed, "bogus"}
	actions := []string{"pay", "ship", "confirm", "refund", "close", "bogus"}
	legal := map[string]bool{
		"pending|pay": true, "paid|ship": true, "shipped|confirm": true,
		"paid|refund": true, "shipped|refund": true, "pending|close": true,
	}
	for _, s := range statuses {
		for _, a := range actions {
			if legal[s+"|"+a] {
				continue
			}
			if got, err := Transition(s, a); err == nil {
				t.Errorf("Transition(%s,%s) 应报错，实际返 %s", s, a, got)
			}
		}
	}
}
