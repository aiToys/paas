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
  airsync install  --bundle <file.tar.gz> --target-registry <reg> [--namespace paas]
  airsync verify   --bundle <file.tar.gz>
  airsync doctor   # 检查 docker/helm/kubectl 依赖

公网路径（在线）: helm install paas oci://ghcr.io/aitoys/charts/paas（或源码 helm install paas deploy/charts/paas）
离线路径: airsync bundle 打包 → 物理介质传到客户 → airsync install 部署`)
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
	fs.Parse(args) //nolint:errcheck // flag.ExitOnError 模式：解析错误已触发 os.Exit，无返回值可检查
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
