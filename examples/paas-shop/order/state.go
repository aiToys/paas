package main

import "fmt"

// 订单状态（spec §3：pending →(pay) paid →(ship) shipped →(confirm) completed；
// pending --超时关单--> closed（sweeper）；paid|shipped →(退款审批流) refunded）。
const (
	StatusPending   = "pending"
	StatusPaid      = "paid"
	StatusShipped   = "shipped"
	StatusCompleted = "completed"
	StatusRefunded  = "refunded"
	StatusClosed    = "closed"
)

// transitions 动作 → 当前状态 → 目标状态（单一真源，非法组合查表不中即拒）。
var transitions = map[string]map[string]string{
	"pay":     {StatusPending: StatusPaid},
	"ship":    {StatusPaid: StatusShipped},
	"confirm": {StatusShipped: StatusCompleted},
	"refund":  {StatusPaid: StatusRefunded, StatusShipped: StatusRefunded},
	"close":   {StatusPending: StatusClosed},
}

// Transition 校验并返回目标状态；非法迁移返错（handler 映射 409）。
func Transition(from, action string) (string, error) {
	if m, ok := transitions[action]; ok {
		if to, ok := m[from]; ok {
			return to, nil
		}
	}
	return "", fmt.Errorf("订单状态 %s 不允许执行 %s", from, action)
}
