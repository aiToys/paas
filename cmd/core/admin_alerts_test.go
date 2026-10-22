package main

import (
	"context"
	"errors"
	"testing"

	"github.com/aitoys/paas/internal/observability"
	"github.com/aitoys/paas/pkg/tenant"
)

type adminAlertsStub struct {
	calls []string
	fail  string
}

func (s *adminAlertsStub) ListAllAlertRules(context.Context) ([]observability.AlertRule, error) {
	return []observability.AlertRule{{TenantID: "t-acme", Enabled: true}, {TenantID: "t-globex", Enabled: true}}, nil
}
func (s *adminAlertsStub) ListAlerts(ctx context.Context, _, _ string) ([]observability.Alert, error) {
	tid, _ := tenant.TenantFrom(ctx)
	s.calls = append(s.calls, tid)
	if tid == s.fail {
		return nil, errors.New("metrics unavailable")
	}
	return []observability.Alert{{RuleID: "rule", Status: observability.AlertFiring}}, nil
}
func TestAdminAlertsFailureIsNotEmpty(t *testing.T) {
	s := &adminAlertsStub{fail: "t-globex"}
	alerts, err := collectAdminAlerts(context.Background(), s, "")
	if err == nil || alerts != nil {
		t.Fatalf("failure hidden: %+v %v", alerts, err)
	}
}
func TestAdminAlertsFilterBeforeEvaluation(t *testing.T) {
	s := &adminAlertsStub{fail: "t-globex"}
	alerts, err := collectAdminAlerts(context.Background(), s, "t-acme")
	if err != nil || len(alerts) != 1 || alerts[0].TenantID != "t-acme" {
		t.Fatalf("bad alerts: %+v %v", alerts, err)
	}
	if len(s.calls) != 1 || s.calls[0] != "t-acme" {
		t.Fatalf("evaluated outside scope: %+v", s.calls)
	}
}
