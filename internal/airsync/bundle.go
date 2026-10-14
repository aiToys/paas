package airsync

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// bundle 内观测栈的固定相对路径（install 侧按同样约定解包后定位）。
const (
	bundleChartsDir  = "charts"        // 上游 chart tgz（OBS_CHART_DIR 指向此目录）
	bundleObsDir     = "observability" // deploy/observability/* 清单（OBS_MANIFEST_DIR 指向此目录）
	bundleScriptPath = "scripts/deploy-observability.sh"
)

// BundleConfig 是 bundle 子命令的输入配置。
type BundleConfig struct {
	Version  string // paas 版本（如 0.1.0）
	Registry string // 源 registry（如 ghcr.io/aitoys）
	ChartDir string // Helm chart 目录（如 deploy/charts/paas）
	Out      string // 输出 bundle 文件（.tar.gz）
	Runner   CmdRunner
	// WithObservability 把可观测栈一并打包：3 个上游 chart（helm pull）+ deploy/observability/*
	// 清单 + 部署脚本本身。默认关闭，保持既有交付件字节不变（存量客户按原样交付）。
	WithObservability bool
	// ObservabilityDir 是清单目录（含 grafana-values.yaml 等）。
	ObservabilityDir string
	// ScriptPath 是可观测栈部署脚本。**必须随包分发**——离线目标机没有仓库，只有 tarball，
	// 包内清单若缺脚本则无任何部署路径（脚本是这套栈的唯一入口）。
	ScriptPath string
}

// Run 执行 bundle：helm package + docker save 各镜像 + 生成 manifest（含 sha256）+ 打包 tar.gz。
func (c BundleConfig) Run() error {
	if c.Runner == nil {
		c.Runner = DefaultRunner
	}
	if c.WithObservability {
		if c.ObservabilityDir == "" {
			c.ObservabilityDir = "deploy/observability"
		}
		if c.ScriptPath == "" {
			c.ScriptPath = "scripts/deploy-observability.sh"
		}
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

	// 观测栈（可选）：chart + 清单 + 脚本。
	var obs *ObservabilityManifest
	if c.WithObservability {
		if obs, err = c.bundleObservability(work); err != nil {
			return err
		}
	}

	// 生成 manifest（含每文件 sha256）。
	if _, err := BuildManifest(ManifestInput{
		BundleDir:     work,
		PaasVersion:   c.Version,
		ChartVersion:  c.Version,
		ChartFile:     chartTgz,
		Images:        images,
		Observability: obs,
		GeneratedAt:   "airsync-bundle",
	}); err != nil {
		return err
	}

	return tarGz(work, c.Out)
}

// bundleObservability 把可观测栈打进 work：helm pull 三个上游 chart → work/charts/；
// 清单拷到 work/observability/；脚本拷到 work/scripts/。返回 manifest 条目。
//
// chart 的「名/版本/仓库 URL」真源在 scripts/deploy-observability.sh（--chart-versions 查询），
// 不在此重复硬编码——版本号两处各写一份必然漂移，而 chart 版本与清单里的镜像/CRD 强耦合。
func (c BundleConfig) bundleObservability(work string) (*ObservabilityManifest, error) {
	charts, err := c.resolveObsCharts()
	if err != nil {
		return nil, err
	}
	chartsDir := filepath.Join(work, bundleChartsDir)
	if err := os.MkdirAll(chartsDir, 0o750); err != nil {
		return nil, err
	}
	// 按仓库分组 add 一次（--force-update 幂等，本地已有该仓库名时不会报错）。
	added := map[string]bool{}
	for i := range charts { // 按索引取址：ch := range 是副本，写 ch.File 不会落到切片元素
		ch := &charts[i]
		repo, _, ok := strings.Cut(ch.Repo, "/")
		if !ok {
			return nil, fmt.Errorf("chart 仓库引用非法（应为 <repo>/<name>）: %s", ch.Repo)
		}
		if !added[repo] {
			if _, err := c.Runner.Run("helm", "repo", "add", repo, ch.RepoURL, "--force-update"); err != nil {
				return nil, fmt.Errorf("helm repo add %s: %w", repo, err)
			}
			added[repo] = true
		}
		if _, err := c.Runner.Run("helm", "pull", ch.Repo, "--version", ch.Version,
			"--destination", chartsDir); err != nil {
			return nil, fmt.Errorf("helm pull %s --version %s: %w", ch.Repo, ch.Version, err)
		}
		ch.File = filepath.Join(bundleChartsDir, fmt.Sprintf("%s-%s.tgz", basename(ch.Repo), ch.Version))
		if _, err := os.Stat(filepath.Join(work, ch.File)); err != nil {
			// helm pull 的产物名是 <name>-<version>.tgz；对不上说明 chart 名或版本与脚本声明不符。
			return nil, fmt.Errorf("helm pull 未产出预期文件 %s（检查脚本 --chart-versions 与仓库实际 chart 名）: %w", ch.File, err)
		}
	}

	// 清单目录：deploy/observability/* → work/observability/（只取 *.yaml，目录内无其它交付物）。
	obsDir := filepath.Join(work, bundleObsDir)
	if err := os.MkdirAll(obsDir, 0o750); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(c.ObservabilityDir)
	if err != nil {
		return nil, fmt.Errorf("读清单目录 %s: %w", c.ObservabilityDir, err)
	}
	var files []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		if err := copyFile(filepath.Join(c.ObservabilityDir, e.Name()), filepath.Join(obsDir, e.Name())); err != nil {
			return nil, err
		}
		files = append(files, filepath.Join(bundleObsDir, e.Name()))
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("清单目录 %s 无 *.yaml（观测栈清单缺失，拒绝打半包）", c.ObservabilityDir)
	}
	sort.Strings(files)

	// 部署脚本（可执行位保留——install 侧直接 bash 调用，但不依赖可执行位）。
	if err := copyFile(c.ScriptPath, filepath.Join(work, bundleScriptPath)); err != nil {
		return nil, fmt.Errorf("拷入部署脚本 %s: %w", c.ScriptPath, err)
	}

	return &ObservabilityManifest{Charts: charts, Files: files, Script: bundleScriptPath}, nil
}

// resolveObsCharts 调部署脚本的 --chart-versions 取三个 chart 的「仓库/名 版本 仓库URL」。
// 以脚本为单一真源，避免 Go 侧另写一份版本常量。
func (c BundleConfig) resolveObsCharts() ([]ChartRef, error) {
	out, err := c.Runner.Run("bash", c.ScriptPath, "--chart-versions")
	if err != nil {
		return nil, fmt.Errorf("取 chart 版本清单（%s --chart-versions）: %w", c.ScriptPath, err)
	}
	var charts []ChartRef
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) != 3 {
			return nil, fmt.Errorf("chart 版本行格式非法（应为「仓库/名 版本 仓库URL」）: %q", line)
		}
		charts = append(charts, ChartRef{Repo: fields[0], Version: fields[1], RepoURL: fields[2]})
	}
	if len(charts) == 0 {
		return nil, fmt.Errorf("%s --chart-versions 无输出（脚本被改坏？）", c.ScriptPath)
	}
	return charts, nil
}

// copyFile 复制单文件（保留源权限位；脚本需 0755）。自动建父目录（scripts/ 等）。
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	fi, err := in.Stat()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, fi.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
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
