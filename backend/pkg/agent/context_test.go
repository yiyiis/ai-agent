package agent

import (
	"strings"
	"testing"

	"backend/dal/model"
)

// 中断轮会在库里留下"assistant 带 tool_calls 但缺部分/全部 tool 结果"的历史，
// buildContext 必须合成占位结果，否则上游以 400 拒绝整轮请求
func TestBuildContextRepairsDanglingToolCalls(t *testing.T) {
	session := &model.Session{SessionID: "s", Model: "test-model"}
	callsJSON := `[{"id":"a","type":"function","function":{"name":"bash","arguments":"{\"command\":\"ls -l\"}"}},` +
		`{"id":"b","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"x\"}"}}]`
	history := []model.Message{
		{Role: "user", Content: "go"},
		{Role: "assistant", ToolCalls: strPtr(callsJSON)}, // b 的结果因中断缺失
		{Role: "tool", Content: "ok-a", ToolCallID: strPtr("a")},
		{Role: "user", Content: "again"},
		{Role: "tool", Content: "orphan", ToolCallID: strPtr("zz")}, // 孤儿 tool 行，应被丢弃
		{Role: "assistant", Content: "hi"},
	}

	msgs := buildContext(session, history)

	var seq []string
	for _, m := range msgs {
		tag := m.Role
		if m.Role == "tool" {
			tag += ":" + m.ToolCallID
		}
		seq = append(seq, tag)
	}
	want := []string{"system", "user", "assistant", "tool:a", "tool:b", "user", "assistant"}
	if len(seq) != len(want) {
		t.Fatalf("消息序列长度 %d, 期望 %d: %v", len(seq), len(want), seq)
	}
	for i := range want {
		if seq[i] != want[i] {
			t.Fatalf("消息序列第 %d 位为 %s, 期望 %s\n完整序列: %v", i, seq[i], want[i], seq)
		}
	}

	var synthetic bool
	for _, m := range msgs {
		if m.Role == "tool" && m.ToolCallID == "b" {
			synthetic = strings.Contains(m.Content, "未执行")
		}
	}
	if !synthetic {
		t.Fatal("悬挂调用 b 应被合成为 [未执行] 占位结果")
	}
}
