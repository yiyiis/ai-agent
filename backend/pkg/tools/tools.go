// Package tools 内置工作区工具集（阶段二建立，阶段四扩展产物导出）。
//
// 文件与进程工具统一经 pkg/sandbox 分发，操作各自会话的沙箱工作区（local
// 直跑宿主目录 / docker 容器 / e2b 云端虚机，由沙箱配置决定，对模型透明）。
// 执行结果统一为喂回 LLM 的文本（作为 tool 消息的 content），export_artifact
// 额外携带导出工件的附件元数据。
package tools

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"mime"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"backend/pkg/errors"
	"backend/pkg/sandbox"
	"backend/pkg/storage"
)

const (
	maxOutput    = 256 * 1024 // 工具输出上限
	maxReadChars = 100 * 1024 // 单次读文件上限
	bashTimeoutMax = 300      // bash 工具自身允许的最大超时（秒）

	// SkillMountPrefix 工作区内 skill 挂载目录前缀（阶段五：pkg/skill.MountDir），
	// 挂载文件是只读引用，写入类工具在此拦截
	SkillMountPrefix = "skills/"
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

// LoadSkillTool 意图驱动自主装载的工具名。schema 在此暴露，但真正的执行在
// agent 层拦截（它要改写 turn 内的挂载列表与 secrets 环境变量，且没有沙箱
// 命令要跑），tools 层只负责参数校验兜底。
const LoadSkillTool = "load_skill"

// LoadSkillSchema load_skill 的工具 schema。仅当本轮存在"未加载的候选
// skill"时由 agent 层拼进工具列表——否则模型会看到一个永远只能返回
// "没有这个 skill"的工具，白白浪费 token 和调用轮次。
func LoadSkillSchema() map[string]any {
	return map[string]any{
		"type": "function",
		"function": map[string]any{
			"name": LoadSkillTool,
			"description": "加载一个「Skill 目录」里列出的 skill：把它的文件挂载到工作区 skills/<目录名>/，" +
				"并返回它的完整使用说明（SKILL.md）。\n" +
				"**何时使用**：用户的需求落在目录中某个 skill 的用途范围内，而该 skill 还没被加载时。\n" +
				"**何时不使用**：目录里没有匹配的 skill（直接正常回答即可）；" +
				"或该 skill 已在「可用 Skills（已挂载）」中列出（说明已经加载好了，直接用）。\n" +
				"加载后请按返回的说明用 bash / python_exec 调用其中的脚本。",
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name": map[string]any{"type": "string", "description": "skill 名，与 Skill 目录里列出的完全一致"},
				},
				"required": []string{"name"},
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
	"load_skill":      {"name"},
}

// argExamples 各工具的参数示例：参数缺失时随纠错提示一起返回——把"补齐"
// 变成"照抄"，退化中的弱模型也能接住（曾出现 MiniMax 连发空 arguments 的塌缩）
var argExamples = map[string]string{
	"read_file":       `{"path": "report.md"}`,
	"write_file":      `{"path": "notes.txt", "content": "要写入的文本内容"}`,
	"edit_file":       `{"path": "notes.txt", "old_string": "被替换的原文", "new_string": "替换后的文本"}`,
	"bash":            `{"command": "ls -la"}`,
	"python_exec":     `{"code": "print(1)"}`,
	"export_artifact": `{"path": "report.md"}`,
	"load_skill":      `{"name": "fetch-url"}`,
}

// Attachment 导出工件的附件元数据（与前端 Attachment 契约一致，
// 随 tool 消息落库并经 tool_artifact 事件推送）
type Attachment struct {
	URL         string `json:"url"`
	Filename    string `json:"filename"`
	Size        *int64 `json:"size,omitempty"`
	ContentType string `json:"content_type,omitempty"`
}

// ---------- Skill secrets 的环境变量注入（阶段五） ----------

// envCtxKey context 键类型（包私有，防止外部伪造键值碰撞）
type envCtxKey struct{}

// WithEnv 把 skill secrets（KEY=VALUE 形式）注入 context：bash / python_exec
// 执行时读取并传给沙箱进程。agent 层在每批工具执行前按当轮已装载的 skill
// 重建（load_skill 命中后同批后续工具即拿得到新合并的机密）。
func WithEnv(ctx context.Context, env []string) context.Context {
	if len(env) == 0 {
		return ctx
	}
	return context.WithValue(ctx, envCtxKey{}, env)
}

// EnvFrom 取出 context 里注入的环境变量（未注入返回 nil）
func EnvFrom(ctx context.Context) []string {
	if v, ok := ctx.Value(envCtxKey{}).([]string); ok {
		return v
	}
	return nil
}

// SortedEnv map 形式的 secrets → 排序后的 KEY=VALUE 切片（确定性输出，便于测试）
func SortedEnv(m map[string]string) []string {
	if len(m) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+m[k])
	}
	return out
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

// 路径越界校验已下沉到 sandbox.cleanRel / resolveLocal（驱动侧防御），
// 工具层只传相对路径。

// CheckArgs 缺必填参数时返回一句能照着改的提示（空串表示参数齐全；
// agent 层据此在执行前落参数异常的取证日志）
func CheckArgs(name string, args map[string]any) string {
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
	if ex, ok := argExamples[name]; ok {
		return fmt.Sprintf("[参数缺失] 调用 `%s` 时缺少必填参数（或传了空值）：%s。本次只收到：%s。正确格式示例：%s。请按此格式补齐后重试。",
			name, strings.Join(missing, "、"), got, ex)
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

// doExecute 单个工具的实际执行逻辑。文件工具统一经沙箱驱动读写（local 驱动
// 退化为宿主 os 调用，docker/e2b 经传输 API 进出沙箱），进程工具经沙箱执行。
func doExecute(ctx context.Context, sessionID, name string, args map[string]any) Output {
	switch name {
	case "read_file":
		rel := argString(args, "path")
		data, err := readWorkspaceFile(ctx, sessionID, rel)
		if err != nil {
			return Output{Text: fileErrOutput(err, "[未读取] "+rel+" 不存在或不是文件。")}
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
		return Output{Text: capRead(sanitize(text), rel, paged)}

	case "write_file":
		rel := argString(args, "path")
		if msg := skillMountGuard(rel); msg != "" {
			return Output{Text: msg}
		}
		content := argString(args, "content")
		if err := sandbox.WriteFile(ctx, sessionID, rel, []byte(content)); err != nil {
			return Output{Text: "[执行失败] " + err.Error()}
		}
		return Output{Text: fmt.Sprintf("已写入 %d 字符到 %s", len(content), rel)}

	case "edit_file":
		path := argString(args, "path")
		if msg := skillMountGuard(path); msg != "" {
			return Output{Text: msg}
		}
		data, err := readWorkspaceFile(ctx, sessionID, path)
		if err != nil {
			return Output{Text: fileErrOutput(err, "[未改动] "+path+" 不存在，请先用 write_file 创建。")}
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
		if err := sandbox.WriteFile(ctx, sessionID, path, []byte(replaced)); err != nil {
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
		res := sandbox.RunBash(ctx, sessionID, argString(args, "command"), EnvFrom(ctx), inner)
		return Output{Text: formatSandboxResult(res)}

	case "python_exec":
		// 脚本经驱动写进沙箱工作区，执行后清理
		script := ".pyexec_" + randHex(12) + ".py"
		if err := sandbox.WriteFile(ctx, sessionID, script, []byte(argString(args, "code"))); err != nil {
			return Output{Text: "[执行失败] " + err.Error()}
		}
		defer sandbox.RemoveFile(sessionID, script)

		res := sandbox.RunPython(ctx, sessionID, script, EnvFrom(ctx), toolTimeouts["python_exec"])
		return Output{Text: formatSandboxResult(res)}

	case LoadSkillTool:
		// 正常路径由 agent 层拦截执行（改写挂载列表与 secrets）；落到这里
		// 说明调用链装配异常，给出可定位的失败而不是"未知工具"
		return Output{Text: "[执行失败] load_skill 需由会话引擎拦截执行（要改写技能挂载与机密注入），工具层不受理。"}

	case "export_artifact":
		path := argString(args, "path")
		data, err := readWorkspaceFile(ctx, sessionID, path)
		if err != nil {
			return Output{Text: fileErrOutput(err, "[未读取] "+path+" 不存在或不是文件。")}
		}
		if err := storage.CheckSize(int64(len(data))); err != nil {
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
		obj, err := storage.Default().Put(ctx, filename, bytes.NewReader(data), int64(len(data)), contentType)
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

// readWorkspaceFile 经沙箱驱动读取工作区文件
func readWorkspaceFile(ctx context.Context, sessionID, rel string) ([]byte, error) {
	return sandbox.ReadFile(ctx, sessionID, rel)
}

// skillMountGuard 挂载进工作区的 skill 文件是只读引用，写入类工具在此拦截。
// 模型想"修一下 skill 脚本"时给一句可自纠正的提示，比放行后污染挂载源友好。
func skillMountGuard(rel string) string {
	clean := path.Clean(strings.TrimSpace(rel))
	if clean == "skills" || strings.HasPrefix(clean, SkillMountPrefix) {
		return "[未改动] " + rel + " 位于 skill 挂载目录 " + SkillMountPrefix + " 下，是只读的技能文件，不能修改。" +
			"如需自定义脚本，请复制到工作区其他路径后再改，或告知用户由管理员更新该 skill。"
	}
	return ""
}

// fileErrOutput 文件读取错误的提示映射：不存在给可自纠正提示，其余给失败前缀
func fileErrOutput(err error, notFoundMsg string) string {
	if errors.Is(err, sandbox.ErrFileNotFound) {
		return notFoundMsg
	}
	return "[执行失败] " + err.Error()
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
	if msg := CheckArgs(name, args); msg != "" {
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
