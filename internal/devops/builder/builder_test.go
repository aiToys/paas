package builder

import (
	"context"
	"strings"
	"testing"
)

func TestMockBuildProducesDeterministicDigest(t *testing.T) {
	r, err := Mock{}.Build(context.Background(), Params{
		TenantID: "t1", AppID: "app-cs", BuildID: "build-1",
		Commit: "abcdef1234567890", Branch: "main",
	})
	if err != nil {
		t.Fatalf("mock build 不应错: %v", err)
	}
	if !strings.HasPrefix(r.Digest, "sha256:") {
		t.Fatalf("digest 应 sha256: 前缀，实得 %s", r.Digest)
	}
	// 相同输入应产出相同 digest（确定性，不可变真源）。
	r2, _ := Mock{}.Build(context.Background(), Params{
		TenantID: "t1", AppID: "app-cs", BuildID: "build-1",
		Commit: "abcdef1234567890", Branch: "main",
	})
	if r.Digest != r2.Digest {
		t.Fatalf("相同输入 digest 应一致")
	}
	// tag = branch + 前 8 位 commit。
	if r.Tag != "main-abcdef12" {
		t.Fatalf("tag 应为 main-abcdef12，实得 %s", r.Tag)
	}
}

func TestSafeShort(t *testing.T) {
	if got := safeShort("abcdef123456", 8); got != "abcdef12" {
		t.Fatalf("应取前 8 位，实得 %s", got)
	}
	if got := safeShort("abc", 8); got != "abc" {
		t.Fatalf("短串应原样返回，实得 %s", got)
	}
}

func TestImageRefAndRegistry(t *testing.T) {
	p := Params{AppID: "app-cs", Branch: "main", Commit: "abcdef12"}
	if got := ImageRef(p, "main-abcdef12"); got != "registry.paas.local/app-cs:main-abcdef12" {
		t.Fatalf("默认 registry 引用错误: %s", got)
	}
	p.Registry = "ghcr.io/aitoys"
	if got := ImageRef(p, "main-abcdef12"); got != "ghcr.io/aitoys/app-cs:main-abcdef12" {
		t.Fatalf("自定义 registry 引用错误: %s", got)
	}
}

func TestInjectToken(t *testing.T) {
	got := injectToken("https://github.com/o/r.git", "tok123")
	if got != "https://tok123@github.com/o/r.git" {
		t.Fatalf("token 注入错误: %s", got)
	}
	// 非 HTTPS 原样返回（如 SSH URL）。
	if got := injectToken("git@github.com:o/r.git", "tok"); got != "git@github.com:o/r.git" {
		t.Fatalf("SSH URL 不应注入 token: %s", got)
	}
}

func TestCurrentLine(t *testing.T) {
	// git rev-parse 输出可能带警告行，取最后非空行。
	if got := currentLine("warning: something\nabc123\n"); got != "abc123" {
		t.Fatalf("应取最后非空行，实得 %s", got)
	}
}

func TestRealMarker(t *testing.T) {
	var r Real
	if !r.Real() {
		t.Fatal("Real 应标记为真实执行")
	}
}

// TestRealBuildFailsGracefullyOnMissingGitURL 验证 Real.Build 在无效 GitURL 时返错
// （不 panic），Store 据此记 failed。不依赖真实仓库/registry。
func TestRealBuildFailsGracefullyOnMissingGitURL(t *testing.T) {
	var r Real
	_, err := r.Build(context.Background(), Params{
		AppID: "app-cs", Branch: "main", BuildID: "b1",
		GitURL: "https://invalid.invalid/nope.git",
	})
	if err == nil {
		t.Fatal("无效仓库应返错（git clone 失败），不应成功")
	}
}
