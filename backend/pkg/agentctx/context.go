// Package agentctx carries message-stream ownership independently of the shared
// session and workspace. An empty ID denotes the main agent for compatibility.
package agentctx

import "context"

type agentIDKey struct{}

func WithAgentID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, agentIDKey{}, id)
}

func AgentID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(agentIDKey{}).(string)
	return id
}
