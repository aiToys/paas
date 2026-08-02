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

// Manifest 是 bundle 的清单（manifest.json）：版本 + 镜像 + chart + 每文件 sha256。
type Manifest struct {
	PaasVersion  string     `json:"paasVersion"`
	ChartVersion string     `json:"chartVersion"`
	ChartFile    string     `json:"chartFile"` // helm package 产物 .tgz
	Images       []ImageRef `json:"images"`
	// Files 文件名→sha256（含镜像 tar、chart tgz）。
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

// BuildManifest 在 bundleDir 内为给定 images/chart 计算 sha256 并写 manifest.json。
// paasVersion / chartVersion 用于校验镜像 tag 对齐；chartFile 为空则跳过该文件。
func BuildManifest(bundleDir, paasVersion, chartVersion, chartFile string, images []ImageRef, generatedAt string) (*Manifest, error) {
	m := &Manifest{
		PaasVersion:  paasVersion,
		ChartVersion: chartVersion,
		ChartFile:    chartFile,
		Images:       images,
		Files:        map[string]string{},
		GeneratedAt:  generatedAt,
	}
	// 收集所有需校验的非空文件名（chart + 各镜像 tar）。
	var candidates []string
	if chartFile != "" {
		candidates = append(candidates, chartFile)
	}
	for _, img := range images {
		if img.File != "" {
			candidates = append(candidates, img.File)
		}
	}
	sort.Strings(candidates)
	for _, f := range candidates {
		sum, err := ComputeSHA256(filepath.Join(bundleDir, f))
		if err != nil {
			return nil, fmt.Errorf("hash %s: %w", f, err)
		}
		m.Files[f] = sum
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(bundleDir, "manifest.json"), data, 0o600); err != nil {
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
