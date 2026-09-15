// Package tools 内置工作区工具集（阶段二建立，阶段四扩展产物导出）。
//
// 文件工具被约束在会话独立的本地工作区（workspace/<session_id>/）内；
// 进程工具（bash / python_exec）经 pkg/sandbox 分发执行——本地直跑或进
// Docker 容器，由沙箱配置决定，对模型透明。执行结果统一为喂回 LLM 的文本
// （作为 tool 消息的 content），export_artifact 额外携带导出工件的附件元数据。
package tools

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"mime"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"backend/pkg/errors"
	"backend/pkg/sandbox"
	"backend/pkg/storage"
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
	"read_file":       30,
	"write_file":      60,
	"edit_file":       60,
	"python_exec":     330,
	"export_artifact": 120,
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
					"运行环境契约（违反会直接失败，别凭惯例猜测）：\n" + sandbox.EnvNote(),
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
		map[string]any{
			"type": "function",
			"function": map[string]any{
				"name": "export_artifact",
				"description": "把工作区内的一个文件导出给用户：上传到对象存储并附在当前回答上，" +
					"用户可直接预览/下载。\n" +
					"**何时使用**：当你生成了用户最终关心的产出物（HTML 报告、Markdown 文档、" +
					"图片、PDF、CSV、Excel 等）时，必须调用本工具，而不要把文件内容直接粘贴在回复里。\n" +
					"**何时不使用**：临时的中间文件、调试输出、scratch 文件，不要导出。",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path":     map[string]any{"type": "string", "description": "工作区内的相对路径，例如 report.html"},
						"filename": map[string]any{"type": "string", "description": "可选；展示给用户的文件名（不传则用 path 末尾的文件名）"},
					},
					"required": []string{"path"},
				},
			},
		},
	}
}

var requiredArgs = map[string][]string{
	"read_file":       {"path"},
	"write_file":      {"path", "content"},
	"edit_file":       {"path", "old_string", "new_string"},
	"bash":            {"command"},
	"python_exec":     {"code"},
	"export_artifact": {"path"},
}

// Attachment 导出工件的附件元数据（与前端 Attachment 契约一致，
// 随 tool 消息落库并经 tool_artifact 事件推送）
type Attachment struct {
	URL         string `json:"url"`
	Filename    string `json:"filename"`
	Size        *int64 `json:"size,omitempty"`
	ContentType string `json:"content_type,omitempty"`
}

// Output 单次工具执行的完整产物
type Output struct {
	Text        string       // 喂回 LLM 的执行结果文本
	Attachments []Attachment // export_artifact 等导出的工件
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

// runSubprocess 已随阶段四迁至 pkg/sandbox（本地驱动内）

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
func doExecute(ctx context.Context, sessionID, name string, args map[string]any) Output {
	switch name {
	case "read_file":
		p, err := resolve(sessionID, argString(args, "path"))
		if err != nil {
			return Output{Text: "[执行失败] " + err.Error()}
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return Output{Text: fmt.Sprintf("[未读取] %s 不存在或不是文件。", argString(args, "path"))}
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
				end = start+limit
			}
			if start > len(lines) {
				start = len(lines)
			}
			text = strings.Join(lines[start:end], "")
		}
		return Output{Text: capRead(sanitize(text), argString(args, "path"), paged)}

	case "write_file":
		p, err := resolve(sessionID, argString(args, "path"))
		if err != nil {
			return Output{Text: "[执行失败] " + err.Error()}
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return Output{Text: "[执行失败] " + err.Error()}
		}
		content := argString(args, "content")
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			return Output{Text: "[执行失败] " + err.Error()}
		}
		return Output{Text: fmt.Sprintf("已写入 %d 字符到 %s", len(content), argString(args, "path"))}

	case "edit_file":
		p, err := resolve(sessionID, argString(args, "path"))
		if err != nil {
			return Output{Text: "[执行失败] " + err.Error()}
		}
		path := argString(args, "path")
		data, err := os.ReadFile(p)
		if err != nil {
			return Output{Text: fmt.Sprintf("[未改动] %s 不存在，请先用 write_file 创建。", path)}
		}
		text := string(data)
		old, newStr := argString(args, "old_string"), argString(args, "new_string")
		hits := strings.Count(text, old)
		if hits == 0 {
			return Output{Text: "[未改动] 在 " + path + " 里没找到 old_string。\n" +
				"常见原因：缩进或空白对不上、跨行时换行符没带全、内容已经被改过。\n" +
				"先用 read_file（配 offset/limit）看一眼实际内容再重试。"}
		}
		if hits > 1 && !argBool(args, "replace_all") {
			return Output{Text: fmt.Sprintf("[未改动] old_string 在 %s 里命中 %d 处，无法确定改哪一处。\n请在前后多带几行上下文让它唯一，或传 replace_all=true 全部替换。", path, hits)}
		}
		replaced := strings.Replace(text, old, newStr, 1)
		if argBool(args, "replace_all") {
			replaced = strings.ReplaceAll(text, old, newStr)
		}
		if err := os.WriteFile(p, []byte(replaced), 0o644); err != nil {
			return Output{Text: "[执行失败] " + err.Error()}
		}
		scope := "1 处"
		if argBool(args, "replace_all") {
			scope = fmt.Sprintf("%d 处", hits)
		}
		return Output{Text: fmt.Sprintf("已修改 %s（替换 %s）", path, scope)}

	case "bash":
		inner := argInt(args, "timeout", 30)
		if inner <= 0 {
			inner = 30
		}
		if inner > bashTimeoutMax {
			inner = bashTimeoutMax
		}
		dir, err := SessionDir(sessionID)
		if err != nil {
			return Output{Text: "[执行失败] " + err.Error()}
		}
		res := sandbox.RunBash(ctx, sessionID, dir, argString(args, "command"), inner)
		return Output{Text: formatSandboxResult(res)}

	case "python_exec":
		dir, err := SessionDir(sessionID)
		if err != nil {
			return Output{Text: "[执行失败] " + err.Error()}
		}
		// 脚本落在工作区（docker 驱动下随目录挂载进容器），执行交给沙箱分发
		script := ".pyexec_" + randHex(12) + ".py"
		if err := os.WriteFile(filepath.Join(dir, script), []byte(argString(args, "code")), 0o644); err != nil {
			return Output{Text: "[执行失败] " + err.Error()}
		}
		defer os.Remove(filepath.Join(dir, script))

		res := sandbox.RunPython(ctx, sessionID, dir, script, toolTimeouts["python_exec"])
		return Output{Text: formatSandboxResult(res)}

	case "export_artifact":
		path := argString(args, "path")
		p, err := resolve(sessionID, path)
		if err != nil {
			return Output{Text: "[执行失败] " + err.Error()}
		}
		st, err := os.Stat(p)
		if err != nil || st.IsDir() {
			return Output{Text: fmt.Sprintf("[未读取] %s 不存在或不是文件。", path)}
		}
		if err := storage.CheckSize(st.Size()); err != nil {
			return Output{Text: "[执行失败] " + err.Error()}
		}
		filename := strings.TrimSpace(argString(args, "filename"))
		if filename == "" {
			filename = filepath.Base(path)
		}
		contentType := contentTypeFor(filename)
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		f, err := os.Open(p)
		if err != nil {
			return Output{Text: "[执行失败] " + err.Error()}
		}
		defer f.Close()
		obj, err := storage.Default().Put(ctx, filename, f, st.Size(), contentType)
		if err != nil {
			slog.Warn("产物上传失败", "session", sessionID, "file", filename, "err", errors.Stack(err))
			return Output{Text: "[执行失败] 导出产物上传失败，请稍后重试或告知用户。"}
		}
		size := obj.Size
		return Output{
			Text: fmt.Sprintf("已导出 %s（%d 字节，%s）。链接：%s。请在回复中告知用户该产出物已生成。",
				obj.Filename, size, contentType, obj.URL),
			Attachments: []Attachment{{
				URL:         obj.URL,
				Filename:    obj.Filename,
				Size:        &size,
				ContentType: contentType,
			}},
		}
	}
	return Output{Text: fmt.Sprintf("[执行失败] 未知工具：%s", name)}
}

// commonContentTypes 常见产物类型的内置映射：宿主（尤其 Windows 注册表）可能没登记
// 这些扩展名，缺省会导致浏览器只能按二进制下载而不是预览
var commonContentTypes = map[string]string{
	".md":       "text/markdown",
	".markdown": "text/markdown",
	".txt":      "text/plain",
	".csv":      "text/csv",
	".json":     "application/json",
	".html":     "text/html",
	".htm":      "text/html",
	".svg":      "image/svg+xml",
}

// contentTypeFor 猜测产物 MIME：系统表优先，内置表兜底，未知返回空串
func contentTypeFor(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	if ct := mime.TypeByExtension(ext); ct != "" {
		return ct
	}
	return commonContentTypes[ext]
}

// formatSandboxResult 沙箱执行结果 → 喂回 LLM 的文本；基础设施失败直接给错误前缀
func formatSandboxResult(res sandbox.Result) string {
	if res.Err != nil {
		return "[执行失败] " + res.Err.Error()
	}
	return formatResult(res.Stdout, res.Stderr, res.ExitCode, res.Killed)
}

// ExecuteWithOutput 执行单个工具调用，返回完整产物（文本 + 导出工件）。
// 每次调用有外层硬超时兜底。
func ExecuteWithOutput(ctx context.Context, sessionID, name string, args map[string]any) Output {
	if msg := checkArgs(name, args); msg != "" {
		return Output{Text: msg}
	}
	timeout := timeoutFor(name, args)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	done := make(chan Output, 1)
	go func() {
		done <- func() (out Output) {
			defer func() {
				if r := recover(); r != nil {
					out = Output{Text: fmt.Sprintf("[执行失败] %s: %v", name, r)}
				}
			}()
			return doExecute(ctx, sessionID, name, args)
		}()
	}()

	select {
	case out := <-done:
		return out
	case <-ctx.Done():
		return Output{Text: fmt.Sprintf("[超时] 工具 %s 执行超过 %.0fs 未返回，已放弃。", name, timeout.Seconds())}
	}
}

// Execute 兼容入口：仅取喂回 LLM 的文本（无工件场景与测试使用）
func Execute(ctx context.Context, sessionID, name string, args map[string]any) string {
	return ExecuteWithOutput(ctx, sessionID, name, args).Text
}
