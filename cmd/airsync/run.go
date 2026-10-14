package main

import (
	"fmt"
	"os"

	"github.com/aitoys/paas/internal/airsync"
)

func airsyncBundle(version, registry, chart, out string, withObs bool, obsDir, obsScript string) error {
	return airsync.BundleConfig{
		Version: version, Registry: registry, ChartDir: chart, Out: out,
		WithObservability: withObs, ObservabilityDir: obsDir, ScriptPath: obsScript,
	}.Run()
}

func airsyncInstall(bundle, targetReg, ns string) error {
	return airsync.InstallConfig{Bundle: bundle, TargetReg: targetReg, Namespace: ns}.Run()
}

func airsyncVerify(bundle string) error {
	dir, err := os.MkdirTemp("", "airsync-verify-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
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
	// 各工具跑 client-only 子命令验证二进制可用（不连外部 daemon/cluster）。
	// envsubst/jq 是包内可观测栈部署脚本的前置（脚本自己会报缺失，但 doctor 提前暴露更省事：
	// 离线目标机补装工具比「装到一半失败」容易得多）。二者设为软校验：只有走
	// --with-observability 的观测栈部署才需要——doctor 不知道调用方后续是否装观测栈，
	// 硬拦会把不用观测栈的 bundle/install 用户误判为环境不合格。
	checks := []struct {
		tool string
		args []string
		hint string
		soft bool // true=缺失仅告警（观测栈专用工具）
	}{
		{"docker", []string{"version"}, "", false},
		{"helm", []string{"version"}, "", false},
		{"kubectl", []string{"version", "--client"}, "", false},
		{"envsubst", []string{"--version"}, "macOS: brew install gettext && brew link --force gettext；Debian: apt install gettext-base", true},
		{"jq", []string{"--version"}, "", true},
	}
	for _, c := range checks {
		if _, err := airsync.DefaultRunner.Run(c.tool, c.args...); err != nil {
			if c.soft {
				fmt.Printf("⚠ %s 不可用（仅观测栈部署需要，不用 --with-observability 可忽略）: %v\n", c.tool, err)
				if c.hint != "" {
					fmt.Printf("  提示: %s\n", c.hint)
				}
				continue
			}
			return fmt.Errorf("%s 不可用: %w", c.tool, err)
		}
		fmt.Printf("✓ %s\n", c.tool)
	}
	fmt.Println("airsync 依赖就绪")
	return nil
}
