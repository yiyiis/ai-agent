// Package tools 内置工作区工具集（阶段二）。
//
// 所有执行都发生在会话独立的本地工作区（workspace/<session_id>/）内：
// 文件工具被约束在工作区路径下，bash / python_exec 以工作区为 cwd 执行。
// 执行结果统一为喂回 LLM 的文本（作为 tool 消息的 content）。
package tools

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// WorkspaceRoot 会话工作区的根目录（相对后端运行目录），测试中可覆写
var WorkspaceRoot = "workspace"

const (
	maxOutput    = 256 * 1024 // 工具输出上限
	maxReadChars = 100 * 1024 // 单次读文件上限
	bashTimeoutMax = 300      // bash 工具自身允许的最大超时（秒）
)

// ctrlRe 控制字符（保留 \t \n \r）剔除，避免二进制输出把消息变成乱码
var ctrlRe = regexp.MustCompile("[\x00-\x08\x0b\x0c\x0e-\x1f\x7f]")

// toolTimeouts 各工具的外层硬超时（秒）
var toolTimeouts = map[string]int{
	"read_file":   30,
	"write_file":  60,
	"edit_file":   60,
	"python_exec": 330,
}

// errorPrefixes 结果以这些前缀开头时，前端按失败渲染，Agent 不将其视为进展
var errorPrefixes = []string{
	"[执行失败]", "[超时]", "[未读取]", "[未改动]", "[参数缺失]", "[参数解析失败]", "[参数超限]",
}

// Schemas 暴露给模型的工具 schema 列表（OpenAI function calling 协议）
func Schemas() []any {
	return []any{
		map[string]any{
			"type": "function",
			"function": map[string]any{
				"name": "read_file",
				"description": "读取工作区内某个文件的文本内容。\n" +
					"大文件请用 offset / limit 分段读，不要一次性拉全文——那会白白吃掉上下文。" +
					"只是想改其中几行的话，用 `edit_file`，根本不需要先读全文。",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path":   map[string]any{"type": "string", "description": "工作区内的相对路径，例如 src/main.py"},
						"offset": map[string]any{"type": "integer", "description": "从第几行开始读（1 起）。不传则从头读"},
						"limit":  map[string]any{"type": "integer", "description": "最多读多少行。不传则读到结尾（仍受总长度上限约束）"},
					},
					"required": []string{"path"},
				},
			},
		},
		map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":  "write_file",
				"description": "向工作区内某个文件写入文本内容（已存在则覆盖；父目录会自动创建）。",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path":    map[string]any{"type": "string", "description": "工作区内的相对路径"},
						"content": map[string]any{"type": "string", "description": "要写入的文本内容"},
					},
					"required": []string{"path", "content"},
				},
			},
		},
		map[string]any{
			"type": "function",
			"function": map[string]any{
				"name": "edit_file",
				"description": "把文件里的一段文本替换成另一段——改已有文件请优先用它，不要用 write_file 重写整个文件。\n" +
					"文件稍大时整文件重写会被模型输出上限截断，得到一个半截文件。\n" +
					"`old_string` 必须与文件内容**逐字符一致**（含缩进），且默认要求全文只匹配一次；" +
					"命中多处会报错，此时请在 old_string 前后多带几行上下文，或设 replace_all=true。",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path":        map[string]any{"type": "string", "description": "工作区内的相对路径"},
						"old_string":  map[string]any{"type": "string", "description": "要被替换的原文，必须逐字符一致"},
						"new_string":  map[string]any{"type": "string", "description": "替换成的新内容；传空串表示删除这段"},
						"replace_all": map[string]any{"type": "boolean", "description": "命中多处时是否全部替换，默认 false"},
					},
					"required": []string{"path", "old_string", "new_string"},
				},
			},
		},
		map[string]any{
			"type": "function",
			"function": map[string]any{
				"name": "bash",
				"description": "在工作区内执行一条 bash 命令，返回 stdout / stderr / exit_code。" +
					"运行环境契约（违反会直接失败，别凭 Unix 惯例猜测）：\n" +
					"- 宿主是 **Windows + Git Bash**，不是 Linux/macOS；\n" +
					"- 启动目录就是会话工作区，用户上传与工具产出的文件都在这里；不要 cd 到 /tmp 等系统目录找文件（重定向到 /dev/null 可用）；\n" +
					"- Python 解释器命令是 `python`；`python3` 是无效的商店占位符，会报 \"Python was not found\"；要跑 Python 优先用 python_exec 工具；\n" +
					"- 没有 systemd、apt、brew 等，系统依赖不可安装。",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"command": map[string]any{"type": "string", "description": "完整的命令字符串"},
						"timeout": map[string]any{"type": "integer", "description": "超时时间（秒），默认 30，上限 300"},
					},
					"required": []string{"command"},
				},
			},
		},
		map[string]any{
			"type": "function",
			"function": map[string]any{
				"name": "python_exec",
				"description": "在工作区内执行一段 Python 代码。每次调用是**独立进程**，" +
					"变量与 import 状态不会跨调用保留（需要状态就写进文件，下次再读）。" +
					"返回 stdout / stderr / exit_code。",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"code": map[string]any{"type": "string", "description": "要执行的 Python 代码"},
					},
					"required": []string{"code"},
				},
			},
		},
	}
}

var requiredArgs = map[string][]string{
	"read_file":   {"path"},
	"write_file":  {"path", "content"},
	"edit_file":   {"path", "old_string", "new_string"},
	"bash":        {"command"},
	"python_exec": {"code"},
}

// emptyOK 允许传空串的必填参数（edit_file 的 new_string 空串表示删除）
var emptyOK = map[string]bool{"edit_file/new_string": true}

// IsErrorResult 结果是否为失败前缀（前端失败渲染 / Loop Guard 参考）
func IsErrorResult(s string) bool {
	for _, p := range errorPrefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// maxArgumentsBytes 单次 tool_call 参数上限：拦截模型试图一次性写入超大文件等行为，
// 避免撑爆上下文与模型输出上限（提示模型分段写）
const maxArgumentsBytes = 40 << 10

// ParseArguments 解析模型生成的 tool_call 参数 JSON；失败时返回可自我纠正的错误提示
func ParseArguments(raw string) (map[string]any, string) {
	if len(raw) > maxArgumentsBytes {
		return nil, fmt.Sprintf(
			"[参数超限] arguments 长度 %d 超过 %dKB 上限。请把大文件拆成多次 write_file 追加写入（每次一小段），或只写入关键部分。",
			len(raw), maxArgumentsBytes>>10)
	}
	if strings.TrimSpace(raw) == "" {
		raw = "{}"
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil, fmt.Sprintf("[参数解析失败] arguments 不是合法 JSON（常因输出被 token 上限截断）：%v。请重新调用并输出完整 JSON；内容过长时拆分为多次小段写入。", err)
	}
	if parsed == nil {
		return nil, "[参数解析失败] arguments 必须是 JSON 对象。请重新调用。"
	}
	return parsed, ""
}

// SessionDir 返回（并确保存在）会话工作区目录
func SessionDir(sessionID string) (string, error) {
	d := filepath.Join(WorkspaceRoot, sessionID)
	if err := os.MkdirAll(d, 0o755); err != nil {
		return "", fmt.Errorf("创建会话工作区失败: %w", err)
	}
	return d, nil
}

// resolve 把工具入参的路径解析到会话工作区内，越界一律报错
func resolve(sessionID, path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("path 不能为空")
	}
	// Windows 上无盘符的 "/x" 不算 IsAbs，但同样不允许（根路径一律越界）
	if filepath.IsAbs(path) || strings.HasPrefix(path, "/") || strings.HasPrefix(path, "\\") {
		return "", fmt.Errorf("路径 %s 超出工作区范围，只能访问工作区内的相对路径", path)
	}
	root, err := SessionDir(sessionID)
	if err != nil {
		return "", err
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	p := filepath.Clean(filepath.Join(rootAbs, path))
	rel, err := filepath.Rel(rootAbs, p)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("路径 %s 超出工作区范围，只能访问工作区内的文件", path)
	}
	return p, nil
}

// checkArgs 缺必填参数时返回一句能照着改的提示
func checkArgs(name string, args map[string]any) string {
	var missing []string
	for _, k := range requiredArgs[name] {
		if emptyOK[name+"/"+k] {
			if _, ok := args[k]; !ok {
				missing = append(missing, k)
			}
			continue
		}
		v, ok := args[k]
		if !ok {
			missing = append(missing, k)
			continue
		}
		if s, isStr := v.(string); isStr && strings.TrimSpace(s) == "" {
			missing = append(missing, k)
		}
	}
	if len(missing) == 0 {
		return ""
	}
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	got := "（无参数）"
	if len(keys) > 0 {
		got = strings.Join(keys, "、")
	}
	return fmt.Sprintf("[参数缺失] 调用 `%s` 时缺少必填参数（或传了空值）：%s。本次只收到：%s。请补齐后重试。",
		name, strings.Join(missing, "、"), got)
}

func sanitize(s string) string {
	if s == "" {
		return s
	}
	s = ctrlRe.ReplaceAllString(s, "")
	if len(s) <= maxOutput {
		return s
	}
	return s[:maxOutput] + fmt.Sprintf("\n…(已截断 %d 字符)", len(s)-maxOutput)
}

func capRead(text, path string, paged bool) string {
	if len(text) <= maxReadChars {
		return text
	}
	hint := "继续用更大的 offset 往后读"
	if !paged {
		hint = "改用 offset / limit 分段读；只是想改几行的话直接用 edit_file，不必读全文"
	}
	return text[:maxReadChars] + fmt.Sprintf("\n\n…（%s 还有 %d 字符未显示。%s。）", path, len(text)-maxReadChars, hint)
}

func argString(args map[string]any, key string) string {
	if v, ok := args[key]; ok {
		if s, isStr := v.(string); isStr {
			return s
		}
	}
	return ""
}

func argInt(args map[string]any, key string, def int) int {
	v, ok := args[key]
	if !ok {
		return def
	}
	switch n := v.(type) {
	case float64:
		return int(n)
	case float32:
		return int(n)
	case int:
		return n
	case int32:
		return int(n)
	case int64:
		return int(n)
	case string:
		if i, err := strconv.Atoi(strings.TrimSpace(n)); err == nil {
			return i
		}
	}
	return def
}

func argBool(args map[string]any, key string) bool {
	if v, ok := args[key]; ok {
		if b, isBool := v.(bool); isBool {
			return b
		}
	}
	return false
}

func formatResult(stdout, stderr string, exitCode int, killed bool) string {
	var parts []string
	if stdout != "" {
		parts = append(parts, "[stdout]\n"+sanitize(stdout))
	}
	if stderr != "" {
		parts = append(parts, "[stderr]\n"+sanitize(stderr))
	}
	code := strconv.Itoa(exitCode)
	if killed {
		code = "killed(timeout)"
	}
	parts = append(parts, "[exit_code] "+code)
	return strings.Join(parts, "\n")
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

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)[:n]
}

// timeoutFor 工具的外层硬超时
func timeoutFor(name string, args map[string]any) time.Duration {
	if name == "bash" {
		inner := argInt(args, "timeout", 30)
		if inner <= 0 {
			inner = 30
		}
		if inner > bashTimeoutMax {
			inner = bashTimeoutMax
		}
		return time.Duration(inner+10) * time.Second
	}
	if secs, ok := toolTimeouts[name]; ok {
		return time.Duration(secs) * time.Second
	}
	return 120 * time.Second
}

// doExecute 单个工具的实际执行逻辑
func doExecute(ctx context.Context, sessionID, name string, args map[string]any) string {
	switch name {
	case "read_file":
		p, err := resolve(sessionID, argString(args, "path"))
		if err != nil {
			return "[执行失败] " + err.Error()
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return fmt.Sprintf("[未读取] %s 不存在或不是文件。", argString(args, "path"))
		}
		text := string(data)
		offset := argInt(args, "offset", 0)
		limit := argInt(args, "limit", 0)
		paged := offset != 0 || limit != 0
		if paged {
			lines := strings.SplitAfter(text, "\n")
			start := offset - 1
			if start < 0 {
				start = 0
			}
			end := len(lines)
			if limit > 0 && start+limit < end {
				end = start + limit
			}
			if start > len(lines) {
				start = len(lines)
			}
			text = strings.Join(lines[start:end], "")
		}
		return capRead(sanitize(text), argString(args, "path"), paged)

	case "write_file":
		p, err := resolve(sessionID, argString(args, "path"))
		if err != nil {
			return "[执行失败] " + err.Error()
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return "[执行失败] " + err.Error()
		}
		content := argString(args, "content")
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			return "[执行失败] " + err.Error()
		}
		return fmt.Sprintf("已写入 %d 字符到 %s", len(content), argString(args, "path"))

	case "edit_file":
		p, err := resolve(sessionID, argString(args, "path"))
		if err != nil {
			return "[执行失败] " + err.Error()
		}
		path := argString(args, "path")
		data, err := os.ReadFile(p)
		if err != nil {
			return fmt.Sprintf("[未改动] %s 不存在，请先用 write_file 创建。", path)
		}
		text := string(data)
		old, newStr := argString(args, "old_string"), argString(args, "new_string")
		hits := strings.Count(text, old)
		if hits == 0 {
			return "[未改动] 在 " + path + " 里没找到 old_string。\n" +
				"常见原因：缩进或空白对不上、跨行时换行符没带全、内容已经被改过。\n" +
				"先用 read_file（配 offset/limit）看一眼实际内容再重试。"
		}
		if hits > 1 && !argBool(args, "replace_all") {
			return fmt.Sprintf("[未改动] old_string 在 %s 里命中 %d 处，无法确定改哪一处。\n请在前后多带几行上下文让它唯一，或传 replace_all=true 全部替换。", path, hits)
		}
		replaced := strings.Replace(text, old, newStr, 1)
		if argBool(args, "replace_all") {
			replaced = strings.ReplaceAll(text, old, newStr)
		}
		if err := os.WriteFile(p, []byte(replaced), 0o644); err != nil {
			return "[执行失败] " + err.Error()
		}
		scope := "1 处"
		if argBool(args, "replace_all") {
			scope = fmt.Sprintf("%d 处", hits)
		}
		return fmt.Sprintf("已修改 %s（替换 %s）", path, scope)

	case "bash":
		if _, err := exec.LookPath("bash"); err != nil {
			return "[执行失败] 当前环境没有可用的 bash 解释器。"
		}
		inner := argInt(args, "timeout", 30)
		if inner <= 0 {
			inner = 30
		}
		if inner > bashTimeoutMax {
			inner = bashTimeoutMax
		}
		dir, err := SessionDir(sessionID)
		if err != nil {
			return "[执行失败] " + err.Error()
		}
		ctx, cancel := context.WithTimeout(ctx, time.Duration(inner)*time.Second)
		defer cancel()
		stdout, stderr, code, killed := runSubprocess(ctx, "bash", []string{"-c", argString(args, "command")}, dir)
		return formatResult(stdout, stderr, code, killed)

	case "python_exec":
		py, err := lookPython()
		if err != nil {
			return "[执行失败] 当前环境没有可用的 Python 解释器。"
		}
		dir, err := SessionDir(sessionID)
		if err != nil {
			return "[执行失败] " + err.Error()
		}
		script := filepath.Join(dir, ".pyexec_"+randHex(12)+".py")
		if err := os.WriteFile(script, []byte(argString(args, "code")), 0o644); err != nil {
			return "[执行失败] " + err.Error()
		}
		defer os.Remove(script)

		ctx, cancel := context.WithTimeout(ctx, time.Duration(toolTimeouts["python_exec"])*time.Second)
		defer cancel()
		stdout, stderr, code, killed := runSubprocess(ctx, py, []string{filepath.Base(script)}, dir)
		return formatResult(stdout, stderr, code, killed)
	}
	return fmt.Sprintf("[执行失败] 未知工具：%s", name)
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
	return "", fmt.Errorf("%s", pythonErrMsg)
}

// Execute 执行单个工具调用，返回喂回 LLM 的文本。每次调用有外层硬超时兜底。
func Execute(ctx context.Context, sessionID, name string, args map[string]any) string {
	if msg := checkArgs(name, args); msg != "" {
		return msg
	}
	timeout := timeoutFor(name, args)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	done := make(chan string, 1)
	go func() {
		done <- func() (out string) {
			defer func() {
				if r := recover(); r != nil {
					out = fmt.Sprintf("[执行失败] %s: %v", name, r)
				}
			}()
			return doExecute(ctx, sessionID, name, args)
		}()
	}()

	select {
	case out := <-done:
		return out
	case <-ctx.Done():
		return fmt.Sprintf("[超时] 工具 %s 执行超过 %.0fs 未返回，已放弃。", name, timeout.Seconds())
	}
}
