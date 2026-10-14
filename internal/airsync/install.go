package airsync

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// InstallConfig 是 install 子命令的输入配置。
type InstallConfig struct {
	Bundle    string // bundle 文件（.tar.gz）
	TargetReg string // 私有 registry（如 registry.private.com）
	Namespace string // K8s namespace
	Runner    CmdRunner
}

// Run 执行 install：解包 → verify 完整性 → docker load/retag/push → helm install。
func (c InstallConfig) Run() error {
	if c.Runner == nil {
		c.Runner = DefaultRunner
	}
	work, err := os.MkdirTemp("", "airsync-install-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(work) }()

	if err := UnTarGz(c.Bundle, work); err != nil {
		return fmt.Errorf("解包: %w", err)
	}
	m, mismatch, err := VerifyManifest(work)
	if err != nil {
		return err
	}
	if len(mismatch) != 0 {
		return fmt.Errorf("完整性校验失败（损坏/篡改）: %v", mismatch)
	}
	// docker load + retag + push 到私有 registry
	for _, img := range m.Images {
		full := img.Name + ":" + img.Tag
		target := fmt.Sprintf("%s/%s:%s", c.TargetReg, basename(img.Name), img.Tag)
		if _, err := c.Runner.Run("docker", "load", "-i", filepath.Join(work, img.File)); err != nil {
			return fmt.Errorf("docker load %s: %w", img.File, err)
		}
		if _, err := c.Runner.Run("docker", "tag", full, target); err != nil {
			return fmt.Errorf("docker tag: %w", err)
		}
		if _, err := c.Runner.Run("docker", "push", target); err != nil {
			return fmt.Errorf("docker push %s: %w", target, err)
		}
	}
	// helm upgrade --install（image.registry 指向私有 registry）。
	// core 镜像 + postgres 镜像都覆盖到私有 registry——否则离线集群 postgres Pod 仍拉公网 postgres:16-alpine 失败。
	chartPath := filepath.Join(work, m.ChartFile)
	if _, err := c.Runner.Run("helm", "upgrade", "--install", "paas", chartPath,
		"--namespace", c.Namespace, "--create-namespace",
		"--set", fmt.Sprintf("image.registry=%s", c.TargetReg),
		"--set", "image.repository=paas-core",
		"--set", fmt.Sprintf("image.tag=%s", m.PaasVersion),
		"--set", fmt.Sprintf("postgres.image=%s/postgres:16-alpine", c.TargetReg),
		"--wait"); err != nil {
		return fmt.Errorf("helm install: %w", err)
	}
	// 包内含观测栈则接着部署（控制面先行：观测栈的 paas-core 抓取 job 依赖控制面已就绪）。
	// 逻辑不在 Go 里重写——包内脚本是这套栈的唯一部署入口（渲染 → 预检 diff → 应用 → 断言）。
	if m.Observability != nil {
		if err := c.installObservability(work, m.Observability); err != nil {
			return err
		}
	}
	return nil
}

// installObservability 调包内脚本部署可观测栈：chart 目录与清单目录都用解包后的绝对路径，
// 故脚本 cwd 无关（离线目标机没有仓库，脚本必须能在任意目录跑）。
//
// 其余配置（NS_OBS / DOMAIN_SUFFIX / STORAGE_CLASS / GRAFANA_ADMIN_PASSWORD / 镜像 registry）
// 由脚本自带默认值 + 调用方导出的环境变量决定——本函数用 env(1) 显式传，不覆盖调用方已导出的值，
// 唯一例外是 MIRROR：离线环境默认 daocloud 中转站必然不可达，未显式指定时置 none。
//
// 固定带 --yes：install 无人值守（常在 CI/无 TTY），脚本预检发现漂移时 read 在 stdin EOF
// 返回非零、配合 set -e 会中断安装；包内文件均已过 sha256 verify，无需人工确认。
func (c InstallConfig) installObservability(work string, obs *ObservabilityManifest) error {
	args := []string{
		"OBS_CHART_DIR=" + filepath.Join(work, bundleChartsDir),
		"OBS_MANIFEST_DIR=" + filepath.Join(work, bundleObsDir),
	}
	if os.Getenv("MIRROR") == "" {
		args = append(args, "MIRROR=none")
	}
	args = append(args, "bash", filepath.Join(work, obs.Script), "--yes")
	if _, err := c.Runner.Run("env", args...); err != nil {
		return fmt.Errorf("部署可观测栈（%s）: %w", obs.Script, err)
	}
	return nil
}

// basename 取镜像名的最后一段（repo 名，去 registry 前缀）。
func basename(name string) string {
	for i := len(name) - 1; i >= 0; i-- {
		if name[i] == '/' {
			return name[i+1:]
		}
	}
	return name
}

// UnTarGz 解包 .tar.gz 到 dst。
func UnTarGz(bundlePath, dst string) error {
	f, err := os.Open(bundlePath)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		out := filepath.Join(dst, hdr.Name)
		// 防 tar slip（路径逃逸到 dst 之外）。
		rel, err := filepath.Rel(dst, out)
		if err != nil || strings.HasPrefix(rel, "..") {
			return fmt.Errorf("非法路径（可能的 tar slip）: %s", hdr.Name)
		}
		// 目录条目：创建后跳过（不当文件 Create）。
		if hdr.Typeflag == tar.TypeDir {
			if err := os.MkdirAll(out, os.FileMode(hdr.Mode&0o777)); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(out), 0o750); err != nil {
			return err
		}
		w, err := os.Create(out)
		if err != nil {
			return err
		}
		// 限流解压：单条目上限 maxExtractBytes 防 zip/tar 炸弹（gosec G110）。
		n, err := io.Copy(w, io.LimitReader(tr, maxExtractBytes+1))
		_ = w.Close()
		if err != nil {
			return err
		}
		if n > maxExtractBytes {
			return fmt.Errorf("条目过大（疑似解压炸弹）: %s", hdr.Name)
		}
		// 还原权限位（os.Create 恒为 0644，会丢掉包内脚本的执行位）。
		// 掩 0o777 显式排除 setuid/setgid/sticky——打包方可信（sha256 已校验），但不该由 tar 决定特权位。
		if err := os.Chmod(out, os.FileMode(hdr.Mode&0o777)); err != nil {
			return err
		}
	}
	return nil
}

// maxExtractBytes 单条目解压上限（10GiB，覆盖大模型镜像层，拦截恶意炸弹）。
const maxExtractBytes = 10 * 1024 * 1024 * 1024
