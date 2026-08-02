package airsync

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// BundleConfig 是 bundle 子命令的输入配置。
type BundleConfig struct {
	Version  string // paas 版本（如 0.1.0）
	Registry string // 源 registry（如 ghcr.io/aitoys）
	ChartDir string // Helm chart 目录（如 deploy/charts/paas）
	Out      string // 输出 bundle 文件（.tar.gz）
	Runner   CmdRunner
}

// Run 执行 bundle：helm package + docker save 各镜像 + 生成 manifest（含 sha256）+ 打包 tar.gz。
func (c BundleConfig) Run() error {
	if c.Runner == nil {
		c.Runner = DefaultRunner
	}
	work, err := os.MkdirTemp("", "airsync-bundle-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(work) }()

	chartTgz := fmt.Sprintf("paas-%s.tgz", c.Version)

	// helm package chart → work/paas-<v>.tgz
	if _, err := c.Runner.Run("helm", "package", c.ChartDir, "--version", c.Version, "--destination", work); err != nil {
		return fmt.Errorf("helm package: %w", err)
	}

	// 镜像列表（core + postgres）。tag 与 chart appVersion 对齐（manifest 校验）。
	images := []ImageRef{
		{Name: c.Registry + "/paas-core", Tag: c.Version, File: fmt.Sprintf("core-%s.tar", c.Version)},
		{Name: "postgres", Tag: "16-alpine", File: "postgres-16-alpine.tar"},
	}
	for _, img := range images {
		full := img.Name + ":" + img.Tag
		if _, err := c.Runner.Run("docker", "save", "-o", filepath.Join(work, img.File), full); err != nil {
			return fmt.Errorf("docker save %s: %w", full, err)
		}
	}

	// 生成 manifest（含每文件 sha256）。
	if _, err := BuildManifest(work, c.Version, c.Version, chartTgz, images, "airsync-bundle"); err != nil {
		return err
	}

	return tarGz(work, c.Out)
}

// tarGz 把 srcDir 所有文件打包到 outPath。
func tarGz(srcDir, outPath string) error {
	out, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer func() { _ = out.Close() }()
	gz := gzip.NewWriter(out)
	defer func() { _ = gz.Close() }()
	tw := tar.NewWriter(gz)
	defer func() { _ = tw.Close() }()
	return filepath.Walk(srcDir, func(path string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(srcDir, path)
		hdr := &tar.Header{Name: rel, Mode: int64(fi.Mode()), Size: fi.Size()}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		// 流式写：镜像 tar 可达 GB 级，os.ReadFile 全量入内存会 OOM（离线交付机器内存有限）。
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		_, err = io.Copy(tw, f)
		_ = f.Close()
		return err
	})
}
