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
	defer os.RemoveAll(work)

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
	// helm upgrade --install（image.registry 指向私有 registry）
	chartPath := filepath.Join(work, m.ChartFile)
	if _, err := c.Runner.Run("helm", "upgrade", "--install", "paas", chartPath,
		"--namespace", c.Namespace, "--create-namespace",
		"--set", fmt.Sprintf("image.registry=%s", c.TargetReg),
		"--set", "image.repository=paas-core",
		"--set", fmt.Sprintf("image.tag=%s", m.PaasVersion),
		"--wait"); err != nil {
		return fmt.Errorf("helm install: %w", err)
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
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
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
	}
	return nil
}

// maxExtractBytes 单条目解压上限（10GiB，覆盖大模型镜像层，拦截恶意炸弹）。
const maxExtractBytes = 10 * 1024 * 1024 * 1024
