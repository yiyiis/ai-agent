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
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"backend/constants"
	"backend/dal/model"
	"backend/dao"
	"backend/pkg/ctxpolicy"
	"backend/pkg/provider"
	"backend/pkg/sandbox"
	"backend/pkg/storage"
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
	Type      string // user_message_id | delta | tool_call_start | tool_call_result | tool_call_error | round_progress | tool_artifact | skill_loaded | done | error
	ID        string
	Content   string // delta 正文增量
	Reasoning string // delta 思考增量
	Name      string // 工具名（tool_call_start）/ 技能名（skill_loaded）
	Arguments string // 工具入参 JSON（tool_call_start）
	Output    string // 工具结果（tool_call_result）
	Error     string // 错误文案（tool_call_error / error）
	Usage     *provider.Usage
	// SkillID load_skill 自主装载成功时对应的注册行 ID（skill_loaded，
	// 前端据此同步启用列表与角标）
	SkillID int64
	// Attachments 导出的工件（tool_artifact，每次事件携带一个；
	// ID 为对应 tool_call_id，前端据此挂到工具调用卡片上）
	Attachments []Attachment
}

// Deps 运行依赖：模型 Provider 与工具执行器（测试可注入假实现）
type Deps struct {
	Provider provider.Provider
	ExecTool func(ctx context.Context, sessionID, name string, args map[string]any) tools.Output
}

func (d *Deps) execTool() func(ctx context.Context, sessionID, name string, args map[string]any) tools.Output {
	if d.ExecTool != nil {
		return d.ExecTool
	}
	return tools.ExecuteWithOutput
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
	// fragCount 携带过 argument 字节的流式分片数。仅活动流内统计（不落库），
	// 工具参数异常时作为取证元数据输出——区分"空对象整块到达"与"零分片"
	fragCount int
}

func newSavedCall(id, name, args string) savedCall {
	var c savedCall
	c.ID, c.Type = id, "function"
	c.Function.Name, c.Function.Arguments = name, args
	return c
}

// Attachment 聊天附件 / 工件元数据（与前端 Attachment 契约一致；定义为 tools 包的别名，
// export_artifact 的产物在工具层产生、在消息与事件层消费）
type Attachment = tools.Attachment

// saveMessage 落库一条消息（含工具调用结构），返回 message_id。
// 落库不随 Turn 取消而中断——中止/断连时已生成的部分内容仍要持久化，
// 故剥离 ctx 的取消信号（保留注入的值，如 DB 连接）。
func saveMessage(ctx context.Context, sessionID, role, content, reasoning string,
	calls []savedCall, toolCallID, name string, attachments []Attachment, ex *msgExtras) (string, error) {
	ctx = context.WithoutCancel(ctx)
	msg := &model.Message{
		MessageID:  uuid.New().String(),
		SessionID:  sessionID,
		Role:       role,
		Content:    strings.ToValidUTF8(content, ""),
		ToolCallID: strPtr(toolCallID),
		Name:       strPtr(name),
		CreatedAt:  time.Now(),
	}
	if reasoning != "" {
		msg.Reasoning = strPtr(strings.ToValidUTF8(reasoning, ""))
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
	if ex != nil {
		msg.TokensFull = ex.tokensFull
		msg.StubTokens = ex.stubTokens
		if ex.stubContent != "" {
			msg.StubContent = strPtr(ex.stubContent)
		}
		if ex.stubToolCalls != "" {
			msg.StubToolCalls = strPtr(ex.stubToolCalls)
		}
		if ex.blobRef != "" {
			msg.BlobRef = strPtr(ex.blobRef)
		}
		if ex.forceStub {
			msg.CtxMode = "stub"
		}
		if ex.toolTarget != "" {
			msg.ToolTarget = strPtr(ex.toolTarget)
		}
		if ex.toolOp != "" {
			msg.ToolOp = strPtr(ex.toolOp)
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

// ctxRow 组装行：消息 + 冷区覆盖值（分区后按 zoneItem 决定用哪一侧）
type ctxRow struct {
	msg         provider.ChatMessage
	coldCalls   any    // 冷区 assistant 行的 stub 版 tool_calls（nil=不换）
	coldContent string // 冷区 tool 行的 stub 内容（""=content 即口径：错误与小结果）
}

// buildContext 装配 system 提示与历史消息（注入当前模型说明与工具规范，对冲历史
// 人设污染，支持多模态视觉历史回放），并做热/冷分区：payload 行（工具调用参数与
// 结果）在热区保留原文（content/tool_calls 列），冷区换 stub 口径（stub 列，旧数据
// 现场按同一套规则生成）；叙事行（user / 纯文本 assistant / system）永远原文。
// 历史图片仅保留最近 ImageKeepLast 张真实图，其余换文本占位（不做 URL/base64 解析）。
func buildContext(ctx context.Context, session *model.Session, history []model.Message) []provider.ChatMessage {
	if ctx == nil {
		ctx = context.Background()
	}
	modelNote := fmt.Sprintf("当前对话由模型 %s 提供支持。如果用户询问你是什么模型，如实回答自己是 %s。", session.Model, session.Model)
	toolNote := "## 文件操作规范\n" +
		"- 修改已有文件时，优先使用 edit_file 进行局部精确替换，不要用 write_file 重写整个文件。\n" +
		"- write_file 仅用于新建文件或写入短文本（<3KB）；避免在 write_file 的 content 参数中塞入超长大段内容。\n\n" +
		"## 产物交付规范\n" +
		"- 凡是用户最终关心的产出物（文档、报告、代码文件、图片、数据文件等），写入工作区后**必须**再调用 export_artifact 导出，用户才能拿到可下载/预览的链接。\n" +
		"- 不要只在回复里描述或粘贴文件内容来代替导出；正文概述可以写，但链接必须通过 export_artifact 给出。"
	systemPrompt := session.SystemPrompt
	if systemPrompt == "" {
		systemPrompt = modelNote + "\n\n" + toolNote
	} else {
		systemPrompt += "\n\n" + modelNote + "\n\n" + toolNote
	}

	// 历史图片两遍扫描：预收集全部图片附件位置 → 仅最近 ImageKeepLast 张进保留集。
	// 被裁图片在拼装时换成文本占位且【不】做磁盘读取与 base64 编码。
	type imgSlot struct{ rowIdx, attIdx int }
	var imgOrder []imgSlot
	for hi := range history {
		if history[hi].Role != "user" || history[hi].Attachments == nil || *history[hi].Attachments == "" {
			continue
		}
		var atts []Attachment
		if json.Unmarshal([]byte(*history[hi].Attachments), &atts) != nil {
			continue
		}
		for j, a := range atts {
			if isImageAttachment(a) {
				imgOrder = append(imgOrder, imgSlot{hi, j})
			}
		}
	}
	keepImg := make(map[imgSlot]bool, ctxpolicy.Current().ImageKeepLast)
	for k := len(imgOrder) - 1; k >= 0 && len(keepImg) < ctxpolicy.Current().ImageKeepLast; k-- {
		keepImg[imgOrder[k]] = true
	}

	rows := []ctxRow{{msg: provider.ChatMessage{Role: "system", Content: systemPrompt}}}
	items := []zoneItem{{}} // 与 system 行对齐（叙事，不参与分区）

	// 协议要求 assistant 发出 tool_calls 后，每个调用必须紧随一条 tool 结果。
	// 中途被打断的轮次（页面刷新/断连/重启）会在库里留下没有 tool 行的 tool_calls，
	// 原样回放会被上游以 400 拒绝（tool call result does not follow tool call）。
	// 这里在轮次边界合成占位结果修复悬挂调用；来历不明的孤儿 tool 行无法在协议中表达，直接丢弃。
	// 合成行内容极小，按叙事处理（不参与分区）。
	var pendingIDs []string
	done := map[string]bool{}
	flushDangling := func() {
		for _, id := range pendingIDs {
			if done[id] {
				continue
			}
			rows = append(rows, ctxRow{msg: provider.ChatMessage{
				Role:       "tool",
				ToolCallID: id,
				Content:    "[未执行] 该轮对话中断，此调用没有结果。不要假设它已成功，请基于当前实际情况继续。",
			}})
			items = append(items, zoneItem{})
		}
		pendingIDs, done = nil, map[string]bool{}
	}

	// curCalls 当前批次的 callID → 原调用（tool 行旧数据现场生成 stub 时取配对参数）
	curCalls := map[string]savedCall{}

	for hi := range history {
		m := &history[hi]
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
			// 冷区口径三来源：stub 列 / 旧数据（治理列缺失）现场生成 / content 即口径
			coldContent := ""
			toolName := ""
			if m.Name != nil {
				toolName = *m.Name
			}
			if m.StubContent != nil && *m.StubContent != "" {
				coldContent = *m.StubContent
			} else if m.TokensFull == 0 && m.Content != "" {
				if c, ok := curCalls[toolCallID]; ok {
					coldContent = stubForToolResult(toolName, c.Function.Arguments, m.Content)
				}
			}
			rows = append(rows, ctxRow{
				msg:         provider.ChatMessage{Role: "tool", Content: m.Content, ToolCallID: toolCallID},
				coldContent: coldContent,
			})
			items = append(items, zoneItem{
				role:       "tool",
				isPayload:  true,
				toolName:   toolName,
				tokensFull: tokensOr(m.TokensFull, estimateTokens(m.Content)),
			})
		case "assistant":
			flushDangling()
			curCalls = map[string]savedCall{}
			cm := provider.ChatMessage{Role: "assistant", Content: m.Content}
			var calls []savedCall
			hasCalls := false
			if m.ToolCalls != nil && *m.ToolCalls != "" {
				if err := json.Unmarshal([]byte(*m.ToolCalls), &calls); err == nil && len(calls) > 0 {
					cm.ToolCalls = calls
					hasCalls = true
					for _, c := range calls {
						pendingIDs = append(pendingIDs, c.ID)
						curCalls[c.ID] = c
					}
				}
			}
			row := ctxRow{msg: cm}
			if hasCalls {
				// 冷区 stub 版：stub 列优先，缺失（旧数据/序列化失败）现场生成（同一规则，逐字节一致）
				var cold []savedCall
				if m.StubToolCalls != nil && *m.StubToolCalls != "" {
					_ = json.Unmarshal([]byte(*m.StubToolCalls), &cold)
				}
				if len(cold) == 0 {
					cold = stubForToolCalls(calls)
				}
				row.coldCalls = cold
			}
			rows = append(rows, row)
			if hasCalls {
				items = append(items, zoneItem{
					role:       "assistant",
					isPayload:  true,
					calls:      calls,
					forceStub:  m.CtxMode == "stub",
					tokensFull: tokensOr(m.TokensFull, estimateTokens(m.Content)+callsTokens(calls)),
				})
			} else {
				items = append(items, zoneItem{}) // 纯文本：叙事
			}
		default: // user / system
			flushDangling()
			curCalls = map[string]savedCall{}
			if m.Role == "user" && m.Attachments != nil && *m.Attachments != "" {
				var atts []Attachment
				if json.Unmarshal([]byte(*m.Attachments), &atts) == nil && len(atts) > 0 {
					var attachParts []provider.ContentPart
					hasImage := false
					for j, a := range atts {
						if !isImageAttachment(a) {
							continue
						}
						hasImage = true
						if keepImg[imgSlot{hi, j}] {
							imgURL, err := resolveAttachmentImageURL(ctx, a)
							if err == nil && imgURL != "" {
								attachParts = append(attachParts, provider.ContentPart{
									Type: string(provider.ContentPartTypeImageURL),
									ImageURL: &provider.ImageURL{
										URL: imgURL,
									},
								})
								continue
							}
						}
						// 被裁图片：文本占位（文件仍在工作区，模型可按需感知其存在）
						attachParts = append(attachParts, provider.ContentPart{
							Type: string(provider.ContentPartTypeText),
							Text: "[图片已省略: " + a.Filename + "]",
						})
					}
					if hasImage {
						var parts []provider.ContentPart
						if m.Content != "" {
							parts = append(parts, provider.ContentPart{
								Type: string(provider.ContentPartTypeText),
								Text: m.Content,
							})
						}
						parts = append(parts, attachParts...)
						rows = append(rows, ctxRow{msg: provider.ChatMessage{
							Role:         "user",
							Content:      m.Content,
							ContentParts: parts,
						}})
						items = append(items, zoneItem{})
						continue
					}
				}
			}
			rows = append(rows, ctxRow{msg: provider.ChatMessage{Role: m.Role, Content: m.Content}})
			items = append(items, zoneItem{})
		}
	}
	flushDangling()

	// 分区：applyCold 就地把冷区行切到 stub 口径，然后统一渲染
	for i := range rows {
		switch {
		case items[i].role == "assistant" && rows[i].coldCalls != nil:
			cold := rows[i].coldCalls
			target := &rows[i].msg
			items[i].applyCold = func() { target.ToolCalls = cold }
		case items[i].role == "tool" && rows[i].coldContent != "":
			cold := rows[i].coldContent
			target := &rows[i].msg
			items[i].applyCold = func() { target.Content = cold }
		}
	}
	zoneBackward(items, session.Model)

	msgs := make([]provider.ChatMessage, 0, len(rows))
	for i := range rows {
		msgs = append(msgs, rows[i].msg)
	}
	return msgs
}

// tokensOr 治理列估算优先，旧数据（0）回退现场估算
func tokensOr(v, fallback int32) int32 {
	if v > 0 {
		return v
	}
	return fallback
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
					if tc.Arguments != "" {
						slot.fragCount++
					}
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

// UploadsDir 上传附件下载客户端（从对象存储拉回附件用，超时与体积上限单独约束）
var attachmentHTTPClient = &http.Client{Timeout: 2 * time.Minute}

// collectAttachments 汇总会话内全部附件：历史消息落库的 attachments + 当轮请求
// 携带的，按 URL 去重（当轮在后，覆盖同名历史项的展示名）。ephemeral 工作区下
// 沙箱随时可能被回收重建，每轮把全部附件重物化一遍，保证"用户传过的原件"永远
// 拿得回来（历史 URL 来自 messages 表，无需额外存储）。
func collectAttachments(history []model.Message, current []Attachment) []Attachment {
	seen := make(map[string]bool, len(current))
	var out []Attachment
	appendOne := func(a Attachment) {
		if a.URL == "" || seen[a.URL] {
			return
		}
		seen[a.URL] = true
		out = append(out, a)
	}
	for i := range history {
		m := &history[i]
		if m.Attachments == nil || *m.Attachments == "" {
			continue
		}
		var atts []Attachment
		if json.Unmarshal([]byte(*m.Attachments), &atts) != nil {
			continue
		}
		for _, a := range atts {
			appendOne(a)
		}
	}
	for _, a := range current {
		appendOne(a)
	}
	return out
}

// materializeAttachments 把用户上传的附件落进当前会话工作区（沙箱），
// 使工作区工具（read_file/edit_file/bash 等）可以直接操作它们。
// 本地存储驱动（/api/uploads/ URL）直接从 uploads/ 目录读字节；对象存储驱动
//（COS 公网 URL）下载回填。返回成功落入工作区的附件（Filename 已替换为实际
// 落盘名）；失败静默降级为"仅引用"。
func materializeAttachments(ctx context.Context, sessionID string, attachments []Attachment) []Attachment {
	var placed []Attachment
	for _, a := range attachments {
		var data []byte
		if name := strings.TrimPrefix(a.URL, "/api/uploads/"); name != "" && name != a.URL {
			// 本地存储驱动：直接读 uploads/ 目录
			d, err := os.ReadFile(filepath.Join(storage.LocalRoot, filepath.Base(name)))
			if err != nil {
				continue
			}
			data = d
		} else if strings.HasPrefix(a.URL, "https://") || strings.HasPrefix(a.URL, "http://") {
			// 对象存储直传的附件：下载后写入沙箱（公开读桶；私有桶需换签名 GET）
			d, err := downloadAttachment(ctx, a.URL)
			if err != nil {
				continue
			}
			data = d
		} else {
			continue
		}
		safe := ""
		if a.Path != "" {
			safe = workspaceRelPath(a.Path)
		}
		if safe == "" {
			safe = workspaceName(a.Filename)
		}
		if safe == "" {
			safe = fallbackWorkspaceName(a.URL)
		}
		if safe == "" {
			continue
		}
		// 经沙箱驱动写入工作区（local 落宿主目录；docker/e2b 传输进沙箱）
		if err := sandbox.WriteFile(ctx, sessionID, safe, data); err != nil {
			continue
		}
		a.Path = safe
		if a.Filename == "" {
			a.Filename = filepath.Base(safe)
		}
		placed = append(placed, a)
	}
	return placed
}

// fallbackWorkspaceName 从 URL 尾段兜底生成工作区文件名（原文件名清洗后无效时）
func fallbackWorkspaceName(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return workspaceName(path.Base(u.Path))
}

// downloadAttachment 下载对象存储附件（带体积上限，读流即断）
func downloadAttachment(ctx context.Context, rawURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := attachmentHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("下载附件失败: HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, storage.MaxBytes+1))
}

// materializeBlobs blob 重物化：沙箱回收重建后，把历史消息引用的 .tool-outputs
// 归档恢复回工作区（与 materializeAttachments 同款生命周期兜底）。文件还在则
// 跳过——沙箱存活时不产生任何下载流量；无归档 URL 的裸路径行（v1.0.0 旧行）
// 无从恢复，静默跳过。失败只跳过该条，绝不阻塞对话。
func materializeBlobs(ctx context.Context, sessionID string, history []model.Message) {
	for i := range history {
		m := &history[i]
		if m.BlobRef == nil || *m.BlobRef == "" {
			continue
		}
		blobPath, archiveURL := decodeBlobRef(*m.BlobRef)
		if blobPath == "" || archiveURL == "" {
			continue
		}
		if _, err := sandbox.ReadFile(ctx, sessionID, blobPath); err == nil {
			continue // 沙箱里还在，无需重物化
		}
		data, err := fetchArchive(ctx, archiveURL)
		if err != nil {
			slog.Warn("blob 重物化取回归档失败", "session", sessionID, "path", blobPath, "err", err)
			continue
		}
		if err := sandbox.WriteFile(ctx, sessionID, blobPath, data); err != nil {
			slog.Warn("blob 重物化写回沙箱失败", "session", sessionID, "path", blobPath, "err", err)
		}
	}
}

// fetchArchive 按归档 URL 取回数据：本地驱动（/api/uploads/）直读 uploads 目录，
// 对象存储走 HTTP 下载（与附件回捞同款通路）
func fetchArchive(ctx context.Context, archiveURL string) ([]byte, error) {
	if name := strings.TrimPrefix(archiveURL, "/api/uploads/"); name != "" && name != archiveURL {
		return os.ReadFile(filepath.Join(storage.LocalRoot, filepath.Base(name)))
	}
	if strings.HasPrefix(archiveURL, "https://") || strings.HasPrefix(archiveURL, "http://") {
		return downloadAttachment(ctx, archiveURL)
	}
	return nil, fmt.Errorf("不可识别的归档 URL: %s", archiveURL)
}

// workspaceRelPath 把相对路径（如 src/components/Header.tsx）清洗为安全的工作区相对路径：
// 规范化斜杠、剥离 /workspace/ 前缀、拒绝越界（../ 与绝对路径）、对各路径段清洗非法字符；
// 保留合法的多级子目录。无效时返回空串。
func workspaceRelPath(orig string) string {
	s := strings.TrimSpace(orig)
	if s == "" {
		return ""
	}
	s = filepath.ToSlash(s)
	if s == "/workspace" {
		return ""
	}
	s = strings.TrimPrefix(s, "/workspace/")
	s = path.Clean(s)
	if s == "" || s == "." || path.IsAbs(s) || strings.HasPrefix(s, "../") {
		return ""
	}
	segments := strings.Split(s, "/")
	var cleaned []string
	for _, seg := range segments {
		cleanSeg := workspaceName(seg)
		if cleanSeg == "" {
			return ""
		}
		cleaned = append(cleaned, cleanSeg)
	}
	return strings.Join(cleaned, "/")
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

// isImageAttachment 判断附件是否为图片（基于 ContentType 或扩展名）
func isImageAttachment(a Attachment) bool {
	ct := strings.ToLower(strings.TrimSpace(a.ContentType))
	if strings.HasPrefix(ct, "image/") {
		return true
	}
	ext := strings.ToLower(filepath.Ext(a.Filename))
	switch ext {
	case ".png", ".jpg", ".jpeg", ".webp", ".gif", ".bmp", ".svg":
		return true
	}
	return false
}

// resolveAttachmentImageURL 将图片附件解析为大模型视觉接口可直接消费的 URL（Base64 Data URL 或公开 HTTP(S) URL）
func resolveAttachmentImageURL(ctx context.Context, a Attachment) (string, error) {
	if strings.HasPrefix(a.URL, "data:image/") {
		return a.URL, nil
	}

	// 本地存储驱动（/api/uploads/ URL）：直接读本地 uploads/ 目录并转为 Data URL（避免外部大模型无法访问本地相对路径）
	if name := strings.TrimPrefix(a.URL, "/api/uploads/"); name != "" && name != a.URL {
		p := filepath.Join(storage.LocalRoot, filepath.Base(name))
		data, err := os.ReadFile(p)
		if err != nil {
			return "", err
		}
		mimeType := a.ContentType
		if mimeType == "" || !strings.HasPrefix(mimeType, "image/") {
			mimeType = storage.NormalizeContentType(mime.TypeByExtension(filepath.Ext(a.Filename)))
			if !strings.HasPrefix(mimeType, "image/") {
				mimeType = http.DetectContentType(data)
			}
		}
		return fmt.Sprintf("data:%s;base64,%s", mimeType, base64.StdEncoding.EncodeToString(data)), nil
	}

	// HTTP/HTTPS 链接
	if strings.HasPrefix(a.URL, "http://") || strings.HasPrefix(a.URL, "https://") {
		// 本地回环或局域网私有地址（云端模型无法直接回连），尝试下载后转为 Data URL
		u, err := url.Parse(a.URL)
		if err == nil {
			host := strings.ToLower(u.Hostname())
			if host == "localhost" || host == "127.0.0.1" || strings.HasPrefix(host, "192.168.") || strings.HasPrefix(host, "10.") || strings.HasPrefix(host, "172.") {
				data, err := downloadAttachment(ctx, a.URL)
				if err == nil && len(data) > 0 {
					mimeType := a.ContentType
					if mimeType == "" || !strings.HasPrefix(mimeType, "image/") {
						mimeType = storage.NormalizeContentType(mime.TypeByExtension(filepath.Ext(a.Filename)))
						if !strings.HasPrefix(mimeType, "image/") {
							mimeType = http.DetectContentType(data)
						}
					}
					return fmt.Sprintf("data:%s;base64,%s", mimeType, base64.StdEncoding.EncodeToString(data)), nil
				}
			}
		}
		// 外部公网直链（如公开读 COS、云端 CDN 等），直接使用 URL
		return a.URL, nil
	}

	return "", fmt.Errorf("不支持的图片 URL 协议: %s", a.URL)
}

// buildMultimodalUserMessage 组装用户消息（兼顾沙箱工作区物化提示与多模态视觉 ContentParts）
func buildMultimodalUserMessage(ctx context.Context, userContent string, turnAttachments []Attachment, placed []Attachment) provider.ChatMessage {
	var textPrompt string
	if len(placed) > 0 {
		var b strings.Builder
		if userContent != "" {
			b.WriteString(userContent)
			b.WriteString("\n\n")
		}
		b.WriteString("[用户上传或会话归档的附件已放入工作区，可直接用 read_file 等工具按路径读取]")
		for _, a := range placed {
			target := a.Path
			if target == "" {
				target = a.Filename
			}
			fmt.Fprintf(&b, "\n- %s", target)
		}
		textPrompt = b.String()
	} else if len(turnAttachments) > 0 {
		var b strings.Builder
		if userContent != "" {
			b.WriteString(userContent)
			b.WriteString("\n\n")
		}
		b.WriteString("[用户上传的附件]")
		for _, a := range turnAttachments {
			size := ""
			if a.Size != nil {
				size = fmt.Sprintf(", %d bytes", *a.Size)
			}
			fmt.Fprintf(&b, "\n- %s (%s%s)", a.Filename, a.ContentType, size)
		}
		textPrompt = b.String()
	} else {
		textPrompt = userContent
	}

	// 提取并解析视觉图片输入
	var imageParts []provider.ContentPart
	for _, a := range turnAttachments {
		if isImageAttachment(a) {
			imgURL, err := resolveAttachmentImageURL(ctx, a)
			if err == nil && imgURL != "" {
				imageParts = append(imageParts, provider.ContentPart{
					Type: string(provider.ContentPartTypeImageURL),
					ImageURL: &provider.ImageURL{
						URL: imgURL,
					},
				})
			} else {
				slog.Warn("解析图片附件失败，退化为普通引用", "filename", a.Filename, "err", err)
			}
		}
	}

	// 无图片输入时返回纯文本 ChatMessage
	if len(imageParts) == 0 {
		return provider.ChatMessage{Role: "user", Content: textPrompt}
	}

	// 组装多模态 ContentParts：文本段 + 图片段
	var parts []provider.ContentPart
	if textPrompt != "" {
		parts = append(parts, provider.ContentPart{
			Type: string(provider.ContentPartTypeText),
			Text: textPrompt,
		})
	}
	parts = append(parts, imageParts...)

	fallbackText := textPrompt
	if fallbackText == "" {
		fallbackText = "[图片]"
	}

	return provider.ChatMessage{
		Role:         "user",
		Content:      fallbackText,
		ContentParts: parts,
	}
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
	} else if len(attachments) > 0 {
		if isImageAttachment(attachments[0]) {
			autoTitle = "[图片] " + attachments[0].Filename
		} else {
			autoTitle = "[附件] " + attachments[0].Filename
		}
		r := []rune(autoTitle)
		if len(r) > 20 {
			autoTitle = string(r[:20]) + "..."
		}
	}
	userMsgID, err := saveMessage(ctx, sessionID, "user", userContent, "", nil, "", "", attachments, nil)
	if err != nil {
		return err
	}
	emit(Event{Type: "user_message_id", ID: userMsgID})
	if (session.Title == "新会话" || session.Title == "") && autoTitle != "" {
		_ = dao.UpdateSessionTitle(context.WithoutCancel(ctx), sessionID, autoTitle)
	}

	// 2. 装配上下文：历史（不含刚落库的本条用户消息，由 handler 在落库前加载）+ 本轮用户输入
	msgs := buildContext(ctx, session, history)
	// 技能装配（阶段五）：启用技能挂载进沙箱 + Catalog 目录注入 System Prompt；
	// 任一步失败降级为无技能对话，不阻塞整轮
	ts := loadTurnSkills(ctx, session)
	if section := ts.systemPromptSection(); section != "" {
		msgs[0].Content = msgs[0].Content + "\n\n" + section
	}
	// 附件落入会话工作区（沙箱），同时构造包含视觉图片的多模态用户消息
	placed := materializeAttachments(ctx, sessionID, collectAttachments(history, attachments))
	userTurn := buildMultimodalUserMessage(ctx, userContent, attachments, placed)
	// blob 重物化：沙箱回收重建后，把历史消息引用的 .tool-outputs 归档恢复回
	// 工作区（文件还在则零流量跳过），恢复模型 read_file 取回完整输出的能力
	materializeBlobs(ctx, sessionID, history)
	msgs = append(msgs, userTurn)
	toolSchemas := tools.Schemas()
	if ts.hasCatalog() {
		// 存在可按需装载的候选才暴露 load_skill——否则模型会看到一个
		// 永远只能返回"没有这个技能"的工具，白白浪费 token 和调用轮次
		toolSchemas = append(toolSchemas, tools.LoadSkillSchema())
	}

	// Loop guard：sig(=tool+args 哈希) -> 历次输出哈希
	guard := map[string][]string{}
	lastAssistantID := ""
	turnUsage := (*provider.Usage)(nil) // 整轮各轮 usage 累计，随 done 事件下发

	// lastPrompt 上一轮实测 prompt tokens（rezone 预算判断，天然滞后一轮）
	lastPrompt := 0
	for round := 0; round < MaxToolRounds; round++ {
		// 轮内预算兜底：上一轮实测 prompt_tokens 超窗口×RezoneRatio 时，用与
		// 组装器同一套 stub 规则对内存 msgs 整体分区替换一次（in-flight 最新 step
		// 与叙事行不动）。批量一次、之后前缀只增不变；首轮不触发，
		// 由 buildContext 的分区兜底。
		if lastPrompt > 0 && int64(lastPrompt) > int64(float64(provider.ContextWindow(session.Model))*ctxpolicy.Current().RezoneRatio) {
			rezoneMsgs(msgs)
		}
		req := &provider.ChatRequest{
			Model:     session.Model,
			Messages:  msgs,
			Tools:     toolSchemas,
			MaxTokens: 32768, // 与 mirror 对齐；8192 时写文件类工具参数会被截断成半截 JSON
		}
		tf := &thinkFilter{}
		content, reasoning, calls, finish, usage, streamErr := streamOneRound(ctx, deps.Provider, req, tf, emit)
		if usage != nil {
			lastPrompt = usage.PromptTokens
		}

		// 记账先于异常分支：中途报错/截断/取消的轮次同样烧了 token，照常落账；
		// 记账本身是旁路，失败只告警不影响对话
		recordTokenUsage(ctx, session, round, usage, constants.UsagePurposeTurn)
		turnUsage = sumUsage(turnUsage, usage)
		if turnUsage != nil {
			emit(Event{Type: "round_progress", Usage: turnUsage})
		}

		// 超限参数 / 截断半截 JSON 的调用换 stub 版落库与进上下文（原文落 blob），
		// 执行仍用原始 calls 产生可自纠正的错误提示；逐调用判定，不牵连同批次正常调用
		persistCalls, rejected := prepareAssistantCalls(calls, streamErr != nil, finish == "length")

		if streamErr != nil {
			note := fmt.Sprintf("[对话因异常中止：%v]", streamErr)
			var text string
			if content != "" {
				text = content + "\n\n[回答因服务异常中断: " + streamErr.Error() + "]"
			} else {
				text = note
			}
			errID, _ := saveMessage(ctx, sessionID, "assistant", text, reasoning, persistCalls, "", "", nil,
				assistantExtras(ctx, sessionID, text, persistCalls, calls, rejected))
			emit(Event{Type: "error", Error: streamErr.Error()})
			emit(Event{Type: "done", ID: errID})
			return nil
		}

		// finish=length 时 tool_calls 是被截断的半截 JSON，不能执行；已换 stub 版
		// 落库，下轮回放不再携带非法 JSON
		if finish == "length" {
			text := content + "\n\n[模型输出被 token 上限截断]"
			id, _ := saveMessage(ctx, sessionID, "assistant", text, reasoning, persistCalls, "", "", nil,
				assistantExtras(ctx, sessionID, text, persistCalls, calls, rejected))
			emit(Event{Type: "error", Error: "回答被 token 上限截断，可重试或拆小任务"})
			emit(Event{Type: "done", ID: id})
			return nil
		}

		assistantID, err := saveMessage(ctx, sessionID, "assistant", content, reasoning, persistCalls, "", "", nil,
			assistantExtras(ctx, sessionID, content, persistCalls, calls, rejected))
		if err != nil {
			return err
		}
		lastAssistantID = assistantID
		msgs = append(msgs, provider.ChatMessage{Role: "assistant", Content: content, ToolCalls: callsOrNil(persistCalls)})

		if len(calls) == 0 || finish != "tool_calls" {
			emit(Event{Type: "done", ID: assistantID, Usage: turnUsage})
			return nil
		}

		// 3. 广播工具调用开始事件
		for _, tc := range calls {
			emit(Event{Type: "tool_call_start", ID: tc.ID, Name: tc.Function.Name, Arguments: orEmptyJSON(tc.Function.Arguments)})
		}

		// 4. load_skill 串行先行（改写 turn 内的挂载列表与 secrets 合并表，
		//    后续工具才拿得到新挂载的文件与机密），其余工具并发执行；
		//    装载成功同时广播 skill_loaded（前端同步启用列表并弹提示条）
		outputs := make([]tools.Output, len(calls))
		executed := make([]bool, len(calls))
		for i, tc := range calls {
			if tc.Function.Name == tools.LoadSkillTool {
				var loaded *Event
				outputs[i], loaded = ts.runLoadSkill(ctx, sessionID, tc)
				executed[i] = true
				if loaded != nil {
					emit(*loaded)
				}
			}
		}
		execCtx := tools.WithEnv(ctx, tools.SortedEnv(ts.envs))
		exec := deps.execTool()
		var wg sync.WaitGroup
		for i, tc := range calls {
			if executed[i] {
				continue
			}
			wg.Add(1)
			go func(i int, tc savedCall) {
				defer wg.Done()
				args, parseErr := tools.ParseArguments(tc.Function.Arguments)
				if parseErr != "" {
					logArgAnomaly(sessionID, session.Model, tc, "参数解析失败")
					outputs[i] = tools.Output{Text: parseErr}
					return
				}
				if tools.CheckArgs(tc.Function.Name, args) != "" {
					logArgAnomaly(sessionID, session.Model, tc, "参数缺失")
				}
				outputs[i] = exec(execCtx, sessionID, tc.Function.Name, args)
			}(i, tc)
		}
		wg.Wait()

		// 5. Loop Guard 记账 + 结果事件 + 工件事件 + 落库
		killed := false
		for i, tc := range calls {
			out := outputs[i]
			sig := tc.Function.Name + ":" + sigHash(tc.Function.Arguments)
			outHash := sigHash(out.Text[:min(len(out.Text), 400)])
			history := guard[sig]
			history = append(history, outHash)
			guard[sig] = history

			if len(history) >= loopHintThreshold {
				out.Text = loopHintMessage(tc.Function.Name, len(history)) + "\n\n" + out.Text
			}
			if len(history) >= loopKillThreshold && allSame(history) {
				killed = true
			}

			if tools.IsErrorResult(out.Text) {
				emit(Event{Type: "tool_call_error", ID: tc.ID, Error: out.Text})
			} else {
				emit(Event{Type: "tool_call_result", ID: tc.ID, Output: out.Text})
			}
			// 导出工件逐个广播（前端按 tool_call_id 挂到工具调用卡片上）
			for _, att := range out.Attachments {
				emit(Event{Type: "tool_artifact", ID: tc.ID, Attachments: []Attachment{att}})
			}

			if _, err := saveMessage(ctx, sessionID, "tool", out.Text, "", nil, tc.ID, tc.Function.Name, out.Attachments, toolExtras(tc, out)); err != nil {
				return err
			}
			msgs = append(msgs, provider.ChatMessage{Role: "tool", Content: out.Text, ToolCallID: tc.ID})
		}

		if killed {
			killID, _ := saveMessage(ctx, sessionID, "assistant",
				"[已停止：相同的工具调用反复返回相同结果，判定为无进展死循环]", "", nil, "", "", nil, nil)
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

// logArgAnomaly 工具参数异常的取证日志：把反序列化之前的原始 arguments（流式
// 分片按到达顺序拼接的产物）截断后落一条 Warn。API 实际送来的是空对象、零字节
// 还是残缺 JSON，在 run.log 里一眼可辨，不必事后推断（曾出现模型连发空参数的
// 塌缩，当时无现场可查）。正常调用零开销。
func logArgAnomaly(sessionID, modelName string, tc savedCall, trigger string) {
	slog.Warn("工具参数异常（原始 arguments 取证）",
		"session", sessionID,
		"model", modelName,
		"tool", tc.Function.Name,
		"trigger", trigger,
		"fragments", tc.fragCount,
		"bytes", len(tc.Function.Arguments),
		"raw_arguments", argPreview(tc.Function.Arguments),
	)
}

// argPreview 截断到 512 字节，回退到上一个 UTF-8 序列边界避免截出半个字符
func argPreview(s string) string {
	const max = 512
	if len(s) <= max {
		return s
	}
	cut := s[:max]
	for i := max - 1; i >= max-4 && i >= 0; i-- {
		if s[i] < 0x80 { // ASCII：可整字节包含
			cut = s[:i+1]
			break
		}
		if s[i] >= 0xC0 { // 多字节序列首字节：整段剔除，避免留下孤儿字节
			cut = s[:i]
			break
		}
	}
	return fmt.Sprintf("%s…(已截断，共 %d 字节)", cut, len(s))
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
