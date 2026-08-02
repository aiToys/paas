package maas

import (
	"context"
	"errors"
	"testing"

	"github.com/aitoys/paas/pkg/provider"
)

var ctx = context.Background()

func TestModelCRUD(t *testing.T) {
	s := NewMemoryStore()
	m := &provider.Model{ID: "m1", Name: "模型1", Vendor: "OpenAI", ContextWindow: 8192, Capabilities: []string{"chat"}}

	if err := s.CreateModel(ctx, m); err != nil {
		t.Fatalf("CreateModel: %v", err)
	}
	if err := s.CreateModel(ctx, m); !errors.Is(err, ErrModelExists) {
		t.Fatalf("重复创建 want ErrModelExists, got %v", err)
	}
	if err := s.CreateModel(ctx, &provider.Model{}); !errors.Is(err, ErrModelExists) {
		t.Fatalf("空模型 want ErrModelExists, got %v", err)
	}

	got, err := s.GetModel(ctx, "m1")
	if err != nil || got.Name != "模型1" || got.ContextWindow != 8192 {
		t.Fatalf("GetModel got %+v err=%v", got, err)
	}
	if _, err := s.GetModel(ctx, "nope"); !errors.Is(err, ErrModelNotFound) {
		t.Fatalf("GetModel 不存在 want ErrModelNotFound, got %v", err)
	}
	// 深拷贝隔离
	got.Name = "改了"
	if g2, _ := s.GetModel(ctx, "m1"); g2.Name != "模型1" {
		t.Fatal("GetModel 未深拷贝，外部修改泄漏进存储")
	}

	list, err := s.ListModels(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("ListModels len want 1, got %d err=%v", len(list), err)
	}

	// UpdateModel 仅改标量，channels 不受影响
	if err := s.CreateChannel(ctx, "m1", &provider.Channel{ID: "c1", Type: ProviderEcho, Status: provider.StatusHealthy}); err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
	if err := s.UpdateModel(ctx, &provider.Model{ID: "m1", Name: "改名", Vendor: "新供应商"}); err != nil {
		t.Fatalf("UpdateModel: %v", err)
	}
	upd, _ := s.GetModel(ctx, "m1")
	if upd.Name != "改名" || upd.Vendor != "新供应商" || len(upd.Channels) != 1 {
		t.Fatalf("UpdateModel 后 Name=%s Vendor=%s Channels=%v", upd.Name, upd.Vendor, upd.Channels)
	}
	if err := s.UpdateModel(ctx, &provider.Model{ID: "none"}); !errors.Is(err, ErrModelNotFound) {
		t.Fatalf("UpdateModel 不存在 want ErrModelNotFound, got %v", err)
	}

	// Delete 级联清 channels
	if err := s.DeleteModel(ctx, "m1"); err != nil {
		t.Fatalf("DeleteModel: %v", err)
	}
	if _, err := s.GetModel(ctx, "m1"); !errors.Is(err, ErrModelNotFound) {
		t.Fatal("DeleteModel 后模型仍存在")
	}
	if err := s.DeleteModel(ctx, "m1"); !errors.Is(err, ErrModelNotFound) {
		t.Fatalf("DeleteModel 不存在 want ErrModelNotFound, got %v", err)
	}
}

func TestChannelCRUD(t *testing.T) {
	s := NewMemoryStore()
	if err := s.CreateModel(ctx, &provider.Model{ID: "m1", Name: "m1", Vendor: "v"}); err != nil {
		t.Fatal(err)
	}

	c := &provider.Channel{ID: "c1", Type: ProviderEcho, Priority: 1, Status: provider.StatusHealthy}
	if err := s.CreateChannel(ctx, "m1", c); err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
	if err := s.CreateChannel(ctx, "none", c); !errors.Is(err, ErrModelNotFound) {
		t.Fatalf("CreateChannel 模型不存在 want ErrModelNotFound, got %v", err)
	}
	if err := s.CreateChannel(ctx, "m1", c); !errors.Is(err, ErrChannelExists) {
		t.Fatalf("重复通道 want ErrChannelExists, got %v", err)
	}

	chs, err := s.ListChannels(ctx, "m1")
	if err != nil || len(chs) != 1 || chs[0].ID != "c1" {
		t.Fatalf("ListChannels got %+v err=%v", chs, err)
	}
	// ListChannels clone 隔离
	chs[0].Priority = 99
	if cs, _ := s.ListChannels(ctx, "m1"); cs[0].Priority != 1 {
		t.Fatal("ListChannels 未深拷贝")
	}

	if err := s.UpdateChannel(ctx, "m1", &provider.Channel{ID: "c1", Priority: 5, Status: provider.StatusOffline}); err != nil {
		t.Fatalf("UpdateChannel: %v", err)
	}
	cs, _ := s.ListChannels(ctx, "m1")
	if cs[0].Priority != 5 || cs[0].Status != provider.StatusOffline {
		t.Fatalf("UpdateChannel 后 %+v", cs[0])
	}
	if err := s.UpdateChannel(ctx, "m1", &provider.Channel{ID: "nope"}); !errors.Is(err, ErrChannelNotFound) {
		t.Fatalf("UpdateChannel 不存在 want ErrChannelNotFound, got %v", err)
	}

	if err := s.DeleteChannel(ctx, "m1", "c1"); err != nil {
		t.Fatalf("DeleteChannel: %v", err)
	}
	if cs, _ := s.ListChannels(ctx, "m1"); len(cs) != 0 {
		t.Fatal("DeleteChannel 后仍有通道")
	}
	if err := s.DeleteChannel(ctx, "m1", "c1"); !errors.Is(err, ErrChannelNotFound) {
		t.Fatalf("DeleteChannel 不存在 want ErrChannelNotFound, got %v", err)
	}
}

func TestBuildProvider(t *testing.T) {
	cases := []struct {
		name string
		ch   *provider.Channel
		want string // 期望 Provider.Name()，空串表示期望 nil
	}{
		{"echo", &provider.Channel{Type: ProviderEcho}, ProviderEcho},
		{"mock", &provider.Channel{Type: ProviderMock}, ProviderMock},
		{"openai-compatible", &provider.Channel{
			Type: ProviderOpenAICompatible, Vendor: "openai",
			Endpoint: "https://api.openai.com/v1", UpstreamModel: "gpt-4o", CredentialRef: "sec-x",
		}, ProviderOpenAICompatible},
		{"unknown", &provider.Channel{Type: "unknown"}, ""},
		{"nil-channel", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := BuildProvider(tc.ch, nil) // nil resolver：真实通道仍可构造，Chat 时返 ErrCredentialMissing
			if tc.want == "" {
				if p != nil {
					t.Fatalf("want nil, got %T", p)
				}
				return
			}
			if p == nil {
				t.Fatalf("want Provider %s, got nil", tc.want)
			}
			if p.Name() != tc.want {
				t.Fatalf("Name() want %q, got %q", tc.want, p.Name())
			}
		})
	}
}
