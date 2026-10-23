package dao

import (
	"context"
	"encoding/json"
	"strings"

	"backend/dal/model"
	"backend/pkg/db"
	"backend/pkg/errors"
	"backend/pkg/human"
	"gorm.io/gorm/clause"
)

// WithInputLock serializes answers and stop for a session in the database.
// The result also has a deterministic unique message_id as a second guard.
func WithInputLock(ctx context.Context, sessionID string, fn func(context.Context, []model.Message) error) error {
	return db.Transition(ctx, func(tx context.Context) error {
		s := db.Ctx(tx).Session
		if _, err := s.WithContext(tx).Where(s.SessionID.Eq(sessionID)).
			Clauses(clause.Locking{Strength: "UPDATE"}).First(); err != nil {
			return err
		}
		messages, err := ListMessagesBySessionID(tx, sessionID)
		if err != nil {
			return err
		}
		return fn(tx, messages)
	})
}

func inputResult(sessionID, messageID, callID, name, content string) *model.Message {
	return &model.Message{
		MessageID: human.ResultID(sessionID, messageID, callID), SessionID: sessionID,
		Role: "tool", ToolCallID: &callID, Name: &name, Content: content,
		TokensFull: int32((len(content) + 1) / 2),
	}
}

// ReadInputResults polls only the small response rows and terminal marker,
// avoiding repeated reads of large historical tool arguments while waiting.
func ReadInputResults(ctx context.Context, sessionID string, batch *human.Batch) ([]*model.Message, error) {
	ids := []string{human.StopID(sessionID, batch.MessageID)}
	for _, c := range batch.Calls {
		ids = append(ids, human.ResultID(sessionID, batch.MessageID, c.ID))
	}
	m := db.Ctx(ctx).Message
	return m.WithContext(ctx).Where(m.SessionID.Eq(sessionID), m.MessageID.In(ids...)).Order(m.ID).Find()
}

// AnswerInput accepts only one answer to an active, valid request.
// Identical retries return the saved response; conflicting retries are rejected.
func AnswerInput(ctx context.Context, sessionID, messageID, callID, action, answer string) (*model.Message, error) {
	answer = strings.TrimSpace(answer)
	response := human.Response{Status: "answered", Answer: answer}
	switch action {
	case "answer":
		if answer == "" || len(answer) > 16*1024 {
			return nil, errors.NewMsg("回答不能为空，且不能超过 16KB")
		}
	case "skip":
		response = human.Response{Status: "cancelled", Action: "skip"}
	default:
		return nil, errors.NewMsg("无效的回答操作")
	}
	raw, _ := json.Marshal(response)
	var result *model.Message
	err := WithInputLock(ctx, sessionID, func(tx context.Context, messages []model.Message) error {
		// Check saved answers before active-state validation: retries may arrive
		// after the model has already advanced to another assistant message.
		id := human.ResultID(sessionID, messageID, callID)
		for _, m := range messages {
			if m.MessageID == id {
				if m.Content != string(raw) {
					return errors.NewMsg("该问题已在其他页面回答或取消，请刷新查看")
				}
				result = &m
				return nil
			}
		}
		b := human.Active(messages)
		if b == nil || b.MessageID != messageID {
			return errors.NewMsg("该问题已结束或不存在")
		}
		for _, r := range b.Requests {
			if r.CallID == callID {
				if _, exists := b.Results[callID]; exists {
					return errors.NewMsg("该问题已回答")
				}
				result = inputResult(sessionID, messageID, callID, human.ToolName, string(raw))
				return CreateMessage(tx, result)
			}
		}
		return errors.NewMsg("该问题不存在")
	})
	return result, err
}

// PrepareInputBatch closes every non-interactive sibling without executing it.
// This barrier makes restart recovery safe even if a provider mixes tools.
func PrepareInputBatch(ctx context.Context, sessionID, messageID string) error {
	return WithInputLock(ctx, sessionID, func(tx context.Context, messages []model.Message) error {
		b := human.Active(messages)
		if b == nil || b.MessageID != messageID {
			return errors.NewMsg("等待输入的轮次已结束")
		}
		for _, c := range b.Calls {
			if _, ok := b.Results[c.ID]; ok {
				continue
			}
			if c.Function.Name == human.ToolName {
				if _, err := human.Parse(c.Function.Arguments); err == nil {
					continue
				}
			}
			text := "[未执行] 本批需要用户输入，此调用尚未执行；收到回答后如仍需要，请重新调用。"
			if c.Function.Name == human.ToolName {
				text = "[参数解析失败] request_input 问题格式不合法，请重新生成。"
			}
			if err := CreateMessage(tx, inputResult(sessionID, messageID, c.ID, c.Function.Name, text)); err != nil {
				return err
			}
		}
		return nil
	})
}

// StopInput records cancellation plus a terminal assistant note atomically.
// It also works after a restart when no in-memory Turn exists.
func StopInput(ctx context.Context, sessionID string) error {
	return WithInputLock(ctx, sessionID, func(tx context.Context, messages []model.Message) error {
		b := human.Active(messages)
		if b == nil {
			return nil
		}
		for _, c := range b.Calls {
			if _, ok := b.Results[c.ID]; ok {
				continue
			}
			content := "[未执行] 用户已停止任务。"
			if c.Function.Name == human.ToolName {
				content = `{"status":"cancelled","action":"stop"}`
			}
			if err := CreateMessage(tx, inputResult(sessionID, b.MessageID, c.ID, c.Function.Name, content)); err != nil {
				return err
			}
		}
		return CreateMessage(tx, &model.Message{
			MessageID: human.StopID(sessionID, b.MessageID),
			SessionID: sessionID, Role: "assistant", Content: "[已停止：用户停止了任务]",
		})
	})
}
