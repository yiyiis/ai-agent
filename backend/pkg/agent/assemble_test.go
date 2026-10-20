// assemble_test.go 分区组装测试：热/冷边界、预算挤出、force_stub、旧数据回退、
// 图片 last-N、rezone 幂等、组装确定性（纯函数，无需 DB）
package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"backend/dal/model"
	"backend/pkg/ctxpolicy"
	"backend/pkg/provider"
)

func sp(s string) *string { return &s }

func callsJSON(t *testing.T, calls ...savedCall) *string {
	t.Helper()
	b, err := json.Marshal(calls)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	return &s
}

func asCalls(t *testing.T, m provider.ChatMessage) []savedCall {
	t.Helper()
	calls, ok := m.ToolCalls.([]savedCall)
	if !ok {
		t.Fatalf("expected []savedCall ToolCalls, got %T", m.ToolCalls)
	}
	return calls
}

// TestBuildContextZoning 新数据（治理列齐全）：write 批次老化出热区换 stub，
// 最新 read 批次（in-flight）保真；悬空修复不受分区影响
func TestBuildContextZoning(t *testing.T) {
	session := &model.Session{SessionID: "s1", Model: "test-model"}
	history := []model.Message{
		{Role: "user", Content: "写个大文件"},
		{
			Role:      "assistant",
			Content:   "我来写",
			ToolCalls: callsJSON(t, newSavedCall("call_w", "write_file", `{"path":"a.py","content":"`+strings.Repeat("x", 8192)+`"}`)),
			StubToolCalls: func() *string {
				stub := stubForToolCalls([]savedCall{newSavedCall("call_w", "write_file", `{"path":"a.py","content":"`+strings.Repeat("x", 8192)+`"}`)})
				b, _ := json.Marshal(stub)
				s := string(b)
				return &s
			}(),
			TokensFull: 5000,
			StubTokens: 40,
		},
		{Role: "tool", Content: "已写入 8192 字符到 a.py", ToolCallID: sp("call_w"), Name: sp("write_file"), TokensFull: 20, StubTokens: 20},
		{
			Role:      "assistant",
			Content:   "再读一遍验证",
			ToolCalls: callsJSON(t, newSavedCall("call_r", "read_file", `{"path":"a.py"}`)),
			StubToolCalls: func() *string {
				b, _ := json.Marshal(stubForToolCalls([]savedCall{newSavedCall("call_r", "read_file", `{"path":"a.py"}`)}))
				s := string(b)
				return &s
			}(),
			TokensFull: 30,
			StubTokens: 30,
		},
		{Role: "tool", Content: strings.Repeat("x", 4096), ToolCallID: sp("call_r"), Name: sp("read_file"),
			StubContent: sp("[read_file a.py L1 起，约 4KB；结果已省略，需要时重新 read_file]"), TokensFull: 2048, StubTokens: 30},
	}

	msgs := buildContext(nil, session, history)

	// 角色序列：system,user,assistant,tool,assistant,tool
	var roles []string
	for _, m := range msgs {
		roles = append(roles, m.Role)
	}
	want := []string{"system", "user", "assistant", "tool", "assistant", "tool"}
	if strings.Join(roles, ",") != strings.Join(want, ",") {
		t.Fatalf("role sequence wrong: %v", roles)
	}

	// write 批次（distance 1 ≥ write 阈值 1）→ 冷区 stub
	writeCalls := asCalls(t, msgs[2])
	if !strings.Contains(writeCalls[0].Function.Arguments, "_pruned") || !strings.Contains(writeCalls[0].Function.Arguments, "a.py") {
		t.Fatalf("write args should be stubbed: %s", writeCalls[0].Function.Arguments)
	}
	// read 批次（in-flight）→ 原文保真
	readCalls := asCalls(t, msgs[4])
	if readCalls[0].Function.Arguments != `{"path":"a.py"}` {
		t.Fatalf("read args must stay full: %s", readCalls[0].Function.Arguments)
	}
	if msgs[5].Content != strings.Repeat("x", 4096) {
		t.Fatalf("in-flight read result must stay full, got %.40s", msgs[5].Content)
	}

	// 确定性：同输入两次组装逐字节一致
	again := buildContext(nil, session, history)
	if len(again) != len(msgs) {
		t.Fatalf("determinism broken")
	}
	for i := range msgs {
		a, _ := json.Marshal(msgs[i])
		b, _ := json.Marshal(again[i])
		if string(a) != string(b) {
			t.Fatalf("assemble must be deterministic at msg %d", i)
		}
	}
}

// TestBuildContextLegacyFallback 旧数据（治理列全空）：现场按同一套规则生成 stub
func TestBuildContextLegacyFallback(t *testing.T) {
	session := &model.Session{SessionID: "s1", Model: "test-model"}
	bashJSON := callsJSON(t, newSavedCall("c9", "bash", `{"command":"ls"}`))
	history := []model.Message{
		{Role: "user", Content: "写"},
		{Role: "assistant", Content: "写文件", ToolCalls: callsJSON(t, newSavedCall("c1", "write_file", `{"path":"a.py","content":"`+strings.Repeat("y", 2048)+`"}`))},
		{Role: "tool", Content: "已写入 2048 字符到 a.py", ToolCallID: sp("c1"), Name: sp("write_file")},
		// 更新的 bash 批次，让上面的 write 批次老化出热区（write 阈值 1）
		{Role: "assistant", Content: "", ToolCalls: bashJSON},
		{Role: "tool", Content: "a.py", ToolCallID: sp("c9"), Name: sp("bash")},
		{Role: "assistant", Content: "完成"},
	}
	msgs := buildContext(nil, session, history)

	calls := asCalls(t, msgs[2])
	if !strings.Contains(calls[0].Function.Arguments, "_pruned") {
		t.Fatalf("legacy write args should be stubbed on the fly: %s", calls[0].Function.Arguments)
	}

	// 旧的大 read 结果同样现场生成 stub（配对参数来自 assistant 行）；
	// read 阈值 2，其后需要两个新批次才老化
	history2 := []model.Message{
		{Role: "assistant", Content: "读", ToolCalls: callsJSON(t, newSavedCall("c2", "read_file", `{"path":"b.go"}`))},
		{Role: "tool", Content: strings.Repeat("z", 8192), ToolCallID: sp("c2"), Name: sp("read_file")},
		{Role: "assistant", Content: "", ToolCalls: callsJSON(t, newSavedCall("c8", "bash", `{"command":"ls"}`))},
		{Role: "tool", Content: "b.go", ToolCallID: sp("c8"), Name: sp("bash")},
		{Role: "assistant", Content: "", ToolCalls: callsJSON(t, newSavedCall("c7", "bash", `{"command":"pwd"}`))},
		{Role: "tool", Content: "/workspace", ToolCallID: sp("c7"), Name: sp("bash")},
		{Role: "assistant", Content: "读完了"},
	}
	msgs2 := buildContext(nil, session, history2)
	if !strings.Contains(msgs2[2].Content, "read_file b.go") || !strings.Contains(msgs2[2].Content, "已省略") {
		t.Fatalf("legacy read result should get stub: %.80s", msgs2[2].Content)
	}
}

// TestBuildContextForceStub 原参数完整保留，ctx_mode=stub 即便处于最新 step 也恒用精简版。
func TestBuildContextForceStub(t *testing.T) {
	session := &model.Session{SessionID: "s1", Model: "test-model"}
	raw := `{"path":"big.bin","content":"` + strings.Repeat("q", 41<<10) + `"}`
	history := []model.Message{
		{Role: "user", Content: "写"},
		{Role: "assistant", Content: "写入", ToolCalls: callsJSON(t, newSavedCall("c1", "write_file", raw)), CtxMode: "stub", TokensFull: 22000, StubTokens: 60},
		{Role: "tool", Content: "已写入 big.bin", ToolCallID: sp("c1"), Name: sp("write_file"), TokensFull: 40, StubTokens: 40},
	}
	msgs := buildContext(nil, session, history)
	calls := asCalls(t, msgs[2])
	if !strings.Contains(calls[0].Function.Arguments, "_pruned") || strings.Contains(calls[0].Function.Arguments, "_rejected") {
		t.Fatalf("force-stub row must render stub even in-flight: %s", calls[0].Function.Arguments)
	}
	var persisted []savedCall
	if err := json.Unmarshal([]byte(*history[1].ToolCalls), &persisted); err != nil {
		t.Fatal(err)
	}
	if msgs[3].Content != "已写入 big.bin" || persisted[0].Function.Arguments != raw {
		t.Fatal("successful result and stored original arguments must remain intact")
	}
}

// TestBuildContextImageKeepLast 历史图片只保留最近 N 张真实图，其余文本占位
func TestBuildContextImageKeepLast(t *testing.T) {
	session := &model.Session{SessionID: "s1", Model: "test-model"}
	att := func(name string) *string {
		b, _ := json.Marshal([]Attachment{{URL: "https://example.com/" + name, Filename: name, ContentType: "image/png"}})
		s := string(b)
		return &s
	}
	history := []model.Message{
		{Role: "user", Content: "第一张图", Attachments: att("old.png")},
		{Role: "assistant", Content: "看过"},
		{Role: "user", Content: "后两张", Attachments: att("m1.png")},
		{Role: "user", Content: "再一张", Attachments: att("m2.png")},
	}
	msgs := buildContext(nil, session, history)

	imgCount, phCount := 0, 0
	for _, m := range msgs {
		for _, p := range m.ContentParts {
			if p.Type == string(provider.ContentPartTypeImageURL) {
				imgCount++
			}
			if p.Type == string(provider.ContentPartTypeText) && strings.Contains(p.Text, "图片已省略") {
				phCount++
			}
		}
	}
	if imgCount != 2 || phCount != 1 {
		t.Fatalf("image keep-last wrong: images=%d placeholders=%d", imgCount, phCount)
	}
	found := false
	for _, m := range msgs {
		for _, p := range m.ContentParts {
			if strings.Contains(p.Text, "old.png") {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("dropped image placeholder should carry filename")
	}
}

// TestZoneBackwardBudgetEviction 热区预算挤出：预算耗尽后更旧条目全部进冷区（粘性）
func TestZoneBackwardBudgetEviction(t *testing.T) {
	old := ctxpolicy.Current()
	t.Cleanup(func() { ctxpolicy.Init(old) })
	ctxpolicy.Init(ctxpolicy.ContextPolicy{
		HotSteps:        []ctxpolicy.HotStepRule{{Tool: "read_file", Steps: 8}},
		DefaultHotSteps: 8,
		HotBudgetRatio:  0.25,
	})
	// model "" → 默认窗口 131072 → 预算 32768；三条 read 结果各 20000：第三条
	// in-flight 保真，第二条（20000+20000>32768 触发）吃满预算，第一条被粘性挤出
	var a1, a2, a3 bool
	mkItem := func(name string, tokens int32, applied *bool) zoneItem {
		return zoneItem{
			role: "tool", isPayload: true, toolName: name, tokensFull: tokens,
			applyCold: func() { *applied = true },
		}
	}
	assistant := func(calls ...savedCall) zoneItem {
		return zoneItem{role: "assistant", isPayload: true, calls: calls}
	}
	items := []zoneItem{
		assistant(newSavedCall("c1", "read_file", "{}")),
		mkItem("read_file", 20000, &a1),
		assistant(newSavedCall("c2", "read_file", "{}")),
		mkItem("read_file", 20000, &a2),
		assistant(newSavedCall("c3", "read_file", "{}")),
		mkItem("read_file", 20000, &a3),
	}
	zoneBackward(items, "")
	if a3 {
		t.Fatalf("newest step must stay hot")
	}
	if !a1 || !a2 {
		t.Fatalf("older steps must be evicted once budget blown (a1=%v a2=%v)", a1, a2)
	}
}

// TestRezoneMsgs 轮内 rezone：老化批次换 stub、in-flight 保真、幂等
func TestRezoneMsgs(t *testing.T) {
	big := strings.Repeat("k", 60000)
	msgs := []provider.ChatMessage{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "跑一下"},
		{Role: "assistant", Content: "看日志", ToolCalls: []savedCall{newSavedCall("c1", "bash", `{"command":"cat big.log"}`)}},
		{Role: "tool", Content: "[stdout]\n" + big + "\n[exit_code] 0", ToolCallID: "c1"},
		{Role: "assistant", Content: "再读配置", ToolCalls: []savedCall{newSavedCall("c2", "read_file", `{"path":"conf.yml"}`)}},
		{Role: "tool", Content: strings.Repeat("v", 5000), ToolCallID: "c2"},
		// 悬空行（无配对调用）：按叙事处理，永不被改
		{Role: "tool", Content: "[未执行] 该轮对话中断", ToolCallID: "c_orphan"},
	}
	rezoneMsgs(msgs, "")

	// bash 批次（distance 1，阈值 3？bash hotSteps=3 → 应保真！）
	// 说明：bash 阈值为 3，distance 1 仍在热区——这里断言它保真，老化判定由 read/write
	// 的窄阈值与预算兜底。此用例改为构造 distance ≥ 3 的场景见下。
	if strings.Contains(msgs[3].Content, "已省略") {
		t.Fatalf("bash within 3 steps should stay full (policy)")
	}

	// 构造 4 个批次（read 在最老、write 次之、两个 bash 在最新）：
	// read 阈值 2 / write 阈值 1，distance 3 的两个批次必冷；bash 阈值 3 保真
	msgs2 := []provider.ChatMessage{
		{Role: "user", Content: "q"},
		{Role: "assistant", ToolCalls: []savedCall{newSavedCall("r1", "read_file", `{"path":"a.py"}`)}},
		{Role: "tool", Content: strings.Repeat("y", 8000), ToolCallID: "r1"},
		{Role: "assistant", ToolCalls: []savedCall{newSavedCall("w1", "write_file", `{"path":"a.py","content":"`+strings.Repeat("x", 3000)+`"}`)}},
		{Role: "tool", Content: "ok", ToolCallID: "w1"},
		{Role: "assistant", ToolCalls: []savedCall{newSavedCall("b1", "bash", `{"command":"ls"}`)}},
		{Role: "tool", Content: "out", ToolCallID: "b1"},
		{Role: "assistant", ToolCalls: []savedCall{newSavedCall("b2", "bash", `{"command":"ls"}`)}},
		{Role: "tool", Content: "out", ToolCallID: "b2"},
	}
	rezoneMsgs(msgs2, "")
	// 最老的 read 批次（distance 3 ≥ 阈值 2）→ 结果换 stub
	if !strings.Contains(msgs2[2].Content, "已省略") {
		t.Fatalf("read result at distance 3 must be stubbed: %.40s", msgs2[2].Content)
	}
	// write 批次（distance 2 ≥ 阈值 1）→ 参数换 stub
	w := asCalls(t, msgs2[3])
	if !strings.Contains(w[0].Function.Arguments, "_pruned") {
		t.Fatalf("write at distance 2 must be stubbed: %s", w[0].Function.Arguments)
	}
	// bash 批次（distance 1，阈值 3）与 in-flight（最新批次）保真
	if strings.Contains(msgs2[6].Content, "已省略") || strings.Contains(msgs2[8].Content, "已省略") {
		t.Fatalf("bash within threshold must stay full")
	}
	b := asCalls(t, msgs2[7])
	if b[0].Function.Arguments != `{"command":"ls"}` {
		t.Fatalf("in-flight args must stay")
	}

	// 幂等：再次 rezone 结果不变
	before, _ := json.Marshal(msgs2)
	rezoneMsgs(msgs2, "")
	after, _ := json.Marshal(msgs2)
	if string(before) != string(after) {
		t.Fatalf("rezone must be idempotent")
	}
}
