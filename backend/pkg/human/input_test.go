package human

import (
	"testing"

	"backend/dal/model"
)

func TestPendingLifecycle(t *testing.T) {
	calls := `[{"id":"q","type":"function","function":{"name":"request_input","arguments":"{\"question\":\"格式？\",\"options\":[\"PDF\",\"Word\"]}"}}]`
	history := []model.Message{{Role: "user", Content: "导出"}, {Role: "assistant", MessageID: "a", ToolCalls: &calls}}
	if len(Pending(history)) != 1 || Active(history).Rounds != 1 {
		t.Fatal("must restore unanswered question")
	}
	id := "q"
	history = append(history, model.Message{Role: "tool", ToolCallID: &id, Content: `{"status":"cancelled","action":"skip"}`})
	if len(Pending(history)) != 0 || Active(history) == nil {
		t.Fatal("skip finishes question but still permits continuation")
	}
	history = append(history, model.Message{Role: "assistant", Content: "已停止"})
	if Active(history) != nil {
		t.Fatal("terminal assistant closes the checkpoint")
	}
}

func TestInvalidInputNeverWaits(t *testing.T) {
	for _, raw := range []string{`null`, `[]`, `{}`, `{"question":""}`, `{"question":"ok","options":[null]}`, `{"question":"partial`} {
		if _, err := Parse(raw); err == nil {
			t.Fatalf("accepted invalid input %s", raw)
		}
	}
	if ResultID("s", "a", "c") == ResultID("s", "b", "c") {
		t.Fatal("reused provider call IDs must be scoped by assistant message")
	}
}
