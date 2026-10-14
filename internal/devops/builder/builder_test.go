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

// TestBuildTagNoArgsBackwardCompat 无 buildArgs 时保持原 branch-commit8（向后兼容单服务场景）。
func TestBuildTagNoArgsBackwardCompat(t *testing.T) {
	got := buildTag(Params{Branch: "main", Commit: "abcdef1234567890"})
	if got != "main-abcdef12" {
		t.Fatalf("无 buildArgs 应为 main-abcdef12，实得 %s", got)
	}
}

// TestBuildTagDifferentArgsProduceDifferentTag 多服务同 repo 构建场景：
// 同 app 同 commit 但 buildArgs 不同（SERVICE=product vs recommend）必须产出不同 tag，
// 否则 registry 同 tag 互相覆盖，各 BuildRun 记的 digest 与实际拉取内容不一致。
func TestBuildTagDifferentArgsProduceDifferentTag(t *testing.T) {
	base := Params{Branch: "main", Commit: "abcdef1234567890"}
	product := buildTag(base.withArgs(map[string]string{"SERVICE": "product"}))
	recommend := buildTag(base.withArgs(map[string]string{"SERVICE": "recommend"}))
	if product == recommend {
		t.Fatalf("不同 buildArgs 应产出不同 tag（多服务区分），均得 %s", product)
	}
	// 基础部分一致（branch-commit8 前缀）。
	if !strings.HasPrefix(product, "main-abcdef12-") || !strings.HasPrefix(recommend, "main-abcdef12-") {
		t.Fatalf("tag 应保留 branch-commit8 前缀，product=%s recommend=%s", product, recommend)
	}
}

// TestBuildTagSameArgsIdempotent 同 buildArgs 重构产出相同 tag（幂等，不产生垃圾 tag）。
func TestBuildTagSameArgsIdempotent(t *testing.T) {
	// 两次独立构造（等值 map 不同实例），tag 应一致（幂等，不产生垃圾 tag）。
	p1 := Params{Branch: "main", Commit: "abcdef12", BuildArgs: map[string]string{"SERVICE": "product", "VERSION": "1"}}
	p2 := Params{Branch: "main", Commit: "abcdef12", BuildArgs: map[string]string{"VERSION": "1", "SERVICE": "product"}}
	if buildTag(p1) != buildTag(p2) {
		t.Fatalf("同 buildArgs 重构 tag 应一致（幂等），got %s vs %s", buildTag(p1), buildTag(p2))
	}
}

// TestArgsHashStableRegardlessOfMapIterationOrder map 迭代序漂移不影响 hash（按 key 排序）。
func TestArgsHashStableRegardlessOfMapIterationOrder(t *testing.T) {
	a := map[string]string{"SERVICE": "product", "B": "2", "A": "1"}
	h1 := argsHash(a)
	for i := 0; i < 20; i++ { // 多次取应稳定（map 迭代序随机）
		if argsHash(a) != h1 {
			t.Fatal("argsHash 应不受 map 迭代序影响（按 key 排序）")
		}
	}
}

// TestBuildTagDockerfileHash dockerfile/buildContext 参与哈希（D1 修复 2026-09-04）：
// 切 Dockerfile 产物必变，tag 必须随之变化，否则同 tag 两 digest 破坏 registry 不可变语义；
// 默认值与存量形态（含 argsHash 段）零变化。
func TestBuildTagDockerfileHash(t *testing.T) {
	base := Params{Branch: "main", Commit: "abcdef1234567890"}
	// 1. 默认构建：tag 保持 branch-commit8（存量兼容，不追加 dfHash 段）。
	if got := buildTag(base); got != "main-abcdef12" {
		t.Fatalf("默认 dockerfile/context 不应追加段，got %s", got)
	}
	// 2. 有 buildArgs + 默认输入：形态与存量完全一致（argsHash 后不追加）。
	withArgs := base.withArgs(map[string]string{"SERVICE": "frontend"})
	if got, want := buildTag(withArgs), "main-abcdef12-"+argsHash(withArgs.BuildArgs); got != want {
		t.Fatalf("默认输入 + buildArgs 应与存量形态一致，got %s want %s", got, want)
	}
	// 3. 切 dockerfile：tag 必变（frontend nginx vs traffic-gen 同 buildArgs 场景）。
	frontend := withArgs
	frontend.Dockerfile = "paas-shop/frontend/Dockerfile"
	if buildTag(frontend) == buildTag(withArgs) {
		t.Fatal("切 dockerfile 应产出不同 tag（防同 tag 两 digest）")
	}
	// 4. 同 dockerfile 重构幂等。
	frontend2 := withArgs
	frontend2.Dockerfile = "paas-shop/frontend/Dockerfile"
	if buildTag(frontend) != buildTag(frontend2) {
		t.Fatal("同 dockerfile 重构 tag 应一致（幂等）")
	}
	// 5. 非默认 buildContext 同理参与。
	ctxCustom := withArgs
	ctxCustom.BuildContext = "paas-shop"
	if buildTag(ctxCustom) == buildTag(withArgs) {
		t.Fatal("切 buildContext 应产出不同 tag")
	}
	// 6. dfHash 全默认返回空。
	if dfHash("", "") != "" || dfHash("Dockerfile", ".") != "" {
		t.Fatal("全默认输入 dfHash 应为空（不追加段）")
	}
}

// withArgs 辅助构造（值传递后设 BuildArgs）。
func (p Params) withArgs(a map[string]string) Params {
	p.BuildArgs = a
	return p
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

// TestBuildTagSanitizesBranchSlash 带斜杠分支（feat/x、integration/x）必须产出合法 docker tag：
// 历史行为直接拼分支名，docker build -t 报 invalid reference format（2026-10-12 批次集成实测）。
func TestBuildTagSanitizesBranchSlash(t *testing.T) {
	got := buildTag(Params{Branch: "integration/agent-accept-3", Commit: "abcdef1234567890"})
	if got != "integration-agent-accept-3-abcdef12" {
		t.Fatalf("斜杠分支 tag 期望 integration-agent-accept-3-abcdef12，got %q", got)
	}
	// 无斜杠分支 tag 形态零变化（存量兼容）
	if got := buildTag(Params{Branch: "main", Commit: "abcdef1234567890"}); got != "main-abcdef12" {
		t.Fatalf("main 分支 tag 形态不应变化，got %q", got)
	}
}
