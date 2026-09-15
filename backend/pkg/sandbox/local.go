package sandbox

import (
	"context"
	"os/exec"
	"strings"
	"sync"
	"time"

	"backend/pkg/errors"
)

// localDriver 本地隔离执行：命令以会话工作区为 cwd 跑在宿主机上。
// 与阶段二的原始行为完全一致（超时整棵进程树击杀、Windows 商店占位 python 探测跳过），
// 作为默认驱动保证零配置开箱即用。
type localDriver struct{}

func (localDriver) Name() string { return "local" }

func (localDriver) EnvNote() string { return localEnvNote() }

func (localDriver) RunBash(ctx context.Context, _, hostDir, command string, timeoutSecs int) Result {
	if _, err := exec.LookPath("bash"); err != nil {
		return Result{Err: errors.NewMsg("当前环境没有可用的 bash 解释器。")}
	}
	cctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSecs)*time.Second)
	defer cancel()
	stdout, stderr, code, killed := runSubprocess(cctx, "bash", []string{"-c", command}, hostDir)
	return Result{Stdout: stdout, Stderr: stderr, ExitCode: code, Killed: killed}
}

func (localDriver) RunPython(ctx context.Context, _, hostDir, scriptRel string, timeoutSecs int) Result {
	py, err := lookPython()
	if err != nil {
		return Result{Err: err}
	}
	cctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSecs)*time.Second)
	defer cancel()
	stdout, stderr, code, killed := runSubprocess(cctx, py, []string{scriptRel}, hostDir)
	return Result{Stdout: stdout, Stderr: stderr, ExitCode: code, Killed: killed}
}

func (localDriver) Close(string) {}

// runSubprocess 在 dir 下执行命令，超时由 ctx 控制；返回输出与退出码
func runSubprocess(ctx context.Context, name string, argv []string, dir string) (string, string, int, bool) {
	cmd := exec.CommandContext(ctx, name, argv...)
	cmd.Dir = dir
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	prepareCmd(cmd)
	// 超时先击杀整棵进程树（bash 派生的子进程一起回收），WaitDelay 兜底强杀
	cmd.Cancel = func() error {
		killTree(cmd)
		return cmd.Process.Kill()
	}
	cmd.WaitDelay = 3 * time.Second

	runErr := cmd.Run()
	killed := ctx.Err() != nil
	exitCode := 0
	if runErr != nil {
		if ee, ok := runErr.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
		} else {
			exitCode = -1
		}
	}
	return strings.ToValidUTF8(stdout.String(), ""), strings.ToValidUTF8(stderr.String(), ""), exitCode, killed
}

// lookPython 探测可用的 Python 解释器：LookPath 之外还要真跑一次 `--version`——
// Windows 上 Microsoft Store 的 App Execution Alias 也是一个 python.exe，
// 存在但执行只会提示"未安装"，不验证的话模型会反复撞墙。结果进程内缓存。
var (
	pythonOnce   sync.Once
	pythonFound  string
	pythonErrMsg string
)

func lookPython() (string, error) {
	pythonOnce.Do(func() {
		for _, name := range []string{"python", "python3"} {
			p, err := exec.LookPath(name)
			if err != nil {
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			ver := exec.CommandContext(ctx, p, "--version")
			out, err := ver.CombinedOutput()
			cancel()
			if err != nil || !strings.Contains(string(out), "Python") {
				continue
			}
			pythonFound = p
			return
		}
		pythonErrMsg = "当前环境没有可用的 Python 解释器（已尝试 python / python3）。" +
			"如需执行 Python 代码，请改为用 bash 工具，或告知用户先安装 Python。"
	})
	if pythonFound != "" {
		return pythonFound, nil
	}
	return "", errors.NewMsg(pythonErrMsg)
}
