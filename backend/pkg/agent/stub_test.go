// stub_test.go 写时 stub 规则的纯函数测试：形态、幂等、豁免、拦截置换、extras 装配
package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"backend/dal/model"
	"backend/pkg/sandbox"
	"backend/pkg/storage"
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

func TestCallsNeedStub(t *testing.T) {
	ok := mkCall("read_file", `{"path":"a.py"}`)
	big := mkCall("write_file", `{"path":"a.py","content":"`+strings.Repeat("x", 41<<10)+`"}`)
	half := mkCall("write_file", `{"path":"b.py","content":"xx`)

	// stub 只改回放副本，合法大参数可以正常解析，原始批次不变。
	calls := []savedCall{ok, big}
	out := stubForToolCalls(calls)
	if !callsNeedStub(calls) || !validCallArgs(big.Function.Arguments) {
		t.Fatal("large valid arguments should execute but force stub replay")
	}
	if out[0].Function.Arguments != ok.Function.Arguments {
		t.Fatalf("normal call in batch must survive: %s", out[0].Function.Arguments)
	}
	var stub map[string]any
	if err := json.Unmarshal([]byte(out[1].Function.Arguments), &stub); err != nil {
		t.Fatalf("rejected stub must be valid JSON: %v", err)
	}
	if stub["_pruned"] != true || stub["_rejected"] != nil || stub["path"] != "a.py" {
		t.Fatalf("large write should use a normal pruned stub: %v", stub)
	}
	if calls[1].Function.Arguments != big.Function.Arguments {
		t.Fatal("original large arguments must remain untouched")
	}
	// 非法 JSON 始终生成合法的回放占位，不再依赖 finish reason。
	out = stubForToolCalls([]savedCall{half})
	if !callsNeedStub([]savedCall{half}) || !validCallArgs(out[0].Function.Arguments) ||
		!strings.Contains(out[0].Function.Arguments, "_rejected") {
		t.Fatalf("invalid arguments must get a safe replay stub: %s", out[0].Function.Arguments)
	}
	if callsNeedStub([]savedCall{ok}) {
		t.Fatal("small valid arguments should keep normal replay policy")
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

// TestStubForToolResultProcPreview 超限进程输出的预览版（自带"中间省略"字样）必须
// 仍能生成紧凑 stub，且尾 3 行把 blob 路径带进 stub——v1.0.0 幂等守卫误拦缺陷的回归用例
func TestStubForToolResultProcPreview(t *testing.T) {
	preview := "[stdout]\n" + strings.Repeat("line\n", 40) +
		"…[中间省略 480000 字符]…\n" + strings.Repeat("tail\n", 4) +
		"[输出超长，完整结果已写入工作区 .tool-outputs/bash-0921-x.log。排查建议：\n" +
		"1. 查找报错优先使用 grep：bash -c \"grep -E 'Error|Exception|Failed' .tool-outputs/bash-0921-x.log\"\n" +
		"2. 查看日志末尾优先使用 tail：bash -c \"tail -n 50 .tool-outputs/bash-0921-x.log\"\n" +
		"3. 精确查看代码再使用 read_file 配合 offset/limit]"

	stub := stubForToolResult("bash", `{"command":"cat big.log"}`, preview)
	if stub == preview {
		t.Fatalf("preview carrying 省略 note must still collapse to compact stub")
	}
	if !strings.HasPrefix(stub, "[bash] cat big.log") {
		t.Fatalf("stub should carry command: %q", stub)
	}
	if !strings.Contains(stub, ".tool-outputs/bash-0921-x.log") {
		t.Fatalf("stub tail must keep blob path (cold-zone retrievability): %q", stub)
	}
	if n := len(stub); n > 2000 {
		t.Fatalf("stub should be compact: %d chars", n)
	}
	if again := stubForToolResult("bash", `{"command":"cat big.log"}`, stub); again != stub {
		t.Fatalf("stub must be idempotent")
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
	// bash 超限落盘引用透传（JSON 编码：path + 归档 URL）
	ex = toolExtras(mkCall("bash", `{"command":"x"}`), tools.Output{Text: "ok", BlobRef: ".tool-outputs/bash-1.log"})
	if p, u := decodeBlobRef(ex.blobRef); p != ".tool-outputs/bash-1.log" || u != "" {
		t.Fatalf("blob ref must pass through: %+v", ex)
	}
}

func TestAssistantExtras(t *testing.T) {
	calls := []savedCall{mkCall("write_file", `{"path":"a.py","content":"hello world"}`)}
	ex := assistantExtras(context.Background(), "sess", "我先写文件", calls)
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

// setupBlobEnv 本地沙箱 + 本地对象存储（uploads 指到临时目录），持久化用例共用
func setupBlobEnv(t *testing.T) (wsRoot, uploads string) {
	t.Helper()
	wsRoot = t.TempDir()
	sandbox.Init(sandbox.Config{Driver: "local", LocalRoot: wsRoot})
	t.Cleanup(func() { sandbox.Init(sandbox.Config{Driver: "local"}) })
	uploads = t.TempDir()
	oldRoot := storage.LocalRoot
	storage.LocalRoot = uploads
	t.Cleanup(func() { storage.LocalRoot = oldRoot })
	return wsRoot, uploads
}

// TestWriteCallBlobArchive 大参数/非法参数原文双写：沙箱 + 对象存储归档。
func TestWriteCallBlobArchive(t *testing.T) {
	wsRoot, uploads := setupBlobEnv(t)

	path, url := writeCallBlob(context.Background(), "sess1", `{"calls":[{"id":"c1"}]}`)
	if !strings.HasPrefix(path, ".tool-outputs/arguments-") {
		t.Fatalf("sandbox path wrong: %q", path)
	}
	if !strings.HasPrefix(url, "/api/uploads/") {
		t.Fatalf("archive url wrong: %q", url)
	}
	if _, err := os.Stat(filepath.Join(wsRoot, "sess1", filepath.FromSlash(path))); err != nil {
		t.Fatalf("sandbox blob missing: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(uploads, filepath.Base(url)))
	if err != nil || !strings.Contains(string(data), "c1") {
		t.Fatalf("archive wrong: %v", err)
	}

	// 编解码往返 & v1.0.0 裸路径兼容
	p2, u2 := decodeBlobRef(encodeBlobRef(path, url))
	if p2 != path || u2 != url {
		t.Fatalf("blobRef roundtrip broken: %q %q", p2, u2)
	}
	if p3, u3 := decodeBlobRef(".tool-outputs/bash-x.log"); p3 != ".tool-outputs/bash-x.log" || u3 != "" {
		t.Fatalf("bare path compat broken: %q %q", p3, u3)
	}
}

// TestMaterializeBlobs 沙箱回收重建后凭归档 URL 恢复；文件还在则不覆盖；裸路径行跳过
func TestMaterializeBlobs(t *testing.T) {
	wsRoot, _ := setupBlobEnv(t)
	path, url := writeCallBlob(context.Background(), "sess1", "ORIG-DATA")
	_ = wsRoot

	// 模拟沙箱回收：切到全新空沙箱根
	root2 := t.TempDir()
	sandbox.Init(sandbox.Config{Driver: "local", LocalRoot: root2})

	history := []model.Message{{Role: "tool", Content: "x", BlobRef: sp(encodeBlobRef(path, url))}}
	materializeBlobs(context.Background(), "sess1", history)
	data, err := os.ReadFile(filepath.Join(root2, "sess1", filepath.FromSlash(path)))
	if err != nil || string(data) != "ORIG-DATA" {
		t.Fatalf("rematerialize failed: %v %q", err, data)
	}

	// 已存在则跳过（沙箱存活时零流量，本地内容不被归档覆盖）
	if err := os.WriteFile(filepath.Join(root2, "sess1", filepath.FromSlash(path)), []byte("LOCAL"), 0o644); err != nil {
		t.Fatal(err)
	}
	materializeBlobs(context.Background(), "sess1", history)
	data, _ = os.ReadFile(filepath.Join(root2, "sess1", filepath.FromSlash(path)))
	if string(data) != "LOCAL" {
		t.Fatalf("existing blob must not be overwritten: %q", data)
	}

	// 裸路径（无归档 URL）静默跳过，不报错
	materializeBlobs(context.Background(), "sess1", []model.Message{{Role: "tool", Content: "x", BlobRef: sp(".tool-outputs/old.log")}})
}

func TestEstimateTokens(t *testing.T) {
	if got := estimateTokens("abcd"); got != 2 {
		t.Fatalf("estimate wrong: %d", got)
	}
	if got := estimateTokens("ab", "cd"); got != 2 {
		t.Fatalf("multi-part estimate wrong: %d", got)
	}
}
