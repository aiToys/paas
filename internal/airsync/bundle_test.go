package airsync

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fakeRunner 记录调用并按需伪造命令副作用（bundle 会 stat 命令产物，纯记录不够）。
type fakeRunner struct {
	calls [][]string
	onRun func(name string, args []string) (string, error)
}

func (f *fakeRunner) Run(name string, args ...string) (string, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	if f.onRun != nil {
		return f.onRun(name, args)
	}
	return "", nil
}

func (f *fakeRunner) find(name string, argsMatch ...string) []string {
	for _, c := range f.calls {
		if c[0] != name {
			continue
		}
		if slices.Contains(c, argsMatch[0]) {
			return c
		}
	}
	return nil
}

// argValue 取 --flag value / -flag value 形式的值。
func argValue(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// obsFakeRunner 模拟三个 chart 的 helm pull / helm package / docker save 产物落盘，
// 并回答脚本的 --chart-versions 查询（与真实脚本输出格式一致）。
func obsFakeRunner() *fakeRunner {
	return &fakeRunner{onRun: func(name string, args []string) (string, error) {
		switch {
		case name == "bash" && slices.Contains(args, "--chart-versions"):
			return "prometheus-community/prometheus 29.24.0 https://prometheus-community.github.io/helm-charts\n" +
				"grafana/loki-stack 2.10.3 https://grafana.github.io/helm-charts\n" +
				"grafana/grafana 10.5.15 https://grafana.github.io/helm-charts\n", nil
		case name == "helm" && len(args) > 1 && args[0] == "package":
			dest, v := argValue(args, "--destination"), argValue(args, "--version")
			return "", os.WriteFile(filepath.Join(dest, "paas-"+v+".tgz"), []byte("chart"), 0o600)
		case name == "helm" && len(args) > 1 && args[0] == "pull":
			dest, v, ref := argValue(args, "--destination"), argValue(args, "--version"), args[1]
			return "", os.WriteFile(filepath.Join(dest, basename(ref)+"-"+v+".tgz"), []byte("chart "+ref), 0o600)
		case name == "docker" && len(args) > 1 && args[0] == "save":
			return "", os.WriteFile(argValue(args, "-o"), []byte("img"), 0o600)
		}
		return "", nil
	}}
}

// newObsFixture 建一个含 2 个清单文件 + 脚本的假仓库目录。
func newObsFixture(t *testing.T) (obsDir, script string) {
	t.Helper()
	root := t.TempDir()
	obsDir = filepath.Join(root, "observability")
	if err := os.MkdirAll(obsDir, 0o750); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"prometheus-values.yaml", "jaeger.yaml"} {
		if err := os.WriteFile(filepath.Join(obsDir, f), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// 非 yaml 文件不应进包（目录内可能混入 README 等）。
	if err := os.WriteFile(filepath.Join(obsDir, "README.md"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// 0600 而非 0755：gosec G306/G302 拒绝测试里写宽权限，而 0600 已足以验证权限位透传
	// （解包若走 os.Create 会退化成 0644，断言即失败）。
	script = filepath.Join(root, "deploy-observability.sh")
	if err := os.WriteFile(script, []byte("#!/bin/bash\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return obsDir, script
}

// bundle 带观测栈：helm pull 三个 chart + 拷清单与脚本 + manifest 条目 + sha256 覆盖。
func TestBundleWithObservability(t *testing.T) {
	obsDir, script := newObsFixture(t)
	runner := obsFakeRunner()
	out := filepath.Join(t.TempDir(), "b.tgz")
	if err := (BundleConfig{
		Version: "0.1.0", Registry: "ghcr.io/aitoys", ChartDir: "deploy/charts/paas", Out: out,
		Runner: runner, WithObservability: true, ObservabilityDir: obsDir, ScriptPath: script,
	}).Run(); err != nil {
		t.Fatalf("bundle: %v", err)
	}

	// 三个 chart 各 pull 一次，且参数带正确版本与目标目录。
	var pulled []string
	for _, c := range runner.calls {
		if c[0] == "helm" && len(c) > 1 && c[1] == "pull" {
			pulled = append(pulled, c[2]+"@"+argValue(c, "--version"))
		}
	}
	want := []string{"prometheus-community/prometheus@29.24.0", "grafana/loki-stack@2.10.3", "grafana/grafana@10.5.15"}
	if !slices.Equal(pulled, want) {
		t.Fatalf("helm pull 应为 %v，实际 %v", want, pulled)
	}
	// 两个仓库各 add 一次（loki-stack/grafana 同仓库，不应重复 add）。
	var added []string
	for _, c := range runner.calls {
		if c[0] == "helm" && len(c) > 1 && c[1] == "repo" {
			added = append(added, c[3])
		}
	}
	if !slices.Equal(added, []string{"prometheus-community", "grafana"}) {
		t.Fatalf("helm repo add 应为 prometheus-community/grafana 各一次，实际 %v", added)
	}

	// 解包校验：chart + 清单 + 脚本都在包内，且 sha256 全覆盖。
	dir := t.TempDir()
	if err := UnTarGz(out, dir); err != nil {
		t.Fatalf("解包: %v", err)
	}
	for _, f := range []string{
		"charts/prometheus-29.24.0.tgz", "charts/loki-stack-2.10.3.tgz", "charts/grafana-10.5.15.tgz",
		"observability/prometheus-values.yaml", "observability/jaeger.yaml",
		"scripts/deploy-observability.sh", "paas-0.1.0.tgz", "manifest.json",
	} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("包内应含 %s: %v", f, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "observability/README.md")); err == nil {
		t.Error("非 yaml 文件不应进包")
	}
	// 脚本权限位透传（打包保留 + 解包还原；解包若走 os.Create 默认 0644，此断言即失败）。
	if fi, err := os.Stat(filepath.Join(dir, bundleScriptPath)); err != nil {
		t.Fatalf("脚本应进包: %v", err)
	} else if fi.Mode().Perm() != 0o600 {
		t.Errorf("脚本权限位应透传 0600，实际 %v", fi.Mode().Perm())
	}

	m, mismatch, err := VerifyManifest(dir)
	if err != nil || len(mismatch) != 0 {
		t.Fatalf("verify 应通过，mismatch=%v err=%v", mismatch, err)
	}
	if m.Observability == nil {
		t.Fatal("manifest 应含观测栈条目")
	}
	if len(m.Observability.Charts) != 3 || m.Observability.Script != bundleScriptPath {
		t.Fatalf("观测栈条目不符: %+v", m.Observability)
	}
	// sha256 必须覆盖观测栈的 chart/清单/脚本，否则篡改检不出。
	for _, f := range append([]string{m.Observability.Script}, m.Observability.Files...) {
		if _, ok := m.Files[f]; !ok {
			t.Errorf("sha256 未覆盖 %s", f)
		}
	}
	for _, c := range m.Observability.Charts {
		if _, ok := m.Files[c.File]; !ok {
			t.Errorf("sha256 未覆盖 chart %s", c.File)
		}
	}
}

// 默认（不加开关）bundle 不含观测栈：既有交付件形态不变。
func TestBundleWithoutObservabilityHasNoObsEntries(t *testing.T) {
	runner := obsFakeRunner()
	out := filepath.Join(t.TempDir(), "b.tgz")
	if err := (BundleConfig{
		Version: "0.1.0", Registry: "ghcr.io/aitoys", ChartDir: "deploy/charts/paas", Out: out,
		Runner: runner, // WithObservability 缺省 false
	}).Run(); err != nil {
		t.Fatalf("bundle: %v", err)
	}
	if runner.find("helm", "pull") != nil {
		t.Error("未开 --with-observability 不应 helm pull")
	}
	dir := t.TempDir()
	if err := UnTarGz(out, dir); err != nil {
		t.Fatal(err)
	}
	m, _, err := VerifyManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if m.Observability != nil {
		t.Fatalf("不应含观测栈条目，实际 %+v", m.Observability)
	}
}

// install 侧：包内含观测栈时调包内脚本，chart/清单目录用解包后绝对路径，MIRROR 默认 none。
func TestInstallRunsBundledObservabilityScript(t *testing.T) {
	obsDir, script := newObsFixture(t)
	bundleRunner := obsFakeRunner()
	out := filepath.Join(t.TempDir(), "b.tgz")
	if err := (BundleConfig{
		Version: "0.1.0", Registry: "ghcr.io/aitoys", ChartDir: "deploy/charts/paas", Out: out,
		Runner: bundleRunner, WithObservability: true, ObservabilityDir: obsDir, ScriptPath: script,
	}).Run(); err != nil {
		t.Fatalf("bundle: %v", err)
	}

	t.Setenv("MIRROR", "") // 未显式指定 → install 应置 none
	installRunner := obsFakeRunner()
	if err := (InstallConfig{
		Bundle: out, TargetReg: "registry.private.com", Namespace: "paas", Runner: installRunner,
	}).Run(); err != nil {
		t.Fatalf("install: %v", err)
	}
	call := installRunner.find("env", "bash")
	if call == nil {
		t.Fatal("应调用 env bash <脚本> 部署观测栈")
	}
	joined := strings.Join(call, " ")
	for _, want := range []string{"OBS_CHART_DIR=", "/charts", "OBS_MANIFEST_DIR=", "/observability", "MIRROR=none",
		"/scripts/deploy-observability.sh", "--yes"} {
		if !strings.Contains(joined, want) {
			t.Errorf("调用应含 %q，实际 %s", want, joined)
		}
	}
	// 控制面先于观测栈（观测栈的 paas-core 抓取 job 依赖控制面就绪）。
	var helmIdx, obsIdx int
	for i, c := range installRunner.calls {
		if c[0] == "helm" && len(c) > 1 && c[1] == "upgrade" {
			helmIdx = i
		}
		if c[0] == "env" {
			obsIdx = i
		}
	}
	if obsIdx < helmIdx {
		t.Error("观测栈部署应在控制面 helm upgrade 之后")
	}
}

// 调用方显式导出 MIRROR 时 install 不覆盖（内网有中转站/镜像源时可继续用）。
func TestInstallRespectsExplicitMirror(t *testing.T) {
	obsDir, script := newObsFixture(t)
	out := filepath.Join(t.TempDir(), "b.tgz")
	runner := obsFakeRunner()
	if err := (BundleConfig{
		Version: "0.1.0", Registry: "ghcr.io/aitoys", ChartDir: "deploy/charts/paas", Out: out,
		Runner: runner, WithObservability: true, ObservabilityDir: obsDir, ScriptPath: script,
	}).Run(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MIRROR", "daocloud")
	ir := obsFakeRunner()
	if err := (InstallConfig{Bundle: out, TargetReg: "r.io", Namespace: "paas", Runner: ir}).Run(); err != nil {
		t.Fatal(err)
	}
	if call := ir.find("env", "bash"); slices.Contains(call, "MIRROR=none") {
		t.Errorf("调用方已导出 MIRROR 时不应被覆盖: %v", call)
	}
}

// 无观测栈的包 install 不应调脚本（避免误跑不存在的东西）。
func TestInstallSkipsObservabilityWhenAbsent(t *testing.T) {
	runner := obsFakeRunner()
	out := filepath.Join(t.TempDir(), "b.tgz")
	if err := (BundleConfig{
		Version: "0.1.0", Registry: "ghcr.io/aitoys", ChartDir: "deploy/charts/paas", Out: out, Runner: runner,
	}).Run(); err != nil {
		t.Fatal(err)
	}
	ir := obsFakeRunner()
	if err := (InstallConfig{Bundle: out, TargetReg: "r.io", Namespace: "paas", Runner: ir}).Run(); err != nil {
		t.Fatal(err)
	}
	if ir.find("env", "bash") != nil {
		t.Error("包内无观测栈时不应调用部署脚本")
	}
}

// 清单目录为空/无 yaml：拒绝打半包（宁可不打，不交付缺清单的包）。
func TestBundleObservabilityRejectsEmptyManifestDir(t *testing.T) {
	empty := t.TempDir()
	_, script := newObsFixture(t)
	err := (BundleConfig{
		Version: "0.1.0", Registry: "r", ChartDir: "c", Out: filepath.Join(t.TempDir(), "b.tgz"),
		Runner: obsFakeRunner(), WithObservability: true, ObservabilityDir: empty, ScriptPath: script,
	}).Run()
	if err == nil || !strings.Contains(err.Error(), "无 *.yaml") {
		t.Fatalf("清单目录无 yaml 应报错，实际 %v", err)
	}
}

// chart 版本行格式非法（脚本被改坏）：fail fast，不静默打出空 chart 的包。
func TestResolveObsChartsRejectsBadOutput(t *testing.T) {
	runner := &fakeRunner{onRun: func(name string, args []string) (string, error) {
		return "prometheus-community/prometheus 29.24.0\n", nil // 缺仓库 URL
	}}
	c := BundleConfig{ScriptPath: "s.sh", Runner: runner}
	if _, err := c.resolveObsCharts(); err == nil {
		t.Fatal("格式非法应报错")
	}
	runner.onRun = func(name string, args []string) (string, error) { return "\n", nil }
	if _, err := c.resolveObsCharts(); err == nil {
		t.Fatal("空输出应报错")
	}
}
