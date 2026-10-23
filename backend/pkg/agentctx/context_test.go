package agentctx

import (
	"context"
	"testing"
)

func TestAgentIdentityIsScopedAndSurvivesCancellationDetach(t *testing.T) {
	root := context.Background()
	child, cancel := context.WithCancel(WithAgentID(root, "child"))
	cancel()
	if AgentID(root) != "" || AgentID(context.WithoutCancel(child)) != "child" {
		t.Fatal("root and child message ownership must remain distinct after cancellation")
	}
	if AgentID(WithAgentID(child, "")) != "" || AgentID(child) != "child" {
		t.Fatal("overriding a derived context must not alter its parent")
	}
}
