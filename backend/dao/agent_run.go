package dao

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"backend/dal/model"
	"backend/pkg/db"
	"backend/pkg/errors"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func validAgentStatus(status string) bool {
	switch status {
	case "queued", "running", "completed", "failed", "cancelled", "interrupted":
		return true
	default:
		return false
	}
}

func terminalAgentStatus(status string) bool {
	return status != "queued" && status != "running"
}

// CreateAgentRun records one child execution under an existing root session.
// The session row lock serializes creation with deletion; callers must still
// authorize the root session before admitting work into the runtime.
func CreateAgentRun(ctx context.Context, run *model.AgentRun) error {
	if run == nil || strings.TrimSpace(run.AgentID) == "" || strings.TrimSpace(run.SessionID) == "" ||
		strings.TrimSpace(run.ToolCallID) == "" || strings.TrimSpace(run.Task) == "" {
		return errors.NewMsg("Agent 缺少身份、会话、工具调用或任务")
	}
	if run.AgentID == run.SessionID || len(run.AgentID) > 36 || len(run.SessionID) > 36 ||
		len(run.ToolCallID) > 64 || utf8.RuneCountInString(run.Name) > 128 {
		return errors.NewMsg("Agent 标识或名称不合法")
	}
	if run.ParentAgentID == "" {
		run.ParentAgentID = run.SessionID
	}
	if run.WorkspaceID == "" {
		run.WorkspaceID = run.SessionID
	}
	if run.WorkspaceID != run.SessionID {
		return errors.NewMsg("子 Agent 必须共享会话工作区")
	}
	if run.Mode == "" {
		run.Mode = "read_only"
	}
	if run.Mode != "read_only" && run.Mode != "write" {
		return errors.NewMsg("不支持的 Agent 工作模式")
	}
	if run.Status == "" {
		run.Status = "queued"
	}
	if run.Status != "queued" {
		return errors.NewMsg("新 Agent 必须从 queued 状态开始")
	}
	if run.CreatedAt.IsZero() {
		run.CreatedAt = time.Now()
	}
	return db.Transition(ctx, func(tx context.Context) error {
		s := db.Ctx(tx).Session
		root, err := s.WithContext(tx).Clauses(clause.Locking{Strength: "UPDATE"}).
			Where(s.SessionID.Eq(run.SessionID)).First()
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errors.NewMsg("会话不存在")
		}
		if err != nil {
			return errors.Join(err, errors.New("查询 Agent 会话失败"))
		}
		if run.Model == "" {
			run.Model = root.Model
		}
		if utf8.RuneCountInString(run.Model) > 128 {
			return errors.NewMsg("Agent 模型名称过长")
		}
		if run.ParentAgentID != run.SessionID {
			parent, err := GetAgentRun(tx, run.SessionID, run.ParentAgentID)
			if err != nil {
				return err
			}
			if parent == nil {
				return errors.NewMsg("父 Agent 不属于此会话")
			}
		}
		if err := db.Ctx(tx).AgentRun.WithContext(tx).Create(run); err != nil {
			return errors.Join(err, errors.New("创建 Agent 记录失败"), errors.NewMsg("无法创建子任务"))
		}
		return nil
	})
}

// UpdateAgentRun updates execution state only; ancestry and workspace ownership
// are immutable. A terminal record cannot be reopened by a late worker update.
func UpdateAgentRun(ctx context.Context, agentID string, updates map[string]interface{}) error {
	if strings.TrimSpace(agentID) == "" {
		return errors.NewMsg("Agent 标识不能为空")
	}
	if len(updates) == 0 {
		return nil
	}
	for key := range updates {
		switch key {
		case "status", "result", "error", "started_at", "finished_at":
		default:
			return errors.NewMsg("不允许修改 Agent 归属或任务配置")
		}
	}
	return db.Transition(ctx, func(tx context.Context) error {
		a := db.Ctx(tx).AgentRun
		row, err := a.WithContext(tx).Clauses(clause.Locking{Strength: "UPDATE"}).Where(a.AgentID.Eq(agentID)).First()
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errors.NewMsg("Agent 不存在")
		}
		if err != nil {
			return errors.Join(err, errors.New("读取 Agent 状态失败"))
		}
		if next, exists := updates["status"]; exists {
			status, ok := next.(string)
			if !ok || !validAgentStatus(status) {
				return errors.NewMsg("无效的 Agent 状态")
			}
			if status != row.Status && (terminalAgentStatus(row.Status) || status == "queued") {
				return errors.NewMsg("Agent 状态已结束或发生变化")
			}
		}
		if _, err := a.WithContext(tx).Where(a.AgentID.Eq(agentID)).Updates(updates); err != nil {
			return errors.Join(err, errors.New("更新 Agent 记录失败"), errors.NewMsg("系统异常"))
		}
		return nil
	})
}

// ListAgentRuns returns the whole tree for a root session, oldest first.
func ListAgentRuns(ctx context.Context, sessionID string) ([]*model.AgentRun, error) {
	a := db.Ctx(ctx).AgentRun
	rows, err := a.WithContext(ctx).Where(a.SessionID.Eq(sessionID)).Order(a.CreatedAt, a.AgentID).Find()
	if err != nil {
		return nil, errors.Join(err, errors.New("查询会话 Agent 失败"), errors.NewMsg("系统异常"))
	}
	return rows, nil
}

// GetAgentRun never resolves an agent from a different root session.
func GetAgentRun(ctx context.Context, sessionID, agentID string) (*model.AgentRun, error) {
	a := db.Ctx(ctx).AgentRun
	row, err := a.WithContext(ctx).Where(a.SessionID.Eq(sessionID), a.AgentID.Eq(agentID)).First()
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, errors.Join(err, errors.New("查询 Agent 失败"), errors.NewMsg("系统异常"))
	}
	return row, nil
}

// FindAgentRunByToolCall resolves replayed spawn calls without executing them
// again. The matching unique key also protects concurrent duplicate inserts.
func FindAgentRunByToolCall(ctx context.Context, sessionID, toolCallID string) (*model.AgentRun, error) {
	a := db.Ctx(ctx).AgentRun
	row, err := a.WithContext(ctx).Where(a.SessionID.Eq(sessionID), a.ToolCallID.Eq(toolCallID)).First()
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, errors.Join(err, errors.New("查询 Agent 调用记录失败"), errors.NewMsg("系统异常"))
	}
	return row, nil
}

// InterruptAgentRuns is a single-instance startup reconciliation. It must run
// before new work is admitted; terminal rows and their results are preserved.
func InterruptAgentRuns(ctx context.Context) error {
	a := db.Ctx(ctx).AgentRun
	_, err := a.WithContext(ctx).Where(a.Status.In("queued", "running")).Updates(map[string]interface{}{
		"status": "interrupted", "error": "服务重启，子任务已中断；需要重新发起", "finished_at": time.Now(),
	})
	if err != nil {
		return errors.Join(err, errors.New("恢复 Agent 中断状态失败"))
	}
	return nil
}
