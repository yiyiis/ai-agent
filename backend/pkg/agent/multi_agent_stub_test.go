package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDelegationColdContextRetainsTaskIdentityWithoutReplayAdvice(t *testing.T) {
	data, _ := json.Marshal(map[string]any{"agent_id": "child", "status": "completed", "result": strings.Repeat("结果", 1500)})
	stub := stubForToolResult(DelegateAgentTool, `{}`, string(data))
	var result map[string]any
	if err := json.Unmarshal([]byte(stub), &result); err != nil {
		t.Fatal(err)
	}
	if result["agent_id"] != "child" || result["status"] != "completed" || strings.Contains(stub, "需要时重新调用") {
		t.Fatalf("unsafe delegation stub: %s", stub)
	}
	if len([]rune(result["result"].(string))) > 2100 {
		t.Fatal("cold delegation result is unbounded")
	}
}
