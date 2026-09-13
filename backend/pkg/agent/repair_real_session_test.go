//go:build e2e

// 一次性校验（可留作回归）：对被中断轮污染的真实会话跑 buildContext，
// 确认回放序列满足"每个 tool_calls 都有紧跟结果"的协议要求。
package agent

import (
	"context"
	"os"
	"testing"

	"backend/config"
	"backend/dao"
	"backend/dal/model"
	"backend/pkg/db"
)

func TestBuildContextRepairsRealPoisonedSession(t *testing.T) {
	confPath := "../../etc/config.local.yaml"
	if _, err := os.Stat(confPath); err != nil {
		confPath = "../../etc/config.yaml"
	}
	conf := config.LoadConfig(confPath)
	db.InitDb(conf.DbConf)
	ctx := db.WithContext(context.Background())

	sid := "5ec0d8c9-7224-4ce0-ad05-8437bc399834"
	history, err := dao.ListMessagesBySessionID(ctx, sid)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) == 0 {
		t.Skip("目标会话已不存在（数据被清理）")
	}
	msgs := buildContext(&model.Session{SessionID: sid, Model: conf.LLM.DefaultModel}, history)

	var pending map[string]bool
	for i, m := range msgs {
		switch m.Role {
		case "assistant":
			if len(pending) > 0 {
				t.Fatalf("第 %d 条 assistant 前仍有悬挂调用: %v", i, keysOfBool(pending))
			}
			pending = map[string]bool{}
			if calls, ok := m.ToolCalls.([]savedCall); ok {
				for _, c := range calls {
					pending[c.ID] = true
				}
			}
		case "tool":
			if !pending[m.ToolCallID] {
				t.Fatalf("第 %d 条 tool(%s) 没有对应的待答调用", i, m.ToolCallID)
			}
			delete(pending, m.ToolCallID)
		}
	}
	if len(pending) > 0 {
		t.Fatalf("结尾仍有悬挂调用: %v", keysOfBool(pending))
	}
	t.Logf("✅ %d 条消息回放序列协议完整（含合成的中断占位）", len(msgs))
}

func keysOfBool(m map[string]bool) []string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}

