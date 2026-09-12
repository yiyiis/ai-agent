// Package agent ReAct 工具调用循环（阶段二核心）。
//
// Run 进入一轮完整对话：落库用户消息 → 循环调 LLM 流式 →
// 收到 tool_calls → 并发执行工具 → 落库 tool 结果 → 再调 LLM，
// 直到模型不再请求工具、达到轮数上限或触发 Loop Guard 死循环防护。
// 每个阶段通过 emit 回调发出协议事件，由路由层转 SSE。
package agent

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"backend/dal/model"
	"backend/dao"
	"backend/pkg/provider"
	"backend/pkg/tools"
	"github.com/google/uuid"
)

// MaxToolRounds 单个 turn 内允许的 tool 调用轮数上限
const MaxToolRounds = 32

// Loop Guard 阈值——同一轮 turn 内，相同 (tool_name, args) 的调用：
// 第 3 次在结果前注入 hint 提醒模型；第 4 次且输出仍然相同则判定无进展，强制结束
const (
	loopHintThreshold = 3
	loopKillThreshold = 4
)

// Event 协议事件（由路由层转成 SSE，字段名以 api 层契约为准）
type Event struct {
	Type      string // user_message_id | delta | tool_call_start | tool_call_result | tool_call_error | done | error
	ID        string
	Content   string // delta 正文增量
	Reasoning string // delta 思考增量
	Name      string // 工具名（tool_call_start）
	Arguments string // 工具入参 JSON（tool_call_start）
	Output    string // 工具结果（tool_call_result）
	Error     string // 错误文案（tool_call_error / error）
	Usage     *provider.Usage
}

// Deps 运行依赖：模型 Provider 与工具执行器（测试可注入假实现）
type Deps struct {
	Provider provider.Provider
	ExecTool func(ctx context.Context, sessionID, name string, args map[string]any) string
}

func (d *Deps) execTool() func(ctx context.Context, sessionID, name string, args map[string]any) string {
	if d.ExecTool != nil {
		return d.ExecTool
	}
	return tools.Execute
}

func sigHash(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])[:8]
}

func loopHintMessage(name string, attempt int) string {
	return fmt.Sprintf("[Agent Guard] 你已用**完全相同的参数**调用 `%s` %d 次。"+
		"继续重复没有意义——请反思失败原因，要么换一种实现思路，要么向用户说明这个操作目前无法完成。", name, attempt)
}

// savedCall 落库后的工具调用（携带结构化字段，便于回放上下文）
type savedCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

func newSavedCall(id, name, args string) savedCall {
	var c savedCall
	c.ID, c.Type = id, "function"
	c.Function.Name, c.Function.Arguments = name, args
	return c
}

// Attachment 聊天附件元数据（与前端 Attachment 契约一致）
type Attachment struct {
	URL         string `json:"url"`
	Filename    string `json:"filename"`
	Size        *int64 `json:"size,omitempty"`
	ContentType string `json:"content_type,omitempty"`
}

// saveMessage 落库一条消息（含工具调用结构），返回 message_id
func saveMessage(ctx context.Context, sessionID, role, content, reasoning string, calls []savedCall, toolCallID, name string, attachments []Attachment) (string, error) {
	msg := &model.Message{
		MessageID:  uuid.New().String(),
		SessionID:  sessionID,
		Role:       role,
		Content:    content,
		ToolCallID: strPtr(toolCallID),
		Name:       strPtr(name),
		CreatedAt:  time.Now(),
	}
	if reasoning != "" {
		msg.Reasoning = strPtr(reasoning)
	}
	if len(calls) > 0 {
		if b, err := json.Marshal(calls); err == nil {
			msg.ToolCalls = strPtr(string(b))
		}
	}
	if len(attachments) > 0 {
		if b, err := json.Marshal(attachments); err == nil {
			msg.Attachments = strPtr(string(b))
		}
	}
	if err := dao.CreateMessage(ctx, msg); err != nil {
		return "", err
	}
	return msg.MessageID, nil
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// buildContext 装配 system 提示与历史消息（注入当前模型说明与工具规范，对冲历史人设污染）
func buildContext(session *model.Session, history []model.Message) []provider.ChatMessage {
	modelNote := fmt.Sprintf("当前对话由模型 %s 提供支持。如果用户询问你是什么模型，如实回答自己是 %s。", session.Model, session.Model)
	toolNote := "## 文件操作规范\n- 修改已有文件时，优先使用 edit_file 进行局部精确替换，不要用 write_file 重写整个文件。\n- write_file 仅用于新建文件或写入短文本（<3KB）；避免在 write_file 的 content 参数中塞入超长大段内容。"
	systemPrompt := session.SystemPrompt
	if systemPrompt == "" {
		systemPrompt = modelNote + "\n\n" + toolNote
	} else {
		systemPrompt += "\n\n" + modelNote + "\n\n" + toolNote
	}

	msgs := []provider.ChatMessage{{Role: "system", Content: systemPrompt}}

	// 协议要求 assistant 发出 tool_calls 后，每个调用必须紧随一条 tool 结果。
	// 中途被打断的轮次（页面刷新/断连/重启）会在库里留下没有 tool 行的 tool_calls，
	// 原样回放会被上游以 400 拒绝（tool call result does not follow tool call）。
	// 这里在轮次边界合成占位结果修复悬挂调用；来历不明的孤儿 tool 行无法在协议中表达，直接丢弃。
	var pendingIDs []string
	done := map[string]bool{}
	flushDangling := func() {
		for _, id := range pendingIDs {
			if done[id] {
				continue
			}
			msgs = append(msgs, provider.ChatMessage{
				Role:       "tool",
				ToolCallID: id,
				Content:    "[未执行] 该轮对话中断，此调用没有结果。不要假设它已成功，请基于当前实际情况继续。",
			})
		}
		pendingIDs, done = nil, map[string]bool{}
	}

	for _, m := range history {
		switch m.Role {
		case "tool":
			toolCallID := ""
			if m.ToolCallID != nil {
				toolCallID = *m.ToolCallID
			}
			if toolCallID == "" || !containsStr(pendingIDs, toolCallID) || done[toolCallID] {
				continue
			}
			done[toolCallID] = true
			msgs = append(msgs, provider.ChatMessage{
				Role:       "tool",
				Content:    m.Content,
				ToolCallID: toolCallID,
			})
		case "assistant":
			flushDangling()
			cm := provider.ChatMessage{Role: "assistant", Content: m.Content}
			if m.ToolCalls != nil && *m.ToolCalls != "" {
				var calls []savedCall
				if err := json.Unmarshal([]byte(*m.ToolCalls), &calls); err == nil && len(calls) > 0 {
					cm.ToolCalls = calls
					for _, c := range calls {
						pendingIDs = append(pendingIDs, c.ID)
					}
				}
			}
			msgs = append(msgs, cm)
		default: // user / system
			flushDangling()
			msgs = append(msgs, provider.ChatMessage{Role: m.Role, Content: m.Content})
		}
	}
	flushDangling()
	return msgs
}

func containsStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// streamOneRound 消费一轮模型流式输出：转发 delta 事件，累计正文/思考/工具调用增量
func streamOneRound(
	ctx context.Context,
	p provider.Provider,
	req *provider.ChatRequest,
	tf *thinkFilter,
	emit func(Event),
) (content string, reasoning string, calls []savedCall, finishReason string, lastUsage *provider.Usage, streamErr error) {
	chunkChan, errChan := p.StreamChat(ctx, req)

	var contentBuf, reasoningBuf strings.Builder
	slots := map[int]*savedCall{}
	var order []int
	startedSlots := map[int]bool{}

	for {
		select {
		case <-ctx.Done():
			return contentBuf.String(), reasoningBuf.String(), nil, "", lastUsage, nil
		case err, ok := <-errChan:
			if ok && err != nil {
				return contentBuf.String(), reasoningBuf.String(), collectSlots(slots, order), finishReason, lastUsage, err
			}
		case chunk, ok := <-chunkChan:
			if !ok {
				return contentBuf.String(), reasoningBuf.String(), collectSlots(slots, order), finishReason, lastUsage, nil
			}
			if chunk.Usage != nil {
				lastUsage = chunk.Usage
			}
			if chunk.FinishReason != "" {
				finishReason = chunk.FinishReason
			}
			if chunk.Content != "" || chunk.ReasoningContent != "" || len(chunk.ToolCalls) > 0 {
				var visible, think string
				if chunk.Content != "" {
					visible, think = tf.Filter(chunk.Content)
				}
				if chunk.ReasoningContent != "" {
					think += chunk.ReasoningContent
				}
				// 严格时序：先推送思考增量，再推送正文增量，杜绝二者混入同一事件导致前端打字机交替切碎
				if think != "" {
					reasoningBuf.WriteString(think)
					emit(Event{Type: "delta", Reasoning: think})
				}
				if visible != "" {
					contentBuf.WriteString(visible)
					emit(Event{Type: "delta", Content: visible})
				}
				for _, tc := range chunk.ToolCalls {
					slot, exists := slots[tc.Index]
					if !exists {
						slot = &savedCall{Type: "function"}
						slots[tc.Index] = slot
						order = append(order, tc.Index)
					}
					if tc.ID != "" {
						slot.ID = tc.ID
					}
					if tc.Name != "" {
						slot.Function.Name = tc.Name
					}
					slot.Function.Arguments += tc.Arguments

					if slot.Function.Name != "" && !startedSlots[tc.Index] {
						id := slot.ID
						if id == "" {
							id = fmt.Sprintf("call_idx_%d", tc.Index)
							slot.ID = id
						}
						startedSlots[tc.Index] = true
						emit(Event{
							Type:      "tool_call_start",
							ID:        id,
							Name:      slot.Function.Name,
							Arguments: "",
						})
					}
				}
			}
		}
	}
}

func collectSlots(slots map[int]*savedCall, order []int) []savedCall {
	calls := make([]savedCall, 0, len(order))
	for _, idx := range order {
		if slots[idx] != nil && slots[idx].Function.Name != "" {
			calls = append(calls, *slots[idx])
		}
	}
	return calls
}

// UploadsDir 上传文件的本地存储目录（与 api.UploadsRoot 同源，测试中可覆写）
var UploadsDir = "uploads"

// materializeAttachments 把用户上传的附件从全局 uploads/ 拷进当前会话工作区，
// 使工作区工具（read_file/edit_file/bash 等）可以直接操作它们。
// 返回成功落入工作区的附件（Filename 已替换为实际落盘名，供上下文注入保持一致）。
// 非本地上传的 url（未来的对象存储）跳过；失败静默降级为"仅引用"。
func materializeAttachments(sessionID string, attachments []Attachment) []Attachment {
	var placed []Attachment
	dir, err := tools.SessionDir(sessionID)
	if err != nil {
		return nil
	}
	for _, a := range attachments {
		name := strings.TrimPrefix(a.URL, "/api/uploads/")
		if name == "" || name == a.URL {
			continue
		}
		data, err := os.ReadFile(filepath.Join(UploadsDir, filepath.Base(name)))
		if err != nil {
			continue
		}
		safe := workspaceName(a.Filename)
		if safe == "" {
			safe = filepath.Base(name) // 退回服务端随机存储名（hex，必然安全）
		}
		if err := os.WriteFile(filepath.Join(dir, safe), data, 0o644); err != nil {
			continue
		}
		a.Filename = safe
		placed = append(placed, a)
	}
	return placed
}

// workspaceName 把原始文件名转成各平台都能落盘的名字：去路径成分、
// 替换 Windows 非法字符与控制符；清洗后无效（空、"."、".."）返回空串
func workspaceName(orig string) string {
	s := filepath.Base(strings.TrimSpace(orig))
	var b strings.Builder
	for _, r := range s {
		switch {
		case r < 0x20 || r == 0x7f, strings.ContainsRune(`<>:"|?*`, r):
			b.WriteRune('_')
		default:
			b.WriteRune(r)
		}
	}
	out := b.String()
	if out == "" || out == "." || out == ".." {
		return ""
	}
	return out
}

// Run 执行一轮完整对话（含多轮工具调用），事件经 emit 回调发出。
// session 为已加载的会话实体，history 为本轮之前的历史消息（不含本条用户输入），
// attachments 为用户随本条消息上传的附件。
func Run(ctx context.Context, deps Deps, session *model.Session, history []model.Message, userContent string, attachments []Attachment, emit func(Event)) error {
	sessionID := session.SessionID

	// 1. 落库用户消息；首条对话顺带提取标题
	stripped := strings.TrimSpace(userContent)
	autoTitle := ""
	if stripped != "" {
		r := []rune(strings.Split(stripped, "\n")[0])
		autoTitle = string(r)
		if len(r) > 20 {
			autoTitle = string(r[:20]) + "..."
		}
	}
	userMsgID, err := saveMessage(ctx, sessionID, "user", userContent, "", nil, "", "", attachments)
	if err != nil {
		return err
	}
	emit(Event{Type: "user_message_id", ID: userMsgID})
	if (session.Title == "新会话" || session.Title == "") && autoTitle != "" {
		_ = dao.UpdateSessionTitle(ctx, sessionID, autoTitle)
	}

	// 2. 装配上下文：历史（不含刚落库的本条用户消息，由 handler 在落库前加载）+ 本轮用户输入
	msgs := buildContext(session, history)
	// 附件落入会话工作区，工具可直接按文件名操作（多模态内容流属于阶段八）
	placed := materializeAttachments(sessionID, attachments)
	userTurn := provider.ChatMessage{Role: "user", Content: userContent}
	if len(placed) > 0 {
		var b strings.Builder
		b.WriteString(userContent)
		b.WriteString("\n\n[用户上传的附件已放入当前工作区，可直接用 read_file 等工具按文件名读取]")
		for _, a := range placed {
			fmt.Fprintf(&b, "\n- %s", a.Filename)
		}
		userTurn.Content = b.String()
	} else if len(attachments) > 0 {
		// 未能落入工作区（如未来的对象存储 url），退化为仅告知引用清单
		var b strings.Builder
		b.WriteString(userContent)
		b.WriteString("\n\n[用户上传的附件]")
		for _, a := range attachments {
			size := ""
			if a.Size != nil {
				size = fmt.Sprintf(", %d bytes", *a.Size)
			}
			fmt.Fprintf(&b, "\n- %s (%s%s)", a.Filename, a.ContentType, size)
		}
		userTurn.Content = b.String()
	}
	msgs = append(msgs, userTurn)
	toolSchemas := tools.Schemas()

	// Loop guard：sig(=tool+args 哈希) -> 历次输出哈希
	guard := map[string][]string{}
	lastAssistantID := ""

	for round := 0; round < MaxToolRounds; round++ {
		req := &provider.ChatRequest{
			Model: session.Model,
			Messages:  msgs,
			Tools:     toolSchemas,
			MaxTokens: 32768, // 与 mirror 对齐；8192 时写文件类工具参数会被截断成半截 JSON
		}
		tf := &thinkFilter{}
		content, reasoning, calls, finish, usage, streamErr := streamOneRound(ctx, deps.Provider, req, tf, emit)

		if streamErr != nil {
			note := fmt.Sprintf("[对话因异常中止：%v]", streamErr)
			text := strings.TrimRight(content, "\n")
			if text != "" {
				text += "\n\n" + note
			} else {
				text = note
			}
			errID, _ := saveMessage(ctx, sessionID, "assistant", text, reasoning, calls, "", "", nil)
			emit(Event{Type: "error", Error: streamErr.Error()})
			emit(Event{Type: "done", ID: errID})
			return nil
		}

		// finish=length 时 tool_calls 是被截断的半截 JSON，不能执行
		if finish == "length" {
			text := content + "\n\n[模型输出被 token 上限截断]"
			id, _ := saveMessage(ctx, sessionID, "assistant", text, reasoning, calls, "", "", nil)
			emit(Event{Type: "error", Error: "回答被 token 上限截断，可重试或拆小任务"})
			emit(Event{Type: "done", ID: id})
			return nil
		}

		assistantID, err := saveMessage(ctx, sessionID, "assistant", content, reasoning, calls, "", "", nil)
		if err != nil {
			return err
		}
		lastAssistantID = assistantID
		msgs = append(msgs, provider.ChatMessage{Role: "assistant", Content: content, ToolCalls: callsOrNil(calls)})

		if len(calls) == 0 || finish != "tool_calls" {
			emit(Event{Type: "done", ID: assistantID, Usage: usage})
			return nil
		}

		// 3. 广播工具调用开始事件
		for _, tc := range calls {
			emit(Event{Type: "tool_call_start", ID: tc.ID, Name: tc.Function.Name, Arguments: orEmptyJSON(tc.Function.Arguments)})
		}

		// 4. 并发执行，按发起顺序取回结果
		exec := deps.execTool()
		outputs := make([]string, len(calls))
		var wg sync.WaitGroup
		for i, tc := range calls {
			wg.Add(1)
			go func(i int, tc savedCall) {
				defer wg.Done()
				args, parseErr := tools.ParseArguments(tc.Function.Arguments)
				if parseErr != "" {
					outputs[i] = parseErr
					return
				}
				outputs[i] = exec(ctx, sessionID, tc.Function.Name, args)
			}(i, tc)
		}
		wg.Wait()

		// 5. Loop Guard 记账 + 结果事件 + 落库
		killed := false
		for i, tc := range calls {
			out := outputs[i]
			sig := tc.Function.Name + ":" + sigHash(tc.Function.Arguments)
			outHash := sigHash(out[:min(len(out), 400)])
			history := guard[sig]
			history = append(history, outHash)
			guard[sig] = history

			if len(history) >= loopHintThreshold {
				out = loopHintMessage(tc.Function.Name, len(history)) + "\n\n" + out
			}
			if len(history) >= loopKillThreshold && allSame(history) {
				killed = true
			}

			if tools.IsErrorResult(out) {
				emit(Event{Type: "tool_call_error", ID: tc.ID, Error: out})
			} else {
				emit(Event{Type: "tool_call_result", ID: tc.ID, Output: out})
			}

			if _, err := saveMessage(ctx, sessionID, "tool", out, "", nil, tc.ID, tc.Function.Name, nil); err != nil {
				return err
			}
			msgs = append(msgs, provider.ChatMessage{Role: "tool", Content: out, ToolCallID: tc.ID})
		}

		if killed {
			killID, _ := saveMessage(ctx, sessionID, "assistant",
				"[已停止：相同的工具调用反复返回相同结果，判定为无进展死循环]", "", nil, "", "", nil)
			emit(Event{Type: "error", Error: "已停止：检测到工具死循环（相同调用返回相同结果）"})
			emit(Event{Type: "done", ID: killID})
			return nil
		}
	}

	emit(Event{Type: "error", Error: fmt.Sprintf("已达单轮工具调用轮数上限（%d 轮）", MaxToolRounds)})
	emit(Event{Type: "done", ID: lastAssistantID})
	return nil
}

func callsOrNil(calls []savedCall) any {
	if len(calls) == 0 {
		return nil
	}
	return calls
}

func orEmptyJSON(s string) string {
	if strings.TrimSpace(s) == "" {
		return "{}"
	}
	return s
}

func allSame(hs []string) bool {
	for _, h := range hs {
		if h != hs[0] {
			return false
		}
	}
	return true
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
