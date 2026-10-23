package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"backend/dal/model"
	"backend/dao"
	"backend/pkg/human"
	"backend/pkg/tools"
)

// Channels only wake the worker. Answers and pending requests live in messages.
var inputSignals = struct {
	sync.Mutex
	waiters map[string]chan struct{}
}{waiters: map[string]chan struct{}{}}

func NotifyInput(sessionID string) {
	inputSignals.Lock()
	defer inputSignals.Unlock()
	if ch := inputSignals.waiters[sessionID]; ch != nil {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func hasInputCall(calls []savedCall) bool {
	for _, c := range calls {
		if c.Function.Name == human.ToolName && c.ID != "" {
			if _, err := human.Parse(c.Function.Arguments); err == nil {
				return true
			}
		}
	}
	return false
}

func waitInputs(ctx context.Context, sessionID, messageID string, emit func(Event)) ([]model.Message, error) {
	defer func() {
		if ctx.Err() != nil {
			_ = dao.StopInput(context.WithoutCancel(ctx), sessionID)
		}
	}()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	ch := make(chan struct{}, 1)
	inputSignals.Lock()
	inputSignals.waiters[sessionID] = ch
	inputSignals.Unlock()
	defer func() {
		inputSignals.Lock()
		delete(inputSignals.waiters, sessionID)
		inputSignals.Unlock()
	}()
	if err := dao.PrepareInputBatch(ctx, sessionID, messageID); err != nil {
		return nil, err
	}
	messages, err := dao.ListMessagesBySessionID(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	b := human.Active(messages)
	if b == nil || b.MessageID != messageID {
		emit(Event{Type: "done"})
		return nil, context.Canceled
	}
	// Polling is a fallback for a committed response whose notification was lost.
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	shown := map[string]bool{}
	for {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		results, err := dao.ReadInputResults(ctx, sessionID, b)
		if err != nil {
			return nil, err
		}
		for _, result := range results {
			if result.MessageID == human.StopID(sessionID, messageID) {
				emit(Event{Type: "done", ID: result.MessageID})
				return nil, context.Canceled
			}
			if result.ToolCallID != nil && result.MessageID == human.ResultID(sessionID, messageID, *result.ToolCallID) {
				b.Results[*result.ToolCallID] = *result
			}
		}
		complete := true
		for _, request := range b.Requests {
			if _, ok := b.Results[request.CallID]; ok {
				continue
			}
			complete = false
			if !shown[request.CallID] {
				r := request
				emit(Event{Type: "input_required", ID: r.CallID, Input: &r})
				shown[r.CallID] = true
			}
		}
		// Broadcast answered requests immediately, including multi-question batches.
		for _, c := range b.Calls {
			if result, ok := b.Results[c.ID]; ok && !shown["result:"+c.ID] {
				emit(inputResultEvent(c.ID, result.Content))
				shown["result:"+c.ID] = true
			}
		}
		if complete {
			results := make([]model.Message, 0, len(b.Calls))
			for _, c := range b.Calls {
				m, ok := b.Results[c.ID]
				if !ok {
					return nil, fmt.Errorf("输入批次缺少工具结果 %s", c.ID)
				}
				results = append(results, m)
			}
			return results, nil
		}
		select {
		case <-ctx.Done():
		case <-ch:
		case <-ticker.C:
		}
	}
}

// ResumeInput is used only after losing the original worker. Replay the current
// turn for SSE clients, complete the question, then enter the shared model loop.
func ResumeInput(ctx context.Context, deps Deps, session *model.Session, emit func(Event)) error {
	history, err := dao.ListMessagesBySessionID(ctx, session.SessionID)
	if err != nil {
		return err
	}
	b := human.Active(history)
	if b == nil {
		return nil
	}
	emit(Event{Type: "stream_reset"})
	replayInputTurn(history, emit)
	if _, err := waitInputs(ctx, session.SessionID, b.MessageID, emit); err != nil {
		return err
	}
	history, err = dao.ListMessagesBySessionID(ctx, session.SessionID)
	if err != nil {
		return err
	}
	if human.Active(history) == nil || ctx.Err() != nil {
		return nil // stopped while the answer was being loaded
	}
	return run(ctx, deps, session, history, "", nil, emit, true)
}

func replayInputTurn(messages []model.Message, emit func(Event)) {
	start := 0
	for i, m := range messages {
		if m.Role == "user" {
			start = i + 1
		}
	}
	for _, m := range messages[start:] {
		if m.Role == "assistant" {
			emit(Event{Type: "delta", Content: m.Content, Reasoning: valueOrEmpty(m.Reasoning)})
			var calls []savedCall
			if m.ToolCalls != nil {
				_ = json.Unmarshal([]byte(*m.ToolCalls), &calls)
			}
			for _, c := range calls {
				emit(Event{Type: "tool_call_start", ID: c.ID, Name: c.Function.Name, Arguments: c.Function.Arguments})
			}
		} else if m.Role == "tool" && m.ToolCallID != nil {
			emit(inputResultEvent(*m.ToolCallID, m.Content))
			var attachments []Attachment
			if m.Attachments != nil {
				_ = json.Unmarshal([]byte(*m.Attachments), &attachments)
			}
			for _, attachment := range attachments {
				emit(Event{Type: "tool_artifact", ID: *m.ToolCallID, Attachments: []Attachment{attachment}})
			}
		}
	}
}

func inputResultEvent(callID, content string) Event {
	if tools.IsErrorResult(content) || strings.HasPrefix(content, "[未执行]") {
		return Event{Type: "tool_call_error", ID: callID, Error: content}
	}
	return Event{Type: "tool_call_result", ID: callID, Output: content}
}

func valueOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func restoredGuard(history []model.Message) map[string][]string {
	guard := map[string][]string{}
	calls := map[string]savedCall{}
	for _, m := range history {
		switch m.Role {
		case "user":
			guard, calls = map[string][]string{}, map[string]savedCall{}
		case "assistant":
			var batch []savedCall
			if m.ToolCalls != nil {
				_ = json.Unmarshal([]byte(*m.ToolCalls), &batch)
			}
			for _, c := range batch {
				calls[c.ID] = c
			}
		case "tool":
			if m.ToolCallID == nil {
				continue
			}
			c, ok := calls[*m.ToolCallID]
			if !ok || c.Function.Name == human.ToolName {
				continue
			}
			key := c.Function.Name + ":" + sigHash(c.Function.Arguments)
			content := m.Content
			if strings.HasPrefix(content, "[未执行]") {
				continue // mixed request_input siblings were not executed
			}
			if strings.HasPrefix(content, "[Agent Guard]") {
				if _, original, ok := strings.Cut(content, "\n\n"); ok {
					content = original
				}
			}
			guard[key] = append(guard[key], sigHash(content[:min(len(content), 400)]))
		}
	}
	return guard
}
