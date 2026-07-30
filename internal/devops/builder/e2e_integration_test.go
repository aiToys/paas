//go:build integration

package builder

// 端到端集成测试：验证 Real pipeline 的 git clone → docker build → docker push → digest 解析全链路。
// 需宿主机 git/docker 可用 + 本地 registry（docker run -d -p 5000:5000 registry:2）。
// 触发：PAAS_DEVOPS_E2E=1 go test -tags=integration ./internal/devops/builder/ -run TestE2ERealBuild -v -timeout 10m
// 默认不跑（无 env 跳过）。

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestE2ERealBuild 用临时 git 仓库（含最小 Dockerfile）+ 本地 registry 跑完整 Real.Build。
func TestE2ERealBuild(t *testing.T) {
	if os.Getenv("PAAS_DEVOPS_E2E") == "" {
		t.Skip("跳过：需 PAAS_DEVOPS_E2E=1 触发（要求 git/docker/本地 registry）")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git 不可用")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker 不可用")
	}
	// 本地 registry 可达性检查（默认 5000）。
	registry := os.Getenv("PAAS_REGISTRY")
	if registry == "" {
		registry = "localhost:5000"
	}
	if !portOpen(registry) {
		t.Skipf("registry %s 不可达（先 docker run -d -p 5000:5000 registry:2）", registry)
	}

	// 构造临时 git 仓库（含最小 Dockerfile）。
	tmp := t.TempDir()
	repoDir := filepath.Join(tmp, "repo")
	if err := os.MkdirAll(repoDir, 0o750); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) error {
		cmd := exec.Command("git", args...)
		cmd.Dir = repoDir
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("git %s: %s\n%s", args, err, out)
		}
		return nil
	}
	if err := git("init", "-b", "main"); err != nil {
		t.Fatal(err)
	}
	if err := git("config", "user.email", "e2e@paas.local"); err != nil {
		t.Fatal(err)
	}
	if err := git("config", "user.name", "e2e"); err != nil {
		t.Fatal(err)
	}
	// 最小 Dockerfile（busybox 打印 hello）。
	if err := os.WriteFile(filepath.Join(repoDir, "Dockerfile"),
		[]byte("FROM busybox\nCMD [\"echo\", \"hello\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := git("add", "."); err != nil {
		t.Fatal(err)
	}
	if err := git("commit", "-m", "init"); err != nil {
		t.Fatal(err)
	}

	var r Real
	r.Registry = registry
	res, err := r.Build(context.Background(), Params{
		AppID: "e2e-app", Branch: "main", BuildID: "e2e-build",
		GitURL: "file://" + repoDir,
	})
	if err != nil {
		t.Fatalf("Real.Build 失败: %v\n日志:\n%s", err, res.Log)
	}
	if !strings.HasPrefix(res.Digest, "sha256:") {
		t.Fatalf("digest 应 sha256: 前缀，实得 %s", res.Digest)
	}
	if res.Tag == "" {
		t.Fatal("tag 不应为空")
	}
	t.Logf("✅ 端到端构建成功: digest=%s tag=%s", res.Digest, res.Tag)
}

func portOpen(addr string) bool {
	dialer := net.Dialer{Timeout: 2 * time.Second}
	conn, err := dialer.Dial("tcp", addr)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}
