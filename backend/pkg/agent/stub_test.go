// stub_test.go 写时 stub 规则的纯函数测试：形态、幂等、豁免、拦截置换、extras 装配
package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"backend/pkg/tools"
)

func mkCall(name, args string) savedCall { return newSavedCall("call_1", name, args) }

func TestStubForToolCallsWriteFile(t *testing.T) {
	calls := []savedCall{mkCall("write_file", `{"path":"a.py","content":"line1\nline2"}`)}
	out := stubForToolCalls(calls)

	// 结构保留：id/type/name 原样，仅换 arguments
	if out[0].ID != "call_1" || out[0].Type != "function" || out[0].Function.Name != "write_file" {
		t.Fatalf("structure must be preserved: %+v", out[0])
	}
	var stub map[string]any
	if err := json.Unmarshal([]byte(out[0].Function.Arguments), &stub); err != nil {
		t.Fatalf("stub args must be valid JSON: %v", err)
	}
	if stub["path"] != "a.py" || stub["_pruned"] != true {
		t.Fatalf("write stub wrong: %v", stub)
	}
	if b, _ := stub["bytes"].(float64); b != 11 {
		t.Fatalf("bytes should reflect content size: %v", stub)
	}
	if l, _ := stub["lines"].(float64); l != 2 {
		t.Fatalf("lines should count content lines: %v", stub)
	}
	if !strings.Contains(out[0].Function.Arguments, "read_file") {
		t.Fatalf("stub should tell model how to re-fetch")
	}
	// 幂等：stub 的 stub 不再变化
	if again := stubForToolCalls(out); again[0].Function.Arguments != out[0].Function.Arguments {
		t.Fatalf("stub must be idempotent")
	}
	// 原批次不被修改
	if calls[0].Function.Arguments == out[0].Function.Arguments {
		t.Fatalf("original must be untouched")
	}
}

func TestStubForToolCallsSmallArgsUntouched(t *testing.T) {
	raw := `{"path":"a.py","offset":10}`
	out := stubForToolCalls([]savedCall{mkCall("read_file", raw)})
	if out[0].Function.Arguments != raw {
		t.Fatalf("small args must stay as-is: %s", out[0].Function.Arguments)
	}
}

func TestPrepareAssistantCallsRejection(t *testing.T) {
	ok := mkCall("read_file", `{"path":"a.py"}`)
	big := mkCall("write_file", `{"path":"a.py","content":"`+strings.Repeat("x", 41<<10)+`"}`)
	half := mkCall("write_file", `{"path":"b.py","content":"xx`)

	// 超限：仅超限调用被置换，同批次正常调用保留原文
	out, rejected := prepareAssistantCalls([]savedCall{ok, big}, false, false)
	if !rejected {
		t.Fatalf("oversized call must be rejected")
	}
	if out[0].Function.Arguments != ok.Function.Arguments {
		t.Fatalf("normal call in batch must survive: %s", out[0].Function.Arguments)
	}
	var stub map[string]any
	if err := json.Unmarshal([]byte(out[1].Function.Arguments), &stub); err != nil {
		t.Fatalf("rejected stub must be valid JSON: %v", err)
	}
	if stub["_rejected"] != true || stub["path"] != "a.py" {
		t.Fatalf("rejected stub should keep _rejected and path: %v", stub)
	}

	// finish=length：半截 JSON 置换成可回放的合法 JSON
	out, rejected = prepareAssistantCalls([]savedCall{half}, false, true)
	if !rejected || !validCallArgs(out[0].Function.Arguments) {
		t.Fatalf("truncated call must become replayable JSON: %s", out[0].Function.Arguments)
	}

	// 正常批次：原样返回、不拒绝
	out, rejected = prepareAssistantCalls([]savedCall{ok}, false, false)
	if rejected || out[0].Function.Arguments != ok.Function.Arguments {
		t.Fatalf("normal batch must pass through")
	}

	// 流中断但参数完整合法：不置换（悬空修复已有兜底，合法 JSON 可正常回放）
	if _, rejected = prepareAssistantCalls([]savedCall{ok}, true, false); rejected {
		t.Fatalf("valid calls under stream error must survive")
	}
}

func TestStubForToolResult(t *testing.T) {
	// 错误结果保留全文（失败是证据）
	errText := "[执行失败] boom"
	if got := stubForToolResult("bash", `{"command":"ls"}`, errText); got != errText {
		t.Fatalf("error must be preserved: %q", got)
	}

	// read 摘要：路径 + 起始行 + 体积 + 取回方式
	got := stubForToolResult("read_file", `{"path":"src/a.go","offset":5}`, strings.Repeat("x", 100))
	if !strings.Contains(got, "src/a.go") || !strings.Contains(got, "L5") || !strings.Contains(got, "重新 read_file") {
		t.Fatalf("read stub wrong: %q", got)
	}

	// bash 摘要：命令 + 头尾行（尾部含 exit_code）
	content := "[stdout]\n" + strings.Repeat("line\n", 20) + "[exit_code] 0"
	got = stubForToolResult("bash", `{"command":"go test ./..."}`, content)
	if !strings.Contains(got, "go test ./...") || !strings.Contains(got, "[exit_code] 0") || !strings.Contains(got, "省略") {
		t.Fatalf("bash stub wrong: %q", got)
	}

	// write 成功反馈=原文（本来就短）
	small := "已写入 a.txt（3 行）"
	if got := stubForToolResult("write_file", `{"path":"a.txt"}`, small); got != small {
		t.Fatalf("write feedback stays: %q", got)
	}

	// 幂等：已是 stub 的内容不再变化
	stub := stubForToolResult("read_file", `{"path":"a.go"}`, strings.Repeat("x", 100))
	if got := stubForToolResult("read_file", `{"path":"a.go"}`, stub); got != stub {
		t.Fatalf("stub must be idempotent: %q", got)
	}

	// 大体积未知工具走通用占位
	if got := stubForToolResult("some_tool", "{}", strings.Repeat("x", 1024)); !strings.Contains(got, "已省略") {
		t.Fatalf("generic stub wrong: %q", got)
	}
}

func TestCallTarget(t *testing.T) {
	tgt, op := callTarget([]savedCall{mkCall("write_file", `{"path":"a.py","content":"x"}`)})
	if tgt != "a.py" || op != "write" {
		t.Fatalf("single file target wrong: %q %q", tgt, op)
	}
	// 同批次目标不一致 → 保守不折
	if tgt, _ = callTarget([]savedCall{
		mkCall("write_file", `{"path":"a.py","content":"x"}`),
		mkCall("write_file", `{"path":"b.py","content":"x"}`),
	}); tgt != "" {
		t.Fatalf("mixed targets must fold to nothing: %q", tgt)
	}
	// bash 不参与折叠
	if tgt, _ = callTarget([]savedCall{mkCall("bash", `{"command":"ls"}`)}); tgt != "" {
		t.Fatalf("bash must not fold: %q", tgt)
	}
}

func TestToolExtras(t *testing.T) {
	ex := toolExtras(mkCall("read_file", `{"path":"a.go"}`), tools.Output{Text: strings.Repeat("x", 4096)})
	if ex.stubContent == "" || ex.stubTokens <= 0 || ex.tokensFull != int32(2048) {
		t.Fatalf("read extras wrong: %+v", ex)
	}
	if ex.toolTarget != "a.go" || ex.toolOp != "read" {
		t.Fatalf("fold key wrong: %+v", ex)
	}
	// 错误结果：stub=content → stubContent 列写空
	ex = toolExtras(mkCall("bash", `{"command":"x"}`), tools.Output{Text: "[超时] t"})
	if ex.stubContent != "" || ex.blobRef != "" {
		t.Fatalf("error extras should be minimal: %+v", ex)
	}
	// bash 超限落盘引用透传
	ex = toolExtras(mkCall("bash", `{"command":"x"}`), tools.Output{Text: "ok", BlobRef: ".tool-outputs/bash-1.log"})
	if ex.blobRef != ".tool-outputs/bash-1.log" {
		t.Fatalf("blob ref must pass through: %+v", ex)
	}
}

func TestAssistantExtras(t *testing.T) {
	calls := []savedCall{mkCall("write_file", `{"path":"a.py","content":"hello world"}`)}
	ex := assistantExtras(context.Background(), "sess", "我先写文件", calls, calls, false)
	if ex.forceStub || ex.blobRef != "" {
		t.Fatalf("normal assistant row must not force stub: %+v", ex)
	}
	if ex.stubToolCalls == "" || !strings.Contains(ex.stubToolCalls, "_pruned") {
		t.Fatalf("stub tool calls missing: %q", ex.stubToolCalls)
	}
	if ex.toolTarget != "a.py" || ex.toolOp != "write" {
		t.Fatalf("fold key wrong: %+v", ex)
	}
	if ex.tokensFull <= 0 || ex.stubTokens <= 0 {
		t.Fatalf("token estimates missing: %+v", ex)
	}
}

func TestEstimateTokens(t *testing.T) {
	if got := estimateTokens("abcd"); got != 2 {
		t.Fatalf("estimate wrong: %d", got)
	}
	if got := estimateTokens("ab", "cd"); got != 2 {
		t.Fatalf("multi-part estimate wrong: %d", got)
	}
}
