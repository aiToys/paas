// Package airsync 实现离线交付：bundle（公网打包）/ install（私有部署）/ verify（完整性校验）。
// 核心逻辑纯 Go；调 docker/helm/kubectl 走 os/exec（不引 client 库，KISS）。
package airsync

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
)

// ImageRef 是 bundle 内的一个镜像引用。
type ImageRef struct {
	Name   string `json:"name"`   // 镜像全名（含 registry/repo）
	Tag    string `json:"tag"`    // tag
	Digest string `json:"digest"` // sha256 digest（可选）
	File   string `json:"file"`   // bundle 内的 OCI tar 文件名
}

// ChartRef 是 bundle 内的一个上游 chart（helm pull 产物）。
type ChartRef struct {
	Repo    string `json:"repo"`    // 仓库引用（如 prometheus-community/prometheus）
	Version string `json:"version"` // chart 版本
	RepoURL string `json:"repoURL"` // 仓库地址（留档：复现打包 / 排查 chart 来源）
	File    string `json:"file"`    // bundle 内相对路径（charts/<name>-<ver>.tgz）
}

// ObservabilityManifest 是可观测栈条目（仅 --with-observability 打包时非空）。
// 三个上游 chart + deploy/observability 清单 + 部署脚本随包分发——离线目标机没有仓库，
// 只有 tarball，缺脚本则包内清单无任何部署路径（脚本是这套栈的唯一部署入口）。
type ObservabilityManifest struct {
	Charts []ChartRef `json:"charts"` // 3 个上游 chart
	Files  []string   `json:"files"`  // observability/ 下的清单文件（bundle 内相对路径）
	Script string     `json:"script"` // 部署脚本（bundle 内相对路径）
}

// Manifest 是 bundle 的清单（manifest.json）：版本 + 镜像 + chart + 每文件 sha256。
type Manifest struct {
	PaasVersion  string     `json:"paasVersion"`
	ChartVersion string     `json:"chartVersion"`
	ChartFile    string     `json:"chartFile"` // helm package 产物 .tgz
	Images       []ImageRef `json:"images"`
	// Observability 可观测栈条目（空 = 该 bundle 不含观测栈，install 时跳过其部署）。
	Observability *ObservabilityManifest `json:"observability,omitempty"`
	// Files 文件名→sha256（含镜像 tar、chart tgz、观测栈 chart 与清单）。
	Files       map[string]string `json:"files"`
	GeneratedAt string            `json:"generatedAt"`
}

// ComputeSHA256 计算文件 sha256（十六进制）。流式读取：镜像 tar 可达 GB 级，全量入内存会 OOM。
func ComputeSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ManifestInput 是 BuildManifest 的输入。用具名结构体而非长参数列表：paasVersion /
// chartVersion / chartFile 三个相邻字符串位置敏感，加观测栈后参数达 7 个，易错位。
type ManifestInput struct {
	BundleDir     string
	PaasVersion   string
	ChartVersion  string
	ChartFile     string
	Images        []ImageRef
	Observability *ObservabilityManifest
	GeneratedAt   string
}

// BuildManifest 在 BundleDir 内为给定 images/chart/观测栈计算 sha256 并写 manifest.json。
// ChartFile 为空则跳过该文件。
func BuildManifest(in ManifestInput) (*Manifest, error) {
	m := &Manifest{
		PaasVersion:   in.PaasVersion,
		ChartVersion:  in.ChartVersion,
		ChartFile:     in.ChartFile,
		Images:        in.Images,
		Observability: in.Observability,
		Files:         map[string]string{},
		GeneratedAt:   in.GeneratedAt,
	}
	// 收集所有需校验的非空文件名（chart + 各镜像 tar + 观测栈 chart/清单/脚本）。
	var candidates []string
	if in.ChartFile != "" {
		candidates = append(candidates, in.ChartFile)
	}
	for _, img := range in.Images {
		if img.File != "" {
			candidates = append(candidates, img.File)
		}
	}
	if obs := in.Observability; obs != nil {
		for _, c := range obs.Charts {
			candidates = append(candidates, c.File)
		}
		candidates = append(candidates, obs.Files...)
		if obs.Script != "" {
			candidates = append(candidates, obs.Script)
		}
	}
	sort.Strings(candidates)
	for _, f := range candidates {
		sum, err := ComputeSHA256(filepath.Join(in.BundleDir, f))
		if err != nil {
			return nil, fmt.Errorf("hash %s: %w", f, err)
		}
		m.Files[f] = sum
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(in.BundleDir, "manifest.json"), data, 0o600); err != nil {
		return nil, err
	}
	return m, nil
}

// LoadManifest 从 bundleDir 读 manifest.json。
func LoadManifest(bundleDir string) (*Manifest, error) {
	data, err := os.ReadFile(filepath.Join(bundleDir, "manifest.json"))
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// VerifyManifest 校验 bundleDir 内所有文件 sha256 与 manifest 一致。
// 返回 (manifest, 不一致文件列表, error)；manifest 缺失返回 error。
func VerifyManifest(bundleDir string) (*Manifest, []string, error) {
	m, err := LoadManifest(bundleDir)
	if err != nil {
		return nil, nil, fmt.Errorf("读 manifest: %w", err)
	}
	var mismatch []string
	for file, expected := range m.Files {
		actual, err := ComputeSHA256(filepath.Join(bundleDir, file))
		if err != nil {
			mismatch = append(mismatch, file+"(缺失)")
			continue
		}
		if actual != expected {
			mismatch = append(mismatch, file)
		}
	}
	sort.Strings(mismatch)
	return m, mismatch, nil
}
