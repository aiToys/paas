package airsync

import (
	"fmt"
	"os/exec"
)

// CmdRunner 抽象命令执行（生产用 os/exec；测试可 mock）。
type CmdRunner interface {
	Run(name string, args ...string) (string, error)
}

// osRunner 用 os/exec 实际执行。
type osRunner struct{}

// DefaultRunner 是生产用 runner（docker/helm/kubectl 实际执行）。
var DefaultRunner CmdRunner = osRunner{}

func (osRunner) Run(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s %v: %w: %s", name, args, err, out)
	}
	return string(out), nil
}
