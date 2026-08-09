package builder

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Real 是真实构建流水线：git clone（tempdir）→ docker build → docker push，
// 解析 registry digest。依赖宿主机 git/docker CLI 可用；不可达返错（Store 记 failed）。
//
// 安全：所有参数经 os/exec（不经 shell）传递，防命令注入；clone 到 os.MkdirTemp 隔离目录。
type Real struct {
	// GitBinary/DockerBinary 可注入（测试）；空则 PATH 查找 git/docker。
	GitBinary    string
	DockerBinary string
	// 全局凭证/仓库配置（cmd/core 从 env 注入，所有构建共用；Params 字段为空时回退到此）。
	Registry     string
	GitToken     string
	RegistryUser string
	RegistryPass string
}

// Real 标记（Store 据此为构建预留更长超时）。
func (Real) Real() bool { return true }

// Build 执行 clone→build→push，返不可变 digest。
func (r Real) Build(ctx context.Context, p Params) (Result, error) {
	// 全局配置回退（Params 单条为空时用 Real 配置）。
	if p.Registry == "" {
		p.Registry = r.Registry
	}
	if p.GitToken == "" {
		p.GitToken = r.GitToken
	}
	if p.RegistryUser == "" {
		p.RegistryUser = r.RegistryUser
	}
	if p.RegistryPass == "" {
		p.RegistryPass = r.RegistryPass
	}
	log := &strings.Builder{}
	gitBin := r.GitBinary
	if gitBin == "" {
		gitBin = "git"
	}
	dockerBin := r.DockerBinary
	if dockerBin == "" {
		dockerBin = "docker"
	}

	// 1. git clone 到 tempdir（depth 1 浅克隆省带宽）。
	dir, err := os.MkdirTemp("", "paas-build-*")
	if err != nil {
		return Result{}, fmt.Errorf("创建构建目录失败: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	cloneURL := p.GitURL
	if p.GitToken != "" {
		cloneURL = injectToken(p.GitURL, p.GitToken)
	}
	cloneCtx, cancel := timeout(ctx, 5*time.Minute)
	defer cancel()
	if out, err := runCmd(cloneCtx, log, dir, gitBin, "clone", "--depth", "1", "--branch", p.Branch, cloneURL, "."); err != nil {
		return Result{Log: log.String()}, fmt.Errorf("git clone 失败: %w\n%s", err, out)
	}

	// clone 后取真实 commit（HEAD）。
	if p.Commit == "" {
		out, err := runCmd(ctx, log, dir, gitBin, "rev-parse", "HEAD")
		if err != nil {
			return Result{Log: log.String()}, fmt.Errorf("取 commit 失败: %w\n%s", err, out)
		}
		p.Commit = strings.TrimSpace(currentLine(out))
	}

	// 2. docker build（指定 Dockerfile 与构建上下文）。
	dockerfile := p.Dockerfile
	if dockerfile == "" {
		dockerfile = "Dockerfile"
	}
	buildCtx := p.BuildContext
	if buildCtx == "" {
		buildCtx = "."
	}
	tag := p.Branch + "-" + safeShort(p.Commit, 8)
	ref := ImageRef(p, tag)

	// 可选 docker login（私有 registry 凭证）。
	if p.RegistryUser != "" || p.RegistryPass != "" {
		loginCtx, cancelLogin := timeout(ctx, time.Minute)
		defer cancelLogin()
		// docker login 用 stdin 传密码（不经 argv，不泄漏到进程列表）。
		if err := runCmdStdin(loginCtx, log, dir, dockerBin, p.RegistryUser, p.RegistryPass, RegistryOrDefault(p)); err != nil {
			return Result{Log: log.String()}, fmt.Errorf("docker login 失败: %w", err)
		}
	}

	buildCtx2, cancelBuild := timeout(ctx, 20*time.Minute)
	defer cancelBuild()
	dockerfileAbs := dockerfile
	if !filepath.IsAbs(dockerfileAbs) {
		dockerfileAbs = filepath.Join(dir, dockerfile)
	}
	buildArgs := []string{"build", "-t", ref, "-f", dockerfileAbs}
	for k, v := range p.BuildArgs {
		buildArgs = append(buildArgs, "--build-arg", k+"="+v)
	}
	buildArgs = append(buildArgs, buildCtx)
	if out, err := runCmd(buildCtx2, log, dir, dockerBin, buildArgs...); err != nil {
		return Result{Log: log.String()}, fmt.Errorf("docker build 失败: %w\n%s", err, out)
	}

	// 3. docker push。
	pushCtx, cancelPush := timeout(ctx, 15*time.Minute)
	defer cancelPush()
	if out, err := runCmd(pushCtx, log, dir, dockerBin, "push", ref); err != nil {
		return Result{Log: log.String()}, fmt.Errorf("docker push 失败: %w\n%s", err, out)
	}

	// 4. 解析 digest（registry 不可变标识）。优先 RepoDigests，回退镜像 ID。
	digest, err := resolveDigest(ctx, dockerBin, ref)
	if err != nil {
		return Result{Log: log.String()}, fmt.Errorf("解析 digest 失败: %w", err)
	}
	return Result{Digest: digest, Tag: tag, Log: log.String()}, nil
}

// runCmd 在 workdir 执行命令，stdout/stderr 追加到 log，返回 combined output。
func runCmd(ctx context.Context, log *strings.Builder, dir, name string, args ...string) (string, error) {
	fmt.Fprintf(log, "$ %s %s\n", name, strings.Join(args, " "))
	cmd := exec.CommandContext(ctx, name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	log.WriteString(buf.String())
	return buf.String(), err
}

// runCmdStdin 类似 runCmd，但经 stdin 传 password（docker login 用，避免进程列表泄漏）。
// user 经 -u 参数，password 经 -p stdin（--password-stdin）。
func runCmdStdin(ctx context.Context, log *strings.Builder, dir, name, user, pass, registry string) error {
	fmt.Fprintf(log, "$ %s login -u %s %s\n", name, user, registry)
	cmd := exec.CommandContext(ctx, name, "login", "-u", user, "--password-stdin", registry)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Stdin = strings.NewReader(pass + "\n")
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	log.WriteString(buf.String())
	return err
}

// resolveDigest 取镜像 digest：先试 RepoDigests（push 后的 registry digest），回退 .Id（本地）。
func resolveDigest(ctx context.Context, dockerBin, ref string) (string, error) {
	// {{json .RepoDigests}} → ["repo@sha256:..."]
	out, err := exec.CommandContext(ctx, dockerBin, "inspect", "--format",
		`{{range .RepoDigests}}{{.}}{{end}}`, ref).Output()
	if err == nil {
		s := strings.TrimSpace(string(out))
		if i := strings.Index(s, "@"); i >= 0 {
			return s[i+1:], nil // sha256:...
		}
	}
	// 回退：本地镜像 ID（digest 兜底）。
	out, err = exec.CommandContext(ctx, dockerBin, "inspect", "--format", "{{.Id}}", ref).Output()
	if err != nil {
		return "", fmt.Errorf("docker inspect 失败: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// injectToken 把 token 注入 HTTPS git URL（https://token@host/...）。
// 已含凭证（URL 含 @，如内置 Gitea 的 http://paas-bot:pass@... CloneURL）原样返回；
// 非 HTTPS（含 Gitea 内网 http）原样返回--git 用 URL 内 basic auth clone。
func injectToken(url, token string) string {
	if strings.Contains(url, "@") {
		return url // 已含凭证（internal Gitea CloneURL 或用户填的含凭证 URL）
	}
	if !strings.HasPrefix(url, "https://") {
		return url
	}
	return "https://" + token + "@" + strings.TrimPrefix(url, "https://")
}

func timeout(parent context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, d)
}

// currentLine 取多行输出的最后一行非空内容（git rev-parse 输出可能带警告行）。
func currentLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			return l
		}
	}
	return ""
}
