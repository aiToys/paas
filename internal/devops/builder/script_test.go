package builder

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Execute the actual build script with a local Git repository and a fake Docker
// binary: moving the branch must not change the confirmed commit's build input.
func TestBuilderChecksOutConfirmedCommit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	dir := t.TempDir()
	repo := filepath.Join(dir, "repo")
	if err := os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...) //nolint:gosec // 测试内执行固定 git 子命令
		cmd.Dir = repo
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-b", "feature")
	version := filepath.Join(repo, "version")
	if err := os.WriteFile(version, []byte("confirmed"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "version")
	git("-c", "user.name=test", "-c", "user.email=test@example.invalid", "commit", "-m", "confirmed")
	sha := git("rev-parse", "HEAD")
	if err := os.WriteFile(version, []byte("new-head"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "version")
	git("-c", "user.name=test", "-c", "user.email=test@example.invalid", "commit", "-m", "moved")
	bin := filepath.Join(dir, "bin")
	_ = os.Mkdir(bin, 0700)
	docker := `#!/bin/sh
case "$1" in
 build) cat version > "$CAPTURE";;
 inspect) echo 'example@sha256:test';;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(docker), 0700); err != nil { //nolint:gosec // 测试桩脚本，需可执行权限
		t.Fatal(err)
	}

	t.Run("process-builder-pinned", func(t *testing.T) {
		capture := filepath.Join(dir, "capture-process")
		t.Setenv("CAPTURE", capture)
		_, err := (Real{DockerBinary: filepath.Join(bin, "docker")}).Build(context.Background(), Params{AppID: "local", GitURL: "file://" + repo, Branch: "feature", Commit: sha, Registry: "example.invalid"})
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(capture) //nolint:gosec // 测试临时目录路径
		if err != nil || string(got) != "confirmed" {
			t.Fatalf("process builder used %q (%v)", got, err)
		}
	})
	for _, tc := range []struct {
		name, commit, want string
		fails              bool
	}{{"pinned", sha, "confirmed", false}, {"head", "", "new-head", false}, {"missing", strings.Repeat("b", 40), "", true}} {
		t.Run(tc.name, func(t *testing.T) {
			capture := filepath.Join(dir, "capture-"+tc.name)
			script := strings.ReplaceAll(builderScript, "/workspace", filepath.Join(dir, "workspace-"+tc.name))
			cmd := exec.Command("sh", "-c", script) //nolint:gosec // 测试桩脚本
			cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "CLONE_URL=file://"+repo, "BRANCH=feature", "COMMIT="+tc.commit, "REF=example:test", "REGISTRY_USER=", "DOCKERFILE=", "BUILD_CONTEXT=.", "BUILD_ARG_FLAGS=", "CAPTURE="+capture)
			out, err := cmd.CombinedOutput()
			if tc.fails {
				if err == nil {
					t.Fatal("missing commit unexpectedly built")
				}
				if _, err := os.Stat(capture); !os.IsNotExist(err) {
					t.Fatal("Docker ran without confirmed source")
				}
				return
			}
			if err != nil {
				t.Fatalf("script: %v %s", err, out)
			}
			got, err := os.ReadFile(capture) //nolint:gosec // 测试临时目录路径
			if err != nil || string(got) != tc.want {
				t.Fatalf("built %q, want %q (%v)", got, tc.want, err)
			}
		})
	}
}
