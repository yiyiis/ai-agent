package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"backend/dal/model"
	"backend/dao"
	"backend/pkg/agentctx"
	"backend/pkg/tools"

	"github.com/google/uuid"
)

const (
	DelegateAgentTool   = "delegate_agent"
	MaxConcurrentAgents = 3
	MaxAgentsPerTurn    = 12
	MaxChildRounds      = 16
	ChildTimeout        = 5 * time.Minute
	maxAgentResultRunes = 16000
)

// DelegateAgentSchema exposes bounded, synchronous delegation. Multiple calls in
// one model response execute concurrently through the ordinary tool executor.
func DelegateAgentSchema() any {
	return map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        DelegateAgentTool,
			"description": "委派独立子 Agent 处理自包含任务并等待结果。可在同轮并行委派不同任务，最多同时运行3个、累计12个。子任务共享当前工作区但不读取父对话；任务中必须明确上下文、文件范围和验收条件。read_only只允许读取文件，write允许已授权工具。按文件划分写任务，父Agent负责复查整合结果。",
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name": map[string]any{"type": "string", "description": "简短角色或任务名称"},
					"task": map[string]any{"type": "string", "description": "完整任务、必要背景、文件范围和验收条件"},
					"mode": map[string]any{"type": "string", "enum": []string{"read_only", "write"}, "default": "read_only"},
				},
				"required":             []string{"name", "task"},
				"additionalProperties": false,
			},
		},
	}
}

type agentExecution struct {
	done chan struct{}
	out  tools.Output
}

// multiAgentRuntime belongs to one root turn. It never owns a sandbox: every
// invocation continues to pass the root session ID to the existing tools.
type multiAgentRuntime struct {
	deps      Deps
	session   *model.Session
	emit      func(Event)
	slots     chan struct{}
	workspace chan struct{}
	timeout   time.Duration
	mu        sync.Mutex
	total     int
	calls     map[string]*agentExecution

	// Narrow seams allow scheduler and cancellation tests without a database.
	createRun     func(context.Context, *model.AgentRun) error
	updateRun     func(context.Context, string, map[string]interface{}) error
	findRun       func(context.Context, string, string) (*model.AgentRun, error)
	loadArtifacts func(context.Context, *model.AgentRun) []Attachment
	runChild      func(context.Context, Deps, *model.Session, string, func(Event)) error
}

func newMultiAgentRuntime(deps Deps, session *model.Session, emit func(Event)) *multiAgentRuntime {
	return &multiAgentRuntime{
		deps: deps, session: session, emit: emit,
		slots: make(chan struct{}, MaxConcurrentAgents), workspace: make(chan struct{}, 1),
		timeout: ChildTimeout, calls: map[string]*agentExecution{},
		createRun: dao.CreateAgentRun, updateRun: dao.UpdateAgentRun, findRun: dao.FindAgentRunByToolCall,
		loadArtifacts: func(ctx context.Context, row *model.AgentRun) []Attachment {
			messages, err := dao.ListMessagesBySessionID(agentctx.WithAgentID(ctx, row.AgentID), row.SessionID)
			if err != nil {
				return nil
			}
			var result []Attachment
			for _, message := range messages {
				if message.Attachments == nil {
					continue
				}
				var attachments []Attachment
				if json.Unmarshal([]byte(*message.Attachments), &attachments) == nil {
					result = append(result, attachments...)
				}
			}
			return result
		},
		runChild: func(ctx context.Context, deps Deps, session *model.Session, task string, emit func(Event)) error {
			return run(ctx, deps, session, nil, task, nil, emit, false)
		},
	}
}

// withWorkspace serializes complete tools, including read-modify-write and shell
// commands. Waiting is cancellable. Never hold this lock while delegating.
func (r *multiAgentRuntime) withWorkspace(ctx context.Context, fn func() tools.Output) tools.Output {
	if err := ctx.Err(); err != nil {
		return tools.Output{Text: "[执行失败] " + err.Error()}
	}
	select {
	case <-ctx.Done():
		return tools.Output{Text: "[执行失败] " + ctx.Err().Error()}
	case r.workspace <- struct{}{}:
	}
	defer func() { <-r.workspace }()
	if err := ctx.Err(); err != nil {
		return tools.Output{Text: "[执行失败] " + err.Error()}
	}
	return fn()
}

func (r *multiAgentRuntime) delegate(ctx context.Context, toolCallID string, args map[string]any, skills *turnSkills, schemas []any) (out tools.Output) {
	if agentctx.AgentID(ctx) != "" {
		return tools.Output{Text: "[执行失败] 子 Agent 不允许再次委派任务。"}
	}
	name, _ := args["name"].(string)
	task, _ := args["task"].(string)
	mode, modeOK := args["mode"].(string)
	name, task = strings.TrimSpace(name), strings.TrimSpace(task)
	if args["mode"] != nil && !modeOK {
		return tools.Output{Text: "[参数解析失败] mode 必须为 read_only 或 write。"}
	}
	if args["mode"] == nil || mode == "" {
		mode = "read_only"
	}
	if name == "" || task == "" || utf8.RuneCountInString(name) > 128 || len(task) > 64000 || toolCallID == "" || len(toolCallID) > 64 {
		return tools.Output{Text: "[参数解析失败] name/task/tool_call_id 必填；name 最多128字符，task最多64000字节。"}
	}
	if mode != "read_only" && mode != "write" {
		return tools.Output{Text: "[参数解析失败] mode 必须为 read_only 或 write。"}
	}
	if err := ctx.Err(); err != nil {
		return tools.Output{Text: "[执行失败] " + err.Error()}
	}

	// Reserve a call before any asynchronous work. Duplicate calls wait for the
	// original result and do not consume the cumulative budget a second time.
	r.mu.Lock()
	if previous := r.calls[toolCallID]; previous != nil {
		r.mu.Unlock()
		select {
		case <-ctx.Done():
			return tools.Output{Text: "[执行失败] " + ctx.Err().Error()}
		case <-previous.done:
			return previous.out
		}
	}
	if r.total >= MaxAgentsPerTurn {
		r.mu.Unlock()
		return tools.Output{Text: fmt.Sprintf("[执行失败] 当前主轮次已达 %d 个子任务上限。", MaxAgentsPerTurn)}
	}
	execution := &agentExecution{done: make(chan struct{})}
	r.calls[toolCallID] = execution
	r.total++
	r.mu.Unlock()
	defer func() { execution.out = out; close(execution.done) }()
	created := false
	defer func() {
		if !created {
			r.mu.Lock()
			r.total--
			r.mu.Unlock()
		}
	}()

	childCtx, cancel := context.WithTimeout(agentctx.WithAgentID(ctx, uuid.NewString()), r.timeout)
	defer cancel()
	previous, err := r.findRun(childCtx, r.session.SessionID, toolCallID)
	if err != nil {
		return tools.Output{Text: "[执行失败] 查询委派任务失败：" + err.Error()}
	}
	if previous != nil {
		if previous.Status != "queued" && previous.Status != "running" {
			return agentOutput(previous, r.loadArtifacts(childCtx, previous))
		}
		return existingAgentOutput(previous)
	}

	now := time.Now()
	row := &model.AgentRun{
		AgentID: agentctx.AgentID(childCtx), SessionID: r.session.SessionID,
		ParentAgentID: r.session.SessionID, WorkspaceID: r.session.SessionID,
		ToolCallID: toolCallID, Name: name, Task: task, Mode: mode, Model: r.session.Model,
		Status: "queued", CreatedAt: now,
	}
	if err := r.createRun(childCtx, row); err != nil {
		return tools.Output{Text: "[执行失败] 创建委派任务失败：" + err.Error()}
	}
	created = true
	r.emitStatus(row)

	var result strings.Builder
	var childError string
	sawDone := false
	var attachments []Attachment
	seenAttachments := map[string]bool{}
	// Persist a terminal state even when the root is cancelled or the worker
	// panics. No detached worker remains after this synchronous call returns.
	defer func() {
		if recovered := recover(); recovered != nil {
			childError = fmt.Sprintf("子 Agent 执行异常：%v", recovered)
			slog.Error("子 Agent panic", "agent_id", row.AgentID, "panic", recovered)
		}
		row.Status = "completed"
		if childCtx.Err() != nil {
			row.Status = "cancelled"
			childError = childCtx.Err().Error()
		} else if childError != "" {
			row.Status = "failed"
		}
		finalText := boundedAgentText(result.String())
		row.Result, row.Error = finalText, boundedAgentText(childError)
		finished := time.Now()
		row.FinishedAt = &finished
		persistCtx, persistCancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		persistErr := r.updateRun(persistCtx, row.AgentID, map[string]interface{}{
			"status": row.Status, "result": finalText, "error": row.Error, "finished_at": finished,
		})
		persistCancel()
		if persistErr != nil {
			row.Status = "failed"
			row.Error = "保存子 Agent 终态失败：" + persistErr.Error()
			slog.Error("保存子 Agent 终态失败", "agent_id", row.AgentID, "err", persistErr)
		}
		r.emitStatus(row)
		out = agentOutput(row, attachments)
	}()

	select {
	case <-childCtx.Done():
		return tools.Output{}
	case r.slots <- struct{}{}:
	}
	defer func() { <-r.slots }()
	if childCtx.Err() != nil {
		return tools.Output{}
	}
	started := time.Now()
	row.Status, row.StartedAt = "running", &started
	if err := r.updateRun(childCtx, row.AgentID, map[string]interface{}{"status": "running", "started_at": started}); err != nil {
		childError = "保存子 Agent 运行状态失败：" + err.Error()
		return tools.Output{}
	}
	r.emitStatus(row)

	childDeps := r.deps
	childDeps.multi, childDeps.child, childDeps.childMode = r, true, mode
	childDeps.inheritedSkills, childDeps.inheritedSchemas = cloneTurnSkills(skills), filterChildSchemas(schemas, mode)
	childDeps.maxRounds = MaxChildRounds
	childSession := *r.session
	childSession.Title = "子任务：" + name // A child must never rename its root chat.
	childSession.SystemPrompt += "\n\n## 子 Agent 执行约定\n你处理父 Agent 委派的独立任务。你和父 Agent 共享工作区，但你没有父对话历史。仅执行下面明确委派的范围；不要覆盖其他 Agent 的修改。禁止再次委派、装载新技能或直接询问用户；需要用户决定时把问题与原因返回父 Agent。完成后报告结果、修改文件、验证及限制，由父 Agent 复查。当前权限模式：" + mode + "。"
	var eventMu sync.Mutex
	childEmit := func(ev Event) {
		eventMu.Lock()
		defer eventMu.Unlock()
		switch ev.Type {
		case "delta":
			result.WriteString(ev.Content)
		case "tool_call_start":
			result.Reset() // Only the final assistant round is the task result.
		case "error":
			childError = ev.Error
			if childError == "" {
				childError = "子 Agent 报告执行失败"
			}
		case "done":
			sawDone = true
		case "tool_artifact":
			for _, a := range ev.Attachments {
				key := a.URL + "\x00" + a.Path + "\x00" + a.Filename
				if !seenAttachments[key] {
					attachments = append(attachments, a)
					seenAttachments[key] = true
				}
			}
		}
		copyEvent := ev
		if ev.Usage != nil {
			usage := *ev.Usage
			copyEvent.Usage = &usage
		}
		copyRow := *row
		// Child deltas stay in their transcript; do not evict root events from
		// the bounded reconnect buffer with every parallel worker token.
		if ev.Type != "delta" && ev.Type != "round_progress" {
			r.emit(Event{Type: "agent_event", AgentID: row.AgentID, Agent: &copyRow, ChildEvent: &copyEvent})
		}
	}
	if err := r.runChild(childCtx, childDeps, &childSession, task, childEmit); err != nil {
		childError = err.Error()
	} else if !sawDone && childError == "" {
		childError = "子 Agent 未返回完成事件，无法确认任务结束。"
	}
	return tools.Output{}
}

func (r *multiAgentRuntime) emitStatus(row *model.AgentRun) {
	copyRow := *row
	r.emit(Event{Type: "agent_status", AgentID: row.AgentID, Agent: &copyRow})
}

func boundedAgentText(s string) string {
	runes := []rune(s)
	if len(runes) <= maxAgentResultRunes {
		return s
	}
	return string(runes[:maxAgentResultRunes]) + "\n[结果过长，已截断；完整消息可通过子 Agent 详情查看]"
}

func agentOutput(row *model.AgentRun, attachments []Attachment) tools.Output {
	b, _ := json.Marshal(map[string]any{
		"agent_id": row.AgentID, "status": row.Status, "result": row.Result,
		"error": row.Error, "attachments": attachments,
	})
	prefix := ""
	if row.Status != "completed" {
		prefix = "[执行失败] "
	}
	return tools.Output{Text: prefix + string(b), Attachments: attachments}
}

func existingAgentOutput(row *model.AgentRun) tools.Output {
	if row.Status != "queued" && row.Status != "running" {
		return agentOutput(row, nil)
	}
	copyRow := *row
	copyRow.Error = "此工具调用已有未结束任务；为避免重复执行副作用，不会再次启动。"
	return agentOutput(&copyRow, nil)
}

func toolSchemaName(schema any) string {
	object, ok := schema.(map[string]any)
	if !ok {
		return ""
	}
	function, ok := object["function"].(map[string]any)
	if !ok {
		return ""
	}
	name, _ := function["name"].(string)
	return name
}

func filterChildSchemas(schemas []any, mode string) []any {
	out := make([]any, 0, len(schemas))
	for _, schema := range schemas {
		name := toolSchemaName(schema)
		if name == "" || name == DelegateAgentTool || name == tools.LoadSkillTool || name == "request_input" {
			continue
		}
		if mode == "read_only" && name != "read_file" {
			continue
		}
		out = append(out, schema)
	}
	return out
}

// childAllowedTool is enforced before dispatch as well as in schemas: a model
// can emit a tool that was never advertised, including a previously known MCP.
func childAllowedTool(deps Deps, name string) bool {
	if !deps.child {
		return true
	}
	for _, schema := range filterChildSchemas(deps.inheritedSchemas, deps.childMode) {
		if toolSchemaName(schema) == name {
			return true
		}
	}
	return false
}

func cloneTurnSkills(source *turnSkills) *turnSkills {
	clone := &turnSkills{enabledByName: map[string]string{}, catalogByName: map[string]catalogItem{}, envs: map[string]string{}}
	if source == nil {
		return clone
	}
	clone.enabled = append(clone.enabled, source.enabled...)
	for k, v := range source.enabledByName {
		clone.enabledByName[k] = v
	}
	for k, v := range source.envs {
		clone.envs[k] = v
	}
	// No catalog or autoLoaded state: child workers cannot load new skills.
	return clone
}
