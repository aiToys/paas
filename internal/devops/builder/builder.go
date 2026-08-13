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
	"sort"
	"strings"
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
	tag := buildTag(p)
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

// buildTag 生成镜像 tag：<branch>-<commit8>[-<argsHash8>]。
//
// 多服务同 repo 构建场景（如 paas-shop 用 buildArgs SERVICE=product/recommend/... 区分）：
// 同 app 同 commit 的多次构建若 tag 仅 branch-commit8 会完全相同，后构建在 registry 覆盖
// 先构建，但各 BuildRun 记的 digest 是各自构建时的值 → registry 实际内容（按 tag 拉取）与
// PG images 表 digest 不一致，部署拉到「最后一个构建」而非期望服务。
//
// 解法：有 buildArgs 时末尾追加 buildArgs 稳定哈希（按 key 排序拼接，取 sha256 前 8 位）。
//   - 同 buildArgs 重构 → 同 argsHash → 同 tag（幂等，不产生垃圾 tag）
//   - 不同 buildArgs → 不同 argsHash → 不同 tag（多服务区分，各自独立 digest）
//   - 无 buildArgs → 保持原 branch-commit8（向后兼容，单服务场景不变）
//
// fallbackCommit 为空 Commit 时的兜底（各实现用 BuildID 派生）。
func buildTag(p Params) string {
	tagCommit := p.Commit
	if tagCommit == "" {
		tagCommit = p.BuildID
	}
	base := p.Branch + "-" + safeShort(tagCommit, 8)
	if len(p.BuildArgs) == 0 {
		return base
	}
	return base + "-" + argsHash(p.BuildArgs)
}

// argsHash 按 key 排序拼接 buildArgs 后取 sha256 前 8 位（确定性，避免 map 迭代序漂移）。
func argsHash(args map[string]string) string {
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(args[k])
		b.WriteByte('|')
	}
	return safeShort(sha256hex(b.String()), 8)
}
