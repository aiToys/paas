package main

import (
	"fmt"
	"os"

	"github.com/aitoys/paas/internal/airsync"
)

func airsyncBundle(version, registry, chart, out string) error {
	return airsync.BundleConfig{Version: version, Registry: registry, ChartDir: chart, Out: out}.Run()
}

func airsyncInstall(bundle, targetReg, ns string) error {
	return airsync.InstallConfig{Bundle: bundle, TargetReg: targetReg, Namespace: ns}.Run()
}

func airsyncVerify(bundle string) error {
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
	// 各工具跑 client-only 子命令验证二进制可用（不连外部 daemon/cluster）。
	checks := []struct {
		tool string
		args []string
	}{
		{"docker", []string{"version"}},
		{"helm", []string{"version"}},
		{"kubectl", []string{"version", "--client"}},
	}
	for _, c := range checks {
		if _, err := airsync.DefaultRunner.Run(c.tool, c.args...); err != nil {
			return fmt.Errorf("%s 不可用: %w", c.tool, err)
		}
		fmt.Printf("✓ %s\n", c.tool)
	}
	fmt.Println("airsync 依赖就绪")
	return nil
}
