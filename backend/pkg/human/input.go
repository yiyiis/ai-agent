// Package human defines durable request_input state derived solely from messages.
package human

import (
	"encoding/json"
	"fmt"
	"strings"

	"backend/dal/model"
	"github.com/google/uuid"
)

const ToolName = "request_input"

type Question struct {
	Question string   `json:"question"`
	Options  []string `json:"options,omitempty"`
}

type Call struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type Request struct {
	MessageID string `json:"message_id"`
	CallID    string `json:"tool_call_id"`
	Question
}

type Response struct {
	Status string `json:"status"` // answered | cancelled
	Answer string `json:"answer,omitempty"`
	Action string `json:"action,omitempty"` // skip | stop
}

func Parse(raw string) (Question, error) {
	var q Question
	if err := json.Unmarshal([]byte(raw), &q); err != nil {
		return q, fmt.Errorf("request_input 参数必须是合法的问题对象")
	}
	q.Question = strings.TrimSpace(q.Question)
	if q.Question == "" || len([]rune(q.Question)) > 4000 || len(q.Options) > 8 {
		return q, fmt.Errorf("question 必须为 1–4000 字，options 最多 8 项")
	}
	for _, option := range q.Options {
		if strings.TrimSpace(option) == "" || len([]rune(option)) > 500 {
			return q, fmt.Errorf("每个选项必须为 1–500 字")
		}
	}
	return q, nil
}

func Schema() any {
	return map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        ToolName,
			"description": "需要用户补充必要信息或选择方案时提问并等待回答。单独调用此工具，不要与其他工具放在同一批。用户可以选择选项或自由输入；跳过表示未获得答案，不能假定用户同意。",
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"question": map[string]any{"type": "string", "description": "清晰、完整的问题", "maxLength": 4000},
					"options":  map[string]any{"type": "array", "items": map[string]any{"type": "string", "maxLength": 500}, "maxItems": 8},
				},
				"required": []string{"question"},
			},
		},
	}
}

// Batch is the latest assistant tool batch awaiting input or continuation.
// A subsequent assistant/user message closes it, including a durable stop note.
type Batch struct {
	MessageID string
	Calls     []Call
	Requests  []Request
	Results   map[string]model.Message
	Rounds    int
}

func Active(messages []model.Message) *Batch {
	var batch *Batch
	rounds := 0
	for _, m := range messages {
		switch m.Role {
		case "user":
			batch, rounds = nil, 0
		case "assistant":
			rounds++
			batch = nil
			if m.ToolCalls == nil || strings.Contains(m.Content, "[模型输出被 token 上限截断]") ||
				strings.Contains(m.Content, "[回答因服务异常中断:") || strings.HasPrefix(m.Content, "[对话因异常中止:") ||
				strings.HasPrefix(m.Content, "[对话因异常中止：") {
				continue
			}
			candidate := &Batch{MessageID: m.MessageID, Rounds: rounds, Results: map[string]model.Message{}}
			if json.Unmarshal([]byte(*m.ToolCalls), &candidate.Calls) != nil {
				continue
			}
			for _, c := range candidate.Calls {
				if c.Function.Name == ToolName && c.ID != "" {
					if q, err := Parse(c.Function.Arguments); err == nil {
						candidate.Requests = append(candidate.Requests, Request{MessageID: m.MessageID, CallID: c.ID, Question: q})
					}
				}
			}
			if len(candidate.Requests) > 0 {
				batch = candidate
			}
		case "tool":
			if batch != nil && m.ToolCallID != nil {
				batch.Results[*m.ToolCallID] = m
			}
		}
	}
	return batch
}

// ResultID uses the existing unique messages.message_id constraint. Scope by
// assistant message as providers may reuse call IDs across model responses.
func ResultID(sessionID, messageID, callID string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(sessionID+"\x00"+messageID+"\x00"+callID)).String()
}

func StopID(sessionID, messageID string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("input-stop\x00"+sessionID+"\x00"+messageID)).String()
}

func Pending(messages []model.Message) []Request {
	var pending []Request
	if b := Active(messages); b != nil {
		for _, r := range b.Requests {
			if _, ok := b.Results[r.CallID]; !ok {
				pending = append(pending, r)
			}
		}
	}
	return pending
}
