package builder

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestBuildJobSpec(t *testing.T) {
	p := Params{
		TenantID:     "t-acme",
		AppID:        "app-cs",
		BuildID:      "build-abc",
		Commit:       "c0ffee1234567890",
		Branch:       "main",
		GitURL:       "https://example.com/repo.git",
		Registry:     "registry.example.local:5000",
		Dockerfile:   "Dockerfile",
		BuildContext: ".",
		RegistryUser: "u",
		RegistryPass: "p",
		BuildArgs:    map[string]string{"SERVICE": "product"},
	}
	job := buildJobSpec("docker:git", p, "paas-build-build-abc", "registry.example.local:5000/app-cs:main-c0ffee12", "https://x@git/repo.git")

	if job.Name != "paas-build-build-abc" {
		t.Errorf("Job.Name = %q, want paas-build-build-abc", job.Name)
	}
	// 租户标签便于审计/清理。
	if v := job.Labels["paas.devops/tenant"]; v != "t-acme" {
		t.Errorf("tenant label = %q, want t-acme", v)
	}
	if v := job.Labels["paas.devops/build-id"]; v != "build-abc" {
		t.Errorf("build-id label = %q, want build-abc", v)
	}
	// TTL + BackoffLimit。
	if job.Spec.TTLSecondsAfterFinished == nil || *job.Spec.TTLSecondsAfterFinished != 86400 {
		t.Errorf("TTL = %v, want 86400", job.Spec.TTLSecondsAfterFinished)
	}
	if job.Spec.BackoffLimit == nil || *job.Spec.BackoffLimit != 0 {
		t.Errorf("BackoffLimit = %v, want 0", job.Spec.BackoffLimit)
	}

	c := job.Spec.Template.Spec.Containers[0]
	if c.Image != "docker:git" {
		t.Errorf("image = %q, want docker:git", c.Image)
	}
	if len(c.Command) != 2 || c.Command[0] != "/bin/sh" || c.Command[1] != "-c" {
		t.Errorf("command = %v, want [/bin/sh -c]", c.Command)
	}
	if len(c.Args) != 1 || !strings.Contains(c.Args[0], "git clone") {
		t.Errorf("args = %v, want script containing git clone", c.Args)
	}
	// 关键 env 透传。
	envOf := func(k string) string {
		for _, e := range c.Env {
			if e.Name == k {
				return e.Value
			}
		}
		return "<missing>"
	}
	if envOf("REF") != "registry.example.local:5000/app-cs:main-c0ffee12" {
		t.Errorf("REF env = %q", envOf("REF"))
	}
	if envOf("CLONE_URL") != "https://x@git/repo.git" {
		t.Errorf("CLONE_URL env = %q", envOf("CLONE_URL"))
	}
	if envOf("REGISTRY_PASS") != "p" {
		t.Errorf("REGISTRY_PASS env = %q", envOf("REGISTRY_PASS"))
	}
	if envOf("BUILD_ARG_FLAGS") != "--build-arg SERVICE=product" {
		t.Errorf("BUILD_ARG_FLAGS env = %q, want --build-arg SERVICE=product", envOf("BUILD_ARG_FLAGS"))
	}

	// docker.sock 挂载。
	var hasMount bool
	for _, vm := range c.VolumeMounts {
		if vm.Name == "docker-sock" && vm.MountPath == "/var/run/docker.sock" {
			hasMount = true
		}
	}
	if !hasMount {
		t.Error("缺少 docker.sock VolumeMount")
	}
	var hasVol bool
	for _, v := range job.Spec.Template.Spec.Volumes {
		if v.Name == "docker-sock" && v.HostPath != nil && v.HostPath.Path == "/var/run/docker.sock" &&
			v.HostPath.Type != nil && *v.HostPath.Type == corev1.HostPathSocket {
			hasVol = true
		}
	}
	if !hasVol {
		t.Error("缺少 docker.sock hostPath Volume")
	}

	// DooD 关键断言：非 privileged。
	if c.SecurityContext != nil && c.SecurityContext.Privileged != nil && *c.SecurityContext.Privileged {
		t.Error("容器不应设 privileged:true（DooD 复用节点 daemon）")
	}
}

func TestFormatBuildArgs(t *testing.T) {
	cases := []struct {
		name string
		in   map[string]string
		want string
	}{
		{"nil", nil, ""},
		{"empty", map[string]string{}, ""},
		{"single", map[string]string{"SERVICE": "product"}, "--build-arg SERVICE=product"},
		{"unsafe value skipped", map[string]string{"SERVICE": "product; rm -rf /", "OK": "v"}, "--build-arg OK=v"},
		{"unsafe key skipped", map[string]string{"bad key": "v", "OK": "v"}, "--build-arg OK=v"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := formatBuildArgs(c.in)
			if got != c.want {
				t.Errorf("formatBuildArgs(%v) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestParseDigest(t *testing.T) {
	logs := "Cloning into 'xxx'...\n... docker push ...\nPAAS_DIGEST=sha256:abc123def\nPAAS_BUILD_DONE=1\n"
	d, err := parseDigest(logs)
	if err != nil {
		t.Fatalf("parseDigest err: %v", err)
	}
	if d != "sha256:abc123def" {
		t.Errorf("digest = %q, want sha256:abc123def", d)
	}

	// 无标记行 → err。
	if _, err := parseDigest("no digest here\n"); err == nil {
		t.Error("无 PAAS_DIGEST 行应返错")
	}
	// 非 hex digest → 不匹配。
	if _, err := parseDigest("PAAS_DIGEST=sha256:NOTHEX\n"); err == nil {
		t.Error("非 hex digest 应不匹配")
	}
}

func TestSanitizeK8SName(t *testing.T) {
	cases := map[string]string{
		"paas-build-Build_UP":  "paas-build-build-up",
		"paas-build-build-abc": "paas-build-build-abc",
		"paas_build.x":         "paas-build-x",
		"-leading":             "leading",
	}
	for in, want := range cases {
		if got := sanitizeK8sName(in); got != want {
			t.Errorf("sanitizeK8sName(%q) = %q, want %q", in, got, want)
		}
	}
	// 超长截断 ≤63。
	long := strings.Repeat("ABCdef", 20) // 120 字符
	if got := sanitizeK8sName(long); len(got) > 63 {
		t.Errorf("sanitizeK8sName 超长未截断: len=%d", len(got))
	}
	if got := sanitizeK8sName(long); strings.HasSuffix(got, "-") {
		t.Errorf("截断后不应以 - 结尾: %q", got)
	}
}

func TestK8sJob_Real(t *testing.T) {
	var k K8sJob
	if !k.Real() {
		t.Error("K8sJob.Real() 应为 true（IsReal 标记）")
	}
}
