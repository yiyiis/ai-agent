package sandbox

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"backend/pkg/errors"
)

// defaultLocalRoot local 驱动工作区根目录默认值（相对后端运行目录）
const defaultLocalRoot = "workspace"

// localDriver 本地隔离执行：沙箱即宿主，工作区是驱动自持的 root/<会话ID>/
// 目录（按需创建、会话关闭时清理），文件操作退化为直接 os 调用（零开销），
// 进程以会话工作区为 cwd 跑在宿主机上（超时整棵进程树击杀、Windows 商店占位
// python 探测跳过）。作为默认驱动保证零配置开箱即用。
type localDriver struct {
	root string
}

func newLocalDriver(cfg Config) *localDriver {
	root := cfg.LocalRoot
	if root == "" {
		root = defaultLocalRoot
	}
	return &localDriver{root: root}
}

func (*localDriver) Name() string { return "local" }

func (*localDriver) EnvNote() string { return localEnvNote() }

// dir 会话工作区目录（按需创建；进程执行的 cwd 与文件操作的根）
func (d *localDriver) dir(sessionID string) (string, error) {
	p := filepath.Join(d.root, sessionID)
	if err := os.MkdirAll(p, 0o755); err != nil {
		return "", errors.Join(err, errors.New("mkdir "+p), errors.NewMsg("创建会话工作区失败"))
	}
	return p, nil
}

// resolveLocal 工作区相对路径 → 宿主绝对路径，越界一律拒绝（防 ../ 逃逸）
func resolveLocal(hostDir, rel string) (string, error) {
	rel = filepath.ToSlash(filepath.Clean(rel))
	if rel == "" || rel == "." || filepath.IsAbs(rel) || strings.HasPrefix(rel, "../") || strings.HasPrefix(rel, "/") {
		return "", errors.NewMsg("路径 " + rel + " 超出工作区范围，只能访问工作区内的相对路径")
	}
	p := filepath.Join(hostDir, filepath.FromSlash(rel))
	rootAbs, err := filepath.Abs(hostDir)
	if err != nil {
		return "", err
	}
	pAbs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	r, err := filepath.Rel(rootAbs, pAbs)
	if err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return "", errors.NewMsg("路径 " + rel + " 超出工作区范围，只能访问工作区内的文件")
	}
	return pAbs, nil
}

func (d *localDriver) ReadFile(_ context.Context, sessionID, rel string) ([]byte, error) {
	p, err := resolveLocal(filepath.Join(d.root, sessionID), rel)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrFileNotFound
		}
		return nil, err
	}
	return data, nil
}

func (d *localDriver) WriteFile(_ context.Context, sessionID, rel string, data []byte) error {
	dir, err := d.dir(sessionID)
	if err != nil {
		return err
	}
	p, err := resolveLocal(dir, rel)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, data, 0o644)
}

func (d *localDriver) RemoveFile(sessionID, rel string) {
	if p, err := resolveLocal(filepath.Join(d.root, sessionID), rel); err == nil {
		_ = os.Remove(p)
	}
}

func (d *localDriver) RunBash(ctx context.Context, sessionID, command string, timeoutSecs int) Result {
	if _, err := exec.LookPath("bash"); err != nil {
		return Result{Err: errors.NewMsg("当前环境没有可用的 bash 解释器。")}
	}
	dir, err := d.dir(sessionID)
	if err != nil {
		return Result{Err: err}
	}
	cctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSecs)*time.Second)
	defer cancel()
	stdout, stderr, code, killed := runSubprocess(cctx, "bash", []string{"-c", command}, dir)
	return Result{Stdout: stdout, Stderr: stderr, ExitCode: code, Killed: killed}
}

func (d *localDriver) RunPython(ctx context.Context, sessionID, scriptRel string, timeoutSecs int) Result {
	py, err := lookPython()
	if err != nil {
		return Result{Err: err}
	}
	dir, err := d.dir(sessionID)
	if err != nil {
		return Result{Err: err}
	}
	cctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSecs)*time.Second)
	defer cancel()
	stdout, stderr, code, killed := runSubprocess(cctx, py, []string{scriptRel}, dir)
	return Result{Stdout: stdout, Stderr: stderr, ExitCode: code, Killed: killed}
}

// Close 清理该会话的宿主工作区目录（local 的工作区即真实落盘处）
func (d *localDriver) Close(sessionID string) {
	_ = os.RemoveAll(filepath.Join(d.root, sessionID))
}

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
