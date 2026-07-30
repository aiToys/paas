package provider_test

import (
	"errors"
	"testing"

	"github.com/aitoys/paas/pkg/provider"
)

// stubResolver 验证 CredentialResolver 接口可被外部实现（依赖倒置可行）。
type stubResolver struct {
	v   string
	err error
}

func (s stubResolver) Resolve(string) (string, error) { return s.v, s.err }

func TestCredentialResolverInterface(t *testing.T) {
	var r provider.CredentialResolver = stubResolver{v: "sk-test"}
	v, err := r.Resolve("ref")
	if err != nil || v != "sk-test" {
		t.Fatalf("Resolve 应返回明文，got %q err=%v", v, err)
	}
}

// TestSentinelsAreDistinct 验证五类错误 sentinel 互不混淆（驱动降级决策的基石）。
func TestSentinelsAreDistinct(t *testing.T) {
	errs := []error{
		provider.ErrCredentialMissing,
		provider.ErrCredentialInvalid,
		provider.ErrUpstreamRateLimit,
		provider.ErrUpstreamUnavailable,
		provider.ErrUpstreamConfig,
	}
	for i, a := range errs {
		for j, b := range errs {
			if i != j && errors.Is(a, b) {
				t.Fatalf("sentinel %d 与 %d 不应相等（降级分类会错乱）", i, j)
			}
		}
	}
}

// TestChannelThirdPartyFields 验证 Channel 第三方通道字段可读写（零值向后兼容）。
func TestChannelThirdPartyFields(t *testing.T) {
	c := provider.Channel{ //nolint:gosec // G101 误报：CredentialRef 字段名触发，此处是 ID 占位非凭据
		ID:            "gpt-4o#openai",
		Type:          "openai-compatible",
		UpstreamModel: "gpt-4o",
		CredentialRef: "sec-platform-openai",
		Endpoint:      "https://api.openai.com/v1",
	}
	if c.UpstreamModel != "gpt-4o" || c.CredentialRef != "sec-platform-openai" {
		t.Fatalf("第三方通道字段未正确填充: %+v", c)
	}
	// mock/echo 通道这两个字段为零值（向后兼容）
	mock := provider.Channel{ID: "x#mock", Type: "mock"}
	if mock.UpstreamModel != "" || mock.CredentialRef != "" {
		t.Fatalf("mock 通道第三方字段应为零值")
	}
}
