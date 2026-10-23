package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"backend/dal/model"
	"backend/dao"
	"backend/internal/inputtestdb"
	"backend/pkg/human"
	"backend/pkg/provider"
	"backend/pkg/tools"
)

func inputSession() *model.Session {
	return &model.Session{SessionID: "session", Title: "existing", Model: "test-model"}
}

func TestInputRunBlocksUntilPersistedAnswer(t *testing.T) {
	ctx, store := inputtestdb.New(t)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	p := argumentScript(human.ToolName, `{"question":"格式？","options":["PDF","Word"]}`, "tool_calls")
	required := make(chan human.Request, 1)
	finished := make(chan error, 1)
	go func() {
		finished <- Run(ctx, Deps{Provider: p}, inputSession(), nil, "导出报告", nil, func(ev Event) {
			if ev.Type == "input_required" {
				required <- *ev.Input
			}
		})
	}()
	var request human.Request
	select {
	case request = <-required:
	case err := <-finished:
		t.Fatalf("finished before input: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("no input event")
	}
	select {
	case err := <-finished:
		t.Fatalf("must block without an answer: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	if len(human.Pending(store.Snapshot())) != 1 {
		t.Fatal("refresh must restore the pending card")
	}
	if _, err := dao.AnswerInput(ctx, "session", request.MessageID, request.CallID, "answer", "PDF"); err != nil {
		t.Fatal(err)
	}
	NotifyInput("session")
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("answer did not wake Run")
	}
	if len(p.requests) != 2 {
		t.Fatalf("expected 2 model calls, got %d", len(p.requests))
	}
	next := p.requests[1].Messages
	last := next[len(next)-1]
	if last.Role != "tool" || last.ToolCallID != "arg_call" || !strings.Contains(last.Content, `"answer":"PDF"`) {
		t.Fatalf("answer must enter the original context as a tool result: %+v", last)
	}
	users, answers := 0, 0
	for _, m := range store.Snapshot() {
		if m.Role == "user" {
			users++
		}
		if m.Role == "tool" {
			answers++
		}
	}
	if users != 1 || answers != 1 {
		t.Fatalf("duplicate messages: users=%d answers=%d", users, answers)
	}
}

func checkpoint(rounds int) []model.Message {
	messages := []model.Message{{Role: "user", MessageID: "u", Content: "报告"}}
	for i := 1; i < rounds; i++ {
		messages = append(messages, model.Message{Role: "assistant", Content: "继续"})
	}
	calls := `[{"id":"q","type":"function","function":{"name":"request_input","arguments":"{\"question\":\"格式？\"}"}},{"id":"w","type":"function","function":{"name":"write_file","arguments":"{\"path\":\"x\",\"content\":\"y\"}"}}]`
	return append(messages, model.Message{Role: "assistant", MessageID: "a", ToolCalls: &calls})
}

func TestInputRestartRecoversPendingAndAlreadyAnswered(t *testing.T) {
	for _, alreadyAnswered := range []bool{false, true} {
		t.Run(map[bool]string{false: "pending", true: "answered"}[alreadyAnswered], func(t *testing.T) {
			ctx, store := inputtestdb.New(t, checkpoint(1)...)
			if alreadyAnswered {
				if _, err := dao.AnswerInput(ctx, "session", "a", "q", "answer", "PDF"); err != nil {
					t.Fatal(err)
				}
			}
			p := &argumentProvider{rounds: [][]provider.StreamChunk{{{Content: "继续完成", FinishReason: "stop"}}}}
			executed := false
			err := ResumeInput(ctx, Deps{Provider: p, ExecTool: func(context.Context, string, string, map[string]any) tools.Output {
				executed = true
				return tools.Output{Text: "unexpected"}
			}}, inputSession(), func(ev Event) {
				if ev.Type == "input_required" {
					if _, err := dao.AnswerInput(ctx, "session", "a", "q", "answer", "PDF"); err != nil {
						t.Error(err)
					}
					NotifyInput("session")
				}
			})
			if err != nil {
				t.Fatal(err)
			}
			if executed || len(p.requests) != 1 {
				t.Fatal("recovery must not replay sibling side effects or regenerate question")
			}
			history := store.Snapshot()
			if human.Active(history) != nil {
				t.Fatal("final answer closes checkpoint")
			}
			users := 0
			for _, m := range history {
				if m.Role == "user" {
					users++
				}
			}
			if users != 1 {
				t.Fatal("recovery must not insert a new user message")
			}
			msgs := p.requests[0].Messages
			if msgs[len(msgs)-1].Role != "tool" {
				t.Fatal("must complete protocol before provider call")
			}
		})
	}
}

func TestInputSkipContinuesButStopDoesNot(t *testing.T) {
	for _, stop := range []bool{false, true} {
		t.Run(map[bool]string{false: "skip", true: "stop"}[stop], func(t *testing.T) {
			ctx, store := inputtestdb.New(t)
			p := argumentScript(human.ToolName, `{"question":"格式？"}`, "tool_calls")
			err := Run(ctx, Deps{Provider: p}, inputSession(), nil, "报告", nil, func(ev Event) {
				if ev.Type != "input_required" {
					return
				}
				if stop {
					if err := dao.StopInput(ctx, "session"); err != nil {
						t.Error(err)
					}
				} else {
					if _, err := dao.AnswerInput(ctx, "session", ev.Input.MessageID, ev.ID, "skip", ""); err != nil {
						t.Error(err)
					}
				}
				NotifyInput("session")
			})
			if stop {
				if err != context.Canceled || len(p.requests) != 1 || human.Active(store.Snapshot()) != nil {
					t.Fatalf("stop must terminate: err=%v calls=%d", err, len(p.requests))
				}
			} else if err != nil || len(p.requests) != 2 {
				t.Fatalf("skip must continue: %v", err)
			}
		})
	}
}

func TestInputRestartPreservesRoundCap(t *testing.T) {
	ctx, store := inputtestdb.New(t, checkpoint(MaxToolRounds)...)
	if _, err := dao.AnswerInput(ctx, "session", "a", "q", "skip", ""); err != nil {
		t.Fatal(err)
	}
	p := &argumentProvider{}
	if err := ResumeInput(ctx, Deps{Provider: p}, inputSession(), func(Event) {}); err != nil {
		t.Fatal(err)
	}
	if len(p.requests) != 0 {
		t.Fatal("restart must not reset the round cap")
	}
	rows := store.Snapshot()
	if !strings.Contains(rows[len(rows)-1].Content, "轮数上限") || human.Active(rows) != nil {
		t.Fatal("round cap must leave a durable terminal record")
	}
}

func TestInputContextPreservesAnswersAndPending(t *testing.T) {
	history := checkpoint(1)
	msgs := buildContext(context.Background(), inputSession(), history)
	for _, m := range msgs {
		if m.Role == "tool" && m.ToolCallID == "q" {
			t.Fatal("must not synthesize an interrupted result for a pending question")
		}
	}
	aborted := checkpoint(1)
	aborted[len(aborted)-1].Content = "[模型输出被 token 上限截断]"
	aborted = append(aborted, model.Message{Role: "user", Content: "重新开始"})
	repaired := false
	for _, m := range buildContext(context.Background(), inputSession(), aborted) {
		if m.Role == "tool" && m.ToolCallID == "q" && strings.Contains(m.Content, "[未执行]") {
			repaired = true
		}
	}
	if !repaired {
		t.Fatal("aborted historic input must still receive protocol repair")
	}
	answer := human.Response{Status: "answered", Answer: strings.Repeat("保留用户的决定", 200)}
	raw, _ := json.Marshal(answer)
	if stubForToolResult(human.ToolName, `{"question":"格式？"}`, string(raw)) != string(raw) {
		t.Fatal("cold context must preserve human decisions")
	}
}
