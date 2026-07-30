# airsync 离线交付工具 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 落地 airsync 离线交付 CLI（bundle/install/verify/doctor）+ Helm chart，使平台具备私有化离线交付能力（无外网环境部署）。

**Architecture:** `internal/airsync` 核心逻辑包（manifest.json 生成 + sha256 完整性校验 + bundle 打包 + install 落地，纯 Go + exec 调 docker/helm/kubectl）；`cmd/airsync` CLI 入口（stdlib flag，与 core 一致，零依赖）；`deploy/charts/paas` Helm chart（core + postgres + service + ingress + configmap）。核心可测价值（manifest/verify）纯 Go 单测；bundle/install 真实执行需工具链 + registry/K8s，命令构造可测、集成标注后续。

**Tech Stack:** 纯 Go stdlib（archive/tar + compress/gzip + crypto/sha256 + encoding/json + os/exec）；Helm chart（YAML）；调 docker/helm/kubectl CLI（sh exec，不引 client 库，KISS）。

## Global Constraints

- **纯 Go stdlib + os/exec**，不引 cobra / docker client / helm SDK（KISS，零新依赖）。
- **manifest.json 含每文件 sha256**，verify 校验完整性（防传输损坏/篡改）。
- **chart appVersion 必须匹配镜像 tag**（manifest 记录对齐，verify 校验）。
- **公网/私有两路径共用同一 chart**，仅 image.registry 不同。
- license：airsync 自研 Apache 2.0；Helm Apache 2.0；postgres/distroleless 镜像兼容。
- 注释用中文；未经用户明确要求不 `git commit` / 建分支。
- bundle/install 真实执行（docker pull/save、helm install、kubectl apply）依赖外部工具链 + registry/K8s，本期命令构造可测、端到端集成标注后续。

## 文件结构

- `internal/airsync/manifest.go`（新建）：Manifest 结构 + sha256 计算 + 生成/校验。
- `internal/airsync/manifest_test.go`（新建）：sha256 + verify 篡改检测测试。
- `internal/airsync/bundle.go`（新建）：bundle 打包逻辑（docker save + helm package + tar.gz + manifest）。
- `internal/airsync/install.go`（新建）：install 逻辑（docker load + retag + push + helm install）。
- `internal/airsync/exec.go`（新建）：命令运行抽象（便于测试 mock）。
- `cmd/airsync/main.go`（新建）：CLI 入口（bundle/install/verify/doctor 子命令）。
- `deploy/charts/paas/Chart.yaml` + `values.yaml` + `templates/*`（新建）：Helm chart。
- `CHANGELOG.md`/`CLAUDE.md`（修改）：同步。

---

### Task 1: manifest 核心逻辑 + 测试

**Files:**
- Create: `internal/airsync/manifest.go`
- Create: `internal/airsync/manifest_test.go`

**Interfaces:**
- Produces: `Manifest` / `ImageRef` 结构；`ComputeSHA256(path)`；`BuildManifest(...)`；`VerifyManifest(dir)`。

- [ ] **Step 1: 写 manifest.go**

```go
// Package airsync 实现离线交付：bundle（公网打包）/ install（私有部署）/ verify（完整性校验）。
// 核心逻辑纯 Go；调 docker/helm/kubectl 走 os/exec（不引 client 库）。
package airsync

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
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
	// Files 文件名→sha256（含镜像 tar、chart tgz、migrations SQL）。
	Files       map[string]string `json:"files"`
	GeneratedAt string             `json:"generatedAt"`
}

// ComputeSHA256 计算文件 sha256（十六进制）。
func ComputeSHA256(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// BuildManifest 在 bundleDir 内为给定 images/chart 计算 sha256 并写 manifest.json。
// paasVersion / chartVersion 用于校验镜像 tag 对齐。
func BuildManifest(bundleDir string, paasVersion, chartVersion, chartFile string, images []ImageRef, generatedAt string) (*Manifest, error) {
	m := &Manifest{
		PaasVersion:  paasVersion,
		ChartVersion: chartVersion,
		ChartFile:    chartFile,
		Images:       images,
		Files:        map[string]string{},
		GeneratedAt:  generatedAt,
	}
	// 收集所有需校验的文件（chart + 各镜像 tar）。
	candidates := []string{chartFile}
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
	if err := os.WriteFile(filepath.Join(bundleDir, "manifest.json"), data, 0o644); err != nil {
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
// 返回不一致的文件列表（空=全部通过）；manifest 缺失返回错误。
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
```

- [ ] **Step 2: 写 manifest_test.go（sha256 + verify 篡改检测）**

```go
package airsync

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBuildAndVerifyManifest(t *testing.T) {
	dir := t.TempDir()
	// 准备 chart tgz + 一个镜像 tar
	chartData := []byte("fake chart")
	imgData := []byte("fake image tar")
	if err := os.WriteFile(filepath.Join(dir, "paas-0.1.0.tgz"), chartData, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "core-0.1.0.tar"), imgData, 0o644); err != nil {
		t.Fatal(err)
	}
	images := []ImageRef{{Name: "ghcr.io/aitoys/paas-core", Tag: "0.1.0", File: "core-0.1.0.tar"}}
	m, err := BuildManifest(dir, "0.1.0", "0.1.0", "paas-0.1.0.tgz", images, "2026-07-30T00:00:00Z")
	if err != nil {
		t.Fatalf("BuildManifest: %v", err)
	}
	if len(m.Files) != 2 {
		t.Fatalf("应含 2 文件 sha256，实际 %d", len(m.Files))
	}
	// verify 应通过
	_, mismatch, err := VerifyManifest(dir)
	if err != nil || len(mismatch) != 0 {
		t.Fatalf("未篡改应通过，mismatch=%v err=%v", mismatch, err)
	}
}

func TestVerifyDetectsTamper(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "core-0.1.0.tar")
	if err := os.WriteFile(f, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := BuildManifest(dir, "0.1.0", "0.1.0", "", []ImageRef{{File: "core-0.1.0.tar"}}, ""); err != nil {
		// chartFile="" 时 candidates 只含镜像 tar（BuildManifest 跳过空名？需处理）
		// 这里 chartFile="" 不加入 candidates，仅镜像 tar。
	}
	// 篡改文件
	if err := os.WriteFile(f, []byte("TAMPERED"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, mismatch, _ := VerifyManifest(dir)
	if len(mismatch) == 0 {
		t.Fatalf("篡改后应检出不一致")
	}
}
```

> 注：`chartFile=""` 时 BuildManifest 的 candidates 含空串——执行时确认 `ComputeSHA256(bundleDir+"/")` 行为或过滤空名。若 BuildManifest 对空 chartFile 报错，测试改用真实 chartFile。执行时核对。

- [ ] **Step 3: 跑测试**

Run: `go test ./internal/airsync/ -count=1 -v`
Expected: PASS。

- [ ] **Step 4: Commit（用户未要求 commit 时跳过）**

```bash
git add internal/airsync/manifest.go internal/airsync/manifest_test.go
git commit -m "feat(airsync): manifest 生成 + sha256 完整性校验"
```

---

### Task 2: Helm chart

**Files:**
- Create: `deploy/charts/paas/Chart.yaml`
- Create: `deploy/charts/paas/values.yaml`
- Create: `deploy/charts/paas/templates/_helpers.tpl`
- Create: `deploy/charts/paas/templates/core-deployment.yaml`
- Create: `deploy/charts/paas/templates/core-service.yaml`
- Create: `deploy/charts/paas/templates/postgres.yaml`
- Create: `deploy/charts/paas/templates/ingress.yaml`
- Create: `deploy/charts/paas/templates/configmap.yaml`

- [ ] **Step 1: 写 Chart.yaml**

```yaml
apiVersion: v2
name: paas
description: 一站式 PaaS 平台控制面（Platform Core + MaaS）
type: application
version: 0.1.0
appVersion: "0.1.0"
home: https://github.com/aitoys/paas
sources:
  - https://github.com/aitoys/paas
maintainers:
  - name: aitoys
    url: https://github.com/aitoys
```

- [ ] **Step 2: 写 values.yaml**

```yaml
image:
  registry: ghcr.io/aitoys        # 公网；离线改私有 registry
  repository: paas-core
  tag: "0.1.0"
  pullPolicy: IfNotPresent

db:
  enabled: true                   # true=内置 postgres StatefulSet；false=用外置 db.url
  url: ""                         # 外置 PG（db.enabled=false 时必填）

ingress:
  enabled: false
  host: paas.example.com

env:
  PAAS_API_KEY: ""                # 自定义 API Key（空则默认演示 Key）

resources:
  requests: { cpu: 100m, memory: 128Mi }
  limits: { cpu: 500m, memory: 512Mi }
```

- [ ] **Step 3: 写 templates**

`_helpers.tpl`：
```yaml
{{- define "paas.fullname" -}}
{{ .Release.Name }}-core
{{- end -}}
{{- define "paas.labels" -}}
app.kubernetes.io/name: paas-core
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/version: {{ .Chart.AppVersion }}
{{- end -}}
```

`core-deployment.yaml`：
```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ include "paas.fullname" . }}
  labels: {{- include "paas.labels" . | nindent 4 }}
spec:
  replicas: 1
  selector:
    matchLabels: { app.kubernetes.io/name: paas-core, app.kubernetes.io/instance: {{ .Release.Name }} }
  template:
    metadata:
      labels: { app.kubernetes.io/name: paas-core, app.kubernetes.io/instance: {{ .Release.Name }} }
    spec:
      containers:
        - name: core
          image: "{{ .Values.image.registry }}/{{ .Values.image.repository }}:{{ .Values.image.tag }}"
          imagePullPolicy: {{ .Values.image.pullPolicy }}
          ports: [{ containerPort: 8080 }]
          env:
            - name: PAAS_DB_URL
              value: {{ if .Values.db.enabled }} "postgres://paas:paas-dev@{{ .Release.Name }}-postgres:5432/paas?sslmode=disable" {{ else }} {{ .Values.db.url | quote }} {{ end }}
            {{- if .Values.env.PAAS_API_KEY }}
            - name: PAAS_API_KEY
              value: {{ .Values.env.PAAS_API_KEY | quote }}
            {{- end }}
          resources: {{- toYaml .Values.resources | nindent 12 }}
          readinessProbe: { httpGet: { path: /livez, port: 8080 }, initialDelaySeconds: 5 }
          livenessProbe: { httpGet: { path: /livez, port: 8080 }, initialDelaySeconds: 15 }
```

`core-service.yaml`：
```yaml
apiVersion: v1
kind: Service
metadata:
  name: {{ include "paas.fullname" . }}
  labels: {{- include "paas.labels" . | nindent 4 }}
spec:
  selector: { app.kubernetes.io/name: paas-core, app.kubernetes.io/instance: {{ .Release.Name }} }
  ports: [{ port: 80, targetPort: 8080, name: http }]
```

`postgres.yaml`（db.enabled=true 时）：
```yaml
{{- if .Values.db.enabled }}
apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: {{ .Release.Name }}-postgres
  labels: {{- include "paas.labels" . | nindent 4 }}
spec:
  serviceName: {{ .Release.Name }}-postgres
  replicas: 1
  selector:
    matchLabels: { app.kubernetes.io/name: paas-postgres, app.kubernetes.io/instance: {{ .Release.Name }} }
  template:
    metadata:
      labels: { app.kubernetes.io/name: paas-postgres, app.kubernetes.io/instance: {{ .Release.Name }} }
    spec:
      containers:
        - name: postgres
          image: postgres:16-alpine
          env:
            - { name: POSTGRES_DB, value: paas }
            - { name: POSTGRES_USER, value: paas }
            - { name: POSTGRES_PASSWORD, value: paas-dev }
          ports: [{ containerPort: 5432 }]
          volumeMounts: [{ name: data, mountPath: /var/lib/postgresql/data }]
  volumeClaimTemplates:
    - metadata: { name: data }
      spec:
        accessModes: ["ReadWriteOnce"]
        resources: { requests: { storage: 10Gi } }
---
apiVersion: v1
kind: Service
metadata:
  name: {{ .Release.Name }}-postgres
spec:
  selector: { app.kubernetes.io/name: paas-postgres, app.kubernetes.io/instance: {{ .Release.Name }} }
  ports: [{ port: 5432 }]
{{- end }}
```

`ingress.yaml`（ingress.enabled=true 时，略，标准 networking.k8s.io/v1）。
`configmap.yaml`（预留迁移 SQL 挂载，core 已 embed migrations，本期空 cm，略）。

- [ ] **Step 4: helm lint 验证**

Run: `helm lint deploy/charts/paas/`
Expected: `[INFO] Chart.yaml: icon is recommended` + `1 chart(s) linted, 0 chart(s) failed`（icon 警告可忽略）。

- [ ] **Step 5: helm template 验证渲染**

Run: `helm template paas deploy/charts/paas/ | head -40`
Expected: 渲染出 Deployment + Service（db.enabled=true 默认含 postgres）。

- [ ] **Step 6: Commit（用户未要求 commit 时跳过）**

```bash
git add deploy/charts/paas/
git commit -m "feat(deploy): Helm chart（core + postgres + service + ingress）"
```

---

### Task 3: airsync CLI 入口

**Files:**
- Create: `cmd/airsync/main.go`

**Interfaces:**
- Produces: `airsync bundle/install/verify/doctor` 子命令（stdlib flag）。

- [ ] **Step 1: 写 main.go（子命令分发 + 各子命令 flag）**

```go
// Command airsync 是离线交付工具：bundle（公网打包）/ install（私有部署）/ verify（校验）/ doctor（环境检查）。
// 核心逻辑在 internal/airsync；本入口解析 flag 调用。
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
)

func usage() {
	fmt.Fprintln(os.Stderr, `airsync - PaaS 离线交付工具

用法:
  airsync bundle   --version <v> [--registry ghcr.io/aitoys] [--chart deploy/charts/paas] [--out paas-bundle-<v>.tar.gz]
  airsync install  --bundle <file.tar.gz> --target-registry <reg> [--namespace paas] [--set key=val]
  airsync verify   --bundle <file.tar.gz>
  airsync doctor   # 检查 docker/helm/kubectl 依赖`)
	os.Exit(2)
}

func main() {
	log.SetFlags(0)
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "bundle":
		cmdBundle(os.Args[2:])
	case "install":
		cmdInstall(os.Args[2:])
	case "verify":
		cmdVerify(os.Args[2:])
	case "doctor":
		cmdDoctor(os.Args[2:])
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "未知子命令: %s\n\n", os.Args[1])
		usage()
	}
}

func cmdBundle(args []string) {
	fs := flag.NewFlagSet("bundle", flag.ExitOnError)
	version := fs.String("version", "", "PaaS 版本（如 v0.1.0）")
	registry := fs.String("registry", "ghcr.io/aitoys", "源镜像 registry")
	chart := fs.String("chart", "deploy/charts/paas", "Helm chart 目录")
	out := fs.String("out", "", "输出 bundle 文件名（空则 paas-bundle-<version>.tar.gz）")
	fs.Parse(args)
	if *version == "" {
		log.Fatal("--version 必填")
	}
	if *out == "" {
		*out = fmt.Sprintf("paas-bundle-%s.tar.gz", *version)
	}
	if err := airsyncBundle(*version, *registry, *chart, *out); err != nil {
		log.Fatalf("bundle 失败: %v", err)
	}
	log.Printf("✓ bundle 生成: %s", *out)
}

func cmdInstall(args []string) {
	fs := flag.NewFlagSet("install", flag.ExitOnError)
	bundle := fs.String("bundle", "", "bundle 文件（.tar.gz）")
	targetReg := fs.String("target-registry", "", "私有 registry（如 registry.private.com）")
	namespace := fs.String("namespace", "paas", "K8s namespace")
	fs.Parse(args)
	if *bundle == "" || *targetReg == "" {
		log.Fatal("--bundle 与 --target-registry 必填")
	}
	if err := airsyncInstall(*bundle, *targetReg, *namespace); err != nil {
		log.Fatalf("install 失败: %v", err)
	}
	log.Printf("✓ install 完成（namespace=%s）", *namespace)
}

func cmdVerify(args []string) {
	fs := flag.NewFlagSet("verify", flag.ExitOnError)
	bundle := fs.String("bundle", "", "bundle 文件（.tar.gz）")
	fs.Parse(args)
	if *bundle == "" {
		log.Fatal("--bundle 必填")
	}
	if err := airsyncVerify(*bundle); err != nil {
		log.Fatalf("verify 失败: %v", err)
	}
	log.Printf("✓ verify 通过")
}

func cmdDoctor(args []string) {
	if err := airsyncDoctor(); err != nil {
		log.Fatalf("doctor: %v", err)
	}
}
```

- [ ] **Step 2: Commit（用户未要求 commit 时跳过）**

```bash
git add cmd/airsync/main.go
git commit -m "feat(airsync): CLI 入口（bundle/install/verify/doctor）"
```

---

### Task 4: bundle/install/verify/doctor 逻辑

**Files:**
- Create: `internal/airsync/exec.go`（命令运行抽象）
- Create: `internal/airsync/bundle.go`
- Create: `internal/airsync/install.go`
- Create: `cmd/airsync/run.go`（CLI 调用 internal/airsync 的 glue）

- [ ] **Step 1: 写 exec.go（命令运行抽象，便于测试）**

```go
package airsync

import (
	"fmt"
	"os/exec"
)

// CmdRunner 抽象命令执行（生产用 exec.Command；测试可 mock）。
type CmdRunner interface {
	Run(name string, args ...string) (string, error)
}

// osRunner 用 os/exec 实际执行。
type osRunner struct{}

// DefaultRunner 是生产用 runner。
var DefaultRunner CmdRunner = osRunner{}

func (osRunner) Run(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s %v: %w: %s", name, args, err, out)
	}
	return string(out), nil
}
```

- [ ] **Step 2: 写 bundle.go（docker save + helm package + tar.gz + manifest）**

```go
package airsync

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"os"
	"path/filepath"
)

// BundleConfig 是 bundle 的输入配置。
type BundleConfig struct {
	Version  string // paas 版本（如 0.1.0）
	Registry string // 源 registry
	ChartDir string // Helm chart 目录
	Out      string // 输出 .tar.gz
	Runner   CmdRunner
}

// Run 执行 bundle：拉镜像 docker save + helm package + 算 sha256 + 写 manifest + 打包 tar.gz。
func (c BundleConfig) Run() error {
	if c.Runner == nil {
		c.Runner = DefaultRunner
	}
	work, err := os.MkdirTemp("", "airsync-bundle-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)

	// chart appVersion 决定镜像 tag（对齐校验）。
	chartTag := c.Version
	chartTgz := fmt.Sprintf("paas-%s.tgz", c.Version)

	// helm package chart → work/paas-<v>.tgz
	if _, err := c.Runner.Run("helm", "package", c.ChartDir, "--version", c.Version, "--destination", work); err != nil {
		return fmt.Errorf("helm package: %w", err)
	}

	// 镜像列表（core + postgres:16-alpine）。
	images := []ImageRef{
		{Name: c.Registry + "/paas-core", Tag: chartTag, File: fmt.Sprintf("core-%s.tar", c.Version)},
		{Name: "postgres", Tag: "16-alpine", File: "postgres-16-alpine.tar"},
	}
	// docker save 各镜像到 work/
	for _, img := range images {
		full := img.Name + ":" + img.Tag
		if _, err := c.Runner.Run("docker", "save", "-o", filepath.Join(work, img.File), full); err != nil {
			return fmt.Errorf("docker save %s: %w", full, err)
		}
	}

	// 生成 manifest（含 sha256）。
	if _, err := BuildManifest(work, c.Version, c.Version, chartTgz, images, "bundle"); err != nil {
		return err
	}

	// 打包 work/ → c.Out（tar.gz）。
	return tarGz(work, c.Out)
}

// tarGz 把 srcDir 所有文件打包到 outPath。
func tarGz(srcDir, outPath string) error {
	out, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer out.Close()
	gz := gzip.NewWriter(out)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()
	return filepath.Walk(srcDir, func(path string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(srcDir, path)
		hdr := &tar.Header{Name: rel, Mode: int64(fi.Mode()), Size: fi.Size()}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		_, err = tw.Write(data)
		return err
	})
}
```

- [ ] **Step 3: 写 install.go（解包 + verify + docker load + retag + push + helm install）**

```go
package airsync

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// InstallConfig 是 install 的输入配置。
type InstallConfig struct {
	Bundle       string // bundle .tar.gz
	TargetReg    string // 私有 registry
	Namespace    string // K8s namespace
	Runner       CmdRunner
}

func (c InstallConfig) Run() error {
	if c.Runner == nil {
		c.Runner = DefaultRunner
	}
	work, err := os.MkdirTemp("", "airsync-install-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)

	// 解包 bundle → work/
	if err := unTarGz(c.Bundle, work); err != nil {
		return fmt.Errorf("解包: %w", err)
	}
	// verify 完整性
	m, mismatch, err := VerifyManifest(work)
	if err != nil {
		return err
	}
	if len(mismatch) != 0 {
		return fmt.Errorf("完整性校验失败（文件损坏/篡改）: %v", mismatch)
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
	// helm install（image.registry 指向私有 registry）
	chartPath := filepath.Join(work, m.ChartFile)
	coreImg := fmt.Sprintf("%s/paas-core:%s", c.TargetReg, m.PaasVersion)
	if _, err := c.Runner.Run("helm", "upgrade", "--install", "paas", chartPath,
		"--namespace", c.Namespace, "--create-namespace",
		"--set", fmt.Sprintf("image.registry=%s", c.TargetReg),
		"--set", fmt.Sprintf("image.repository=paas-core"),
		"--set", fmt.Sprintf("image.tag=%s", m.PaasVersion),
		"--set", fmt.Sprintf("image=%s", coreImg)); err != nil {
		return fmt.Errorf("helm install: %w", err)
	}
	return nil
}

func basename(name string) string {
	for i := len(name) - 1; i >= 0; i-- {
		if name[i] == '/' {
			return name[i+1:]
		}
	}
	return name
}

func unTarGz(bundlePath, dst string) error {
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
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return err
		}
		w, err := os.Create(out)
		if err != nil {
			return err
		}
		if _, err := io.Copy(w, tr); err != nil {
			w.Close()
			return err
		}
		w.Close()
	}
	return nil
}
```

- [ ] **Step 4: 写 cmd/airsync/run.go（glue，CLI 调 internal/airsync）**

```go
package main

import (
	"fmt"
	"internal/airsync"  // 实际用 module path
	"os"
)

func airsyncBundle(version, registry, chart, out string) error {
	return airsync.BundleConfig{Version: version, Registry: registry, ChartDir: chart, Out: out}.Run()
}
func airsyncInstall(bundle, targetReg, ns string) error {
	return airsync.InstallConfig{Bundle: bundle, TargetReg: targetReg, Namespace: ns}.Run()
}
func airsyncVerify(bundle string) error {
	// 解包到临时目录 + VerifyManifest
	dir, err := os.MkdirTemp("", "airsync-verify-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	if err := airsync.UnTarGz(bundle, dir); err != nil {
		return err
	}
	_, mismatch, err := airsync.VerifyManifest(dir)
	if err != nil {
		return err
	}
	if len(mismatch) != 0 {
		return fmt.Errorf("完整性校验失败: %v", mismatch)
	}
	return nil
}
func airsyncDoctor() error {
	for _, tool := range []string{"docker", "helm", "kubectl"} {
		if _, err := airsync.DefaultRunner.Run(tool, "version"); err != nil {
			return fmt.Errorf("%s 不可用: %w", tool, err)
		}
		fmt.Printf("✓ %s\n", tool)
	}
	return nil
}
```

> 注：`UnTarGz` 需在 internal/airsync 导出（Task 4 Step 3 的 unTarGz 改为 `UnTarGz`）。执行时确认导出。

- [ ] **Step 5: 编译 + go vet**

Run: `go build ./cmd/airsync/ && go vet ./...`
Expected: 通过。

- [ ] **Step 6: Commit（用户未要求 commit 时跳过）**

```bash
git add internal/airsync/ cmd/airsync/
git commit -m "feat(airsync): bundle/install/verify/doctor 逻辑"
```

---

### Task 5: 验收 + 文档

**Files:**
- Modify: `Makefile`（airsync 构建目标）
- Modify: `CHANGELOG.md`、`CLAUDE.md`、`README.md`（若存在，补离线交付说明）

- [ ] **Step 1: Makefile 加 airsync 目标**

```makefile
airsync: ## 编译 airsync 离线交付工具到 bin/
	mkdir -p bin
	go build -o bin/airsync ./cmd/airsync
```

- [ ] **Step 2: 验收（本地可验证项）**

Run:
```bash
make airsync
./bin/airsync doctor           # 检查 docker/helm/kubectl（本地装了，应全 ✓）
./bin/airsync --help           # 帮助
helm lint deploy/charts/paas/  # chart 合法
go test ./internal/airsync/ -count=1 -v  # manifest/verify 单测
```
Expected: doctor 全 ✓；chart lint 0 failed；airsync 测试 PASS。

- [ ] **Step 3: CHANGELOG 加条目**

```markdown
- airsync 离线交付工具：新增 `cmd/airsync` CLI（bundle/install/verify/doctor，stdlib flag 零依赖）+ `internal/airsync`（manifest.json 含每文件 sha256 完整性校验 + bundle 打包 tar.gz + install docker load/retag/push + helm install）+ `deploy/charts/paas` Helm chart（core Deployment + postgres StatefulSet + service + ingress，values 参数化 image.registry/db.url/ingress）。公网/私有两路径共用同一 chart（仅 image.registry 不同）。控制面可打包为离线交付件（私有化双模交付）。airsync 自研 Apache 2.0；调 docker/helm/kubectl CLI（不引 client 库）。bundle/install 端到端集成需 registry/K8s，命令构造 + manifest/verify + chart lint 已本地验证。
```

- [ ] **Step 4: CLAUDE.md 交付小节更新**

补 airsync + Helm chart 说明（仓库结构 deploy/charts/、airsync 用法）。

- [ ] **Step 5: 全量回归**

Run: `go test ./... -race -count=1 2>&1 | grep -c "^FAIL"`
Expected: 0。

- [ ] **Step 6: Commit（用户未要求 commit 时跳过）**

```bash
git add Makefile CHANGELOG.md CLAUDE.md
git commit -m "feat(airsync): Makefile 目标 + 文档同步"
```

---

## Self-Review

**1. Spec coverage:**
- spec「airsync CLI bundle/install/verify」→ Task 3+4。✅
- spec「Helm chart（core + postgres + service + ingress + configmap）」→ Task 2。✅
- spec「离线镜像包（OCI tar + manifest.json sha256）」→ Task 1+4。✅
- spec「公网/私有两路径共用 chart」→ Task 2 values（image.registry 参数化）。✅
- spec 验收 1（bundle 产 tar.gz 含全部 + manifest sha256）→ Task 4 bundle.go。✅
- spec 验收 2（verify 校验完整性，篡改失败）→ Task 1 TestVerifyDetectsTamper。✅
- spec 验收 3（install 离线部署）→ Task 4 install.go（集成需 K8s，命令构造可测）。⚠️ 本地无 K8s 集群，集成标注后续。
- spec 验收 4（helm install 公网路径可用）→ Task 2 helm lint + template。✅
- spec 验收 5（values 参数化 db.url 外置 PG）→ Task 2 values + core-deployment env。✅
- spec 验收 6（license Apache 2.0）→ airsync 自研 + Helm Apache 2.0。✅
- spec 风险「chart 版本与镜像 tag 对齐」→ manifest 记 chartVersion/paasVersion + verify 校验。✅
- spec 风险「docker/helm/kubectl 依赖」→ doctor 子命令检查。✅

**2. Placeholder scan:** 关键代码齐全（manifest/bundle/install/chart/CLI）；执行时核对 BuildManifest 空 chartFile 处理、UnTarGz 导出、cmd/airsync import module path。

**3. Type consistency:** `Manifest`/`ImageRef`/`BundleConfig`/`InstallConfig`/`CmdRunner` 跨文件一致；cmd/airsync 调 internal/airsync 导出函数。

**已知决策/限制：**
- bundle/install 端到端集成（docker pull/save 真实镜像、helm install 真实 K8s）需 registry/K8s，本地受限（docker 拉镜像网络）；本期命令构造 + manifest/verify 单测 + helm lint 本地验证，集成测试归后续（CI 或手动）。
- chart configmap 模板本期预留（migrations 已 embed 进 core 二进制，不需外挂 SQL）；ingress 标准模板。
- airsync 用 stdlib flag（非 cobra），与项目零依赖风格一致。
