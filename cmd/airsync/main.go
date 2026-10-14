// Command airsync 是 PaaS 离线交付工具：
//
//	bundle（公网打包镜像 + chart + manifest 为 tar.gz）
//	install（私有部署：解包 → verify → docker load/push → helm install）
//	verify（完整性校验，防传输损坏/篡改）
//	doctor（检查 docker/helm/kubectl 依赖）
//
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
                   [--with-observability]   # 一并打包可观测栈（3 个上游 chart + 清单 + 部署脚本）
  airsync install  --bundle <file.tar.gz> --target-registry <reg> [--namespace paas]
                   # 包内含观测栈时自动接着部署（无需额外开关）
  airsync verify   --bundle <file.tar.gz>
  airsync doctor   # 检查 docker/helm/kubectl；envsubst/jq 缺失仅告警（只有观测栈部署才需要）

公网路径（在线）: helm install paas oci://ghcr.io/aitoys/charts/paas（或源码 helm install paas deploy/charts/paas）
离线路径: airsync bundle 打包 → 物理介质传到客户 → airsync install 部署

观测栈（--with-observability）在 install 侧读这些环境变量（缺省值见 scripts/deploy-observability.sh）：
  NS_OBS / DOMAIN_SUFFIX / STORAGE_CLASS / INGRESS_CLASS / GRAFANA_ADMIN_PASSWORD / 各镜像 registry。
  MIRROR 未显式导出时 install 置 none（离线环境无国内中转站）。
  ⚠ 包内只含 chart 与清单，**不含观测栈镜像**：目标集群需能拉到 prometheus/config-reloader/
    node-exporter/kube-state-metrics/pushgateway/grafana/loki/promtail/jaeger 等镜像
    （内网 registry 预推，或镜像清单见 docs/deploy/dependencies.md）。`)
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
	version := fs.String("version", "", "PaaS 版本（如 0.1.0）")
	registry := fs.String("registry", "ghcr.io/aitoys", "源镜像 registry")
	chart := fs.String("chart", "deploy/charts/paas", "Helm chart 目录")
	out := fs.String("out", "", "输出 bundle 文件名（空则 paas-bundle-<version>.tar.gz）")
	withObs := fs.Bool("with-observability", false, "一并打包可观测栈（chart + 清单 + 部署脚本）")
	obsDir := fs.String("observability-dir", "deploy/observability", "可观测栈清单目录")
	obsScript := fs.String("observability-script", "scripts/deploy-observability.sh", "可观测栈部署脚本")
	fs.Parse(args) //nolint:errcheck // flag.ExitOnError 模式：解析错误已触发 os.Exit，无返回值可检查
	if *version == "" {
		log.Fatal("--version 必填")
	}
	if *out == "" {
		*out = fmt.Sprintf("paas-bundle-%s.tar.gz", *version)
	}
	if err := airsyncBundle(*version, *registry, *chart, *out, *withObs, *obsDir, *obsScript); err != nil {
		log.Fatalf("bundle 失败: %v", err)
	}
	log.Printf("✓ bundle 生成: %s", *out)
}

func cmdInstall(args []string) {
	fs := flag.NewFlagSet("install", flag.ExitOnError)
	bundle := fs.String("bundle", "", "bundle 文件（.tar.gz）")
	targetReg := fs.String("target-registry", "", "私有 registry（如 registry.private.com）")
	namespace := fs.String("namespace", "paas", "K8s namespace")
	fs.Parse(args) //nolint:errcheck // flag.ExitOnError 模式：解析错误已触发 os.Exit，无返回值可检查
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
	fs.Parse(args) //nolint:errcheck // flag.ExitOnError 模式：解析错误已触发 os.Exit，无返回值可检查
	if *bundle == "" {
		log.Fatal("--bundle 必填")
	}
	if err := airsyncVerify(*bundle); err != nil {
		log.Fatalf("verify 失败: %v", err)
	}
	log.Printf("✓ verify 通过：完整性校验无误")
}

func cmdDoctor(args []string) {
	if err := airsyncDoctor(); err != nil {
		log.Fatalf("doctor: %v", err)
	}
}
