// Package builder 抽象 DevOps 构建流水线（代码 → 不可变镜像 digest）。
//
// 两个实现：
//   - Mock：sleep + sha256(commit+app+build) 派生 digest（零依赖，与历史行为一致）
//   - Real：git clone → docker build → docker push，产出真实 registry digest
//
// Store（memory/pg）的 runBuild 调 Pipeline.Build 拿 digest/tag/log 后持久化，
// 状态机（pending→running→success/failed）仍由 Store 管——Pipeline 只做"计算"。
// cmd/core 按 PAAS_DEVOPS_REAL=true 注入 Real，空则 Mock（现状不变）。
package builder

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"
)

// Params 是构建流水线输入。
type Params struct {
	TenantID     string
	AppID        string
	BuildID      string
	Commit       string // 已知则直接用；空则 real 路径 clone 后取 HEAD
	Branch       string
	GitURL       string // real 路径 clone 源
	Dockerfile   string // 空则 Dockerfile（仓库根）
	BuildContext string // 空则 .
	Registry     string // 镜像仓库地址（real 推送目标，空则 registry.paas.local）
	GitToken     string // 可选：私有仓库 HTTPS token（注入 git URL）
	RegistryUser string // 可选：docker login 用户名
	RegistryPass string // 可选：docker login 密码
	BuildArgs    map[string]string // 可选：docker build --build-arg K=V（如 SERVICE=product）
}

// Result 是构建产物。Digest 是不可变真源（生产部署锁这个）。
type Result struct {
	Digest string // sha256:...
	Tag    string
	Log    string
	// Registry 回传实际 push 的镜像仓库地址（Build 内兜底后填，store 写 Image.Registry 用）。
	// 关键：Build(ctx, p Params) 是值传递，Build 内修改 p.Registry 不影响 store 的 p；
	// 故必须经 Result 回传，否则 store 用 RegistryOrDefault(p) 拿默认 registry.paas.local，
	// 与 K8sJob/Real 实际 push 的 k.Registry 不一致 -> reconciler 拼错镜像地址致 Pod 拉不到。
	Registry string
}

// Pipeline 执行构建流水线，产出不可变镜像 digest。
type Pipeline interface {
	Build(ctx context.Context, p Params) (Result, error)
}

// Mock 是零依赖 mock 流水线：确定性派生 digest（移植自历史 runBuild）。
type Mock struct{}

// Build 按 commit+app+build 派生 sha256 digest，模拟步进耗时。
func (Mock) Build(_ context.Context, p Params) (Result, error) {
	time.Sleep(800 * time.Millisecond) // 模拟 clone+build+push 耗时
	digest := "sha256:" + sha256hex(p.Commit+p.AppID+p.BuildID)
	tag := p.Branch + "-" + safeShort(p.Commit, 8)
	return Result{Digest: digest, Tag: tag, Log: mockBuildLog(tag), Registry: RegistryOrDefault(p)}, nil
}

// IsReal 标记是否真实执行（Store 据此决定是否预留长耗时）。
type IsReal interface{ Real() bool }

func sha256hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func safeShort(s string, n int) string {
	if len(s) < n {
		return s
	}
	return s[:n] // 取前 n 位（commit 短哈希，与历史 mock 行为一致）
}

func mockBuildLog(tag string) string {
	return fmt.Sprintf(`[mock] clone repository
[mock] resolve commit
[mock] docker build -t %s .
[mock] docker push
[mock] digest resolved
`, tag)
}

// RegistryOrDefault 返回 Params.Registry 或默认本地 registry。
func RegistryOrDefault(p Params) string {
	if p.Registry != "" {
		return p.Registry
	}
	return "registry.paas.local"
}

// ImageRef 拼接 registry/app:tag 全量引用。
func ImageRef(p Params, tag string) string {
	return RegistryOrDefault(p) + "/" + p.AppID + ":" + tag
}
