package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"backend/dao"
	"backend/pkg/db"
	"backend/pkg/provider"
	"backend/pkg/sandbox"
	"backend/pkg/skill"
	"backend/pkg/tools"
)

// capturingProvider 在回放剧本之外记录每次请求（校验 Prompt 注入与工具暴露）
type capturingProvider struct {
	*fakeProvider
	mu   sync.Mutex
	reqs []*provider.ChatRequest
}

func (c *capturingProvider) StreamChat(ctx context.Context, req *provider.ChatRequest) (<-chan provider.StreamChunk, <-chan error) {
	c.mu.Lock()
	c.reqs = append(c.reqs, req)
	c.mu.Unlock()
	return c.fakeProvider.StreamChat(ctx, req)
}

func (c *capturingProvider) firstReq() *provider.ChatRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.reqs) == 0 {
		return nil
	}
	return c.reqs[0]
}

// seedSkillLib 临时技能库：一个可装载的 fetch-url + secrets，并同步进注册表
func seedSkillLib(t *testing.T, ctx context.Context) {
	t.Helper()
	root := t.TempDir()
	write := func(rel, content string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("fetch-url/SKILL.md", "---\nname: fetch-url\ndescription: 抓取网页转 Markdown\n---\n# Fetch URL\n调用 scripts/fetch.py")
	write("fetch-url/scripts/fetch.py", "print('fetch')")
	write(".secrets/fetch-url.env", "FETCH_API_KEY=topsecret\n")

	skill.Init(skill.Config{Dir: root})
	t.Cleanup(func() { skill.Init(skill.Config{}) })

	if err := dao.SyncSkills(ctx, []dao.SkillRegistryItem{{
		Name: "fetch-url", Description: "抓取网页转 Markdown", DirName: "fetch-url",
	}}); err != nil {
		t.Fatalf("sync skills: %v", err)
	}
	q := db.Ctx(ctx)
	t.Cleanup(func() {
		_, _ = q.Skill.WithContext(ctx).Where(q.Skill.DirName.Eq("fetch-url")).Delete()
	})
}

// collectToolResults 收集全部工具结果事件文本
func collectToolResults(events []Event) []string {
	var out []string
	for _, ev := range events {
		if ev.Type == "tool_call_result" || ev.Type == "tool_call_error" {
			out = append(out, ev.Output+ev.Error)
		}
	}
	return out
}

func containsText(list []string, sub string) bool {
	for _, s := range list {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// TestLoadSkillAutoMount 端到端：auto_skill 会话 → 目录注入 + load_skill 暴露 →
// 模型装载 → 挂载文件 + secrets 合并 + 关联以 source=auto 落库
func TestLoadSkillAutoMount(t *testing.T) {
	ctx := setupDB(t)
	seedSkillLib(t, ctx)

	session := newTestSession(t, ctx)
	if err := dao.UpdateSession(ctx, session.SessionID, map[string]interface{}{"auto_skill": true}); err != nil {
		t.Fatalf("enable auto_skill: %v", err)
	}
	session.AutoSkill = true
	t.Cleanup(func() { sandbox.Close(session.SessionID) })

	fp := &fakeProvider{rounds: [][]provider.StreamChunk{
		{ // 第 1 轮：装载技能 + 顺手验证同批工具已拿得到 secrets
			{ToolCalls: []provider.ToolCallDelta{
				{Index: 0, ID: "call_load", Name: tools.LoadSkillTool, Arguments: `{"name": "fetch-url"}`},
				{Index: 1, ID: "call_env", Name: "bash", Arguments: `{"command": "echo KEY=$FETCH_API_KEY"}`},
			}},
			{FinishReason: "tool_calls"},
		},
		{ // 第 2 轮：给出最终回答
			{Content: "网页内容已抓取"},
			{FinishReason: "stop"},
		},
	}}
	cp := &capturingProvider{fakeProvider: fp}

	var envSeen string
	exec := func(ectx context.Context, _, name string, args map[string]any) tools.Output {
		if name == "bash" {
			envSeen = strings.Join(tools.EnvFrom(ectx), ",")
			return tools.Output{Text: "ok"}
		}
		return tools.Output{Text: "[执行失败] unexpected tool " + name}
	}

	var events []Event
	emit := func(ev Event) { events = append(events, ev) }
	if err := Run(ctx, Deps{Provider: cp, ExecTool: exec}, session, nil, "帮我读一下这个网页", nil, emit); err != nil {
		t.Fatalf("run: %v", err)
	}

	// 1. System Prompt 注入了 Catalog 目录段（描述来自注册表）
	req := cp.firstReq()
	if req == nil || len(req.Messages) == 0 {
		t.Fatal("未捕获到模型请求")
	}
	sys := req.Messages[0].Content
	if !strings.Contains(sys, "Skill 目录（按需加载）") || !strings.Contains(sys, "fetch-url") {
		t.Fatalf("system prompt 未注入技能目录:\n%s", sys)
	}
	// 2. load_skill 工具仅在存在候选时暴露
	hasLoad := false
	for _, s := range req.Tools {
		if m, ok := s.(map[string]any); ok {
			if fn, ok := m["function"].(map[string]any); ok && fn["name"] == tools.LoadSkillTool {
				hasLoad = true
			}
		}
	}
	if !hasLoad {
		t.Fatalf("load_skill 未暴露给模型: %+v", req.Tools)
	}

	// 3. load_skill 拦截成功：结果回传 SKILL.md 正文
	results := collectToolResults(events)
	if !containsText(results, "已加载技能 `fetch-url`") || !containsText(results, "# Fetch URL") {
		t.Fatalf("load_skill 结果异常: %v", results)
	}
	// 3b. 装载成功广播 skill_loaded 事件（前端同步启用列表与提示条）
	var loaded *Event
	for i := range events {
		if events[i].Type == "skill_loaded" {
			loaded = &events[i]
		}
	}
	if loaded == nil {
		t.Fatal("未发出 skill_loaded 事件")
	}
	if loaded.Name != "fetch-url" || loaded.SkillID == 0 || loaded.ID != "call_load" {
		t.Fatalf("skill_loaded 事件字段异常: %+v", loaded)
	}
	// 4. 同批 bash 工具已拿得到合并后的 secrets
	if !strings.Contains(envSeen, "FETCH_API_KEY=topsecret") {
		t.Fatalf("同批工具未拿到 secrets: %q", envSeen)
	}
	// 5. 技能文件已挂进沙箱工作区
	data, err := sandbox.ReadFile(ctx, session.SessionID, "skills/fetch-url/scripts/fetch.py")
	if err != nil || !strings.Contains(string(data), "fetch") {
		t.Fatalf("技能文件未挂载进沙箱: %v %q", err, data)
	}
	// 6. 关联以 source=auto_loaded 落库（幂等）
	rows, err := dao.ListSessionSkills(ctx, session.SessionID)
	if err != nil || len(rows) != 1 || rows[0].Source != "auto_loaded" || rows[0].Skill.Name != "fetch-url" {
		t.Fatalf("自动装载关联落库异常: %v %+v", err, rows)
	}
}

// TestLoadSkillUnknownName 未命中目录：返回可自纠正提示，不落库、不暴露失败卡
func TestLoadSkillUnknownName(t *testing.T) {
	ctx := setupDB(t)
	seedSkillLib(t, ctx)

	session := newTestSession(t, ctx)
	if err := dao.UpdateSession(ctx, session.SessionID, map[string]interface{}{"auto_skill": true}); err != nil {
		t.Fatalf("enable auto_skill: %v", err)
	}
	session.AutoSkill = true

	fp := &fakeProvider{rounds: [][]provider.StreamChunk{
		{ // 第 1 轮：模型叫错名字
			{ToolCalls: []provider.ToolCallDelta{
				{Index: 0, ID: "call_1", Name: tools.LoadSkillTool, Arguments: `{"name": "不存在的技能"}`},
			}},
			{FinishReason: "tool_calls"},
		},
		{ // 第 2 轮：模型自我纠正，直接回答
			{Content: "好的，我直接回答"},
			{FinishReason: "stop"},
		},
	}}

	var events []Event
	if err := Run(ctx, Deps{Provider: fp}, session, nil, "随便试试", nil, func(ev Event) { events = append(events, ev) }); err != nil {
		t.Fatalf("run: %v", err)
	}

	results := collectToolResults(events)
	if !containsText(results, "[未找到]") || !containsText(results, "fetch-url") {
		t.Fatalf("未命中文案应列出可加载技能: %v", results)
	}
	for _, ev := range events {
		if ev.Type == "tool_call_error" {
			t.Fatalf("未命中不应弹失败卡: %+v", ev)
		}
	}
	rows, err := dao.ListSessionSkills(ctx, session.SessionID)
	if err != nil || len(rows) != 0 {
		t.Fatalf("未命中不应落关联: %v %+v", err, rows)
	}
}

// TestEnabledSkillMountedAndPrompted 手动启用（manual）的技能在 turn 开始即挂载，
// System Prompt 注入"可用 Skills"段，且不进入按需加载目录
func TestEnabledSkillMountedAndPrompted(t *testing.T) {
	ctx := setupDB(t)
	seedSkillLib(t, ctx)

	session := newTestSession(t, ctx)
	if err := dao.UpdateSession(ctx, session.SessionID, map[string]interface{}{"auto_skill": true}); err != nil {
		t.Fatalf("enable auto_skill: %v", err)
	}
	session.AutoSkill = true
	t.Cleanup(func() { sandbox.Close(session.SessionID) })

	// 模拟技能面板手动启用：注册表行已存在（seedSkillLib 同步），建关联
	q := db.Ctx(ctx)
	row, err := q.Skill.WithContext(ctx).Where(q.Skill.DirName.Eq("fetch-url")).First()
	if err != nil {
		t.Fatalf("query skill: %v", err)
	}
	if err := dao.ReplaceSessionSkills(ctx, session.SessionID, []dao.SessionSkillItem{{SkillID: row.ID}}); err != nil {
		t.Fatalf("enable skill: %v", err)
	}

	fp := &fakeProvider{rounds: [][]provider.StreamChunk{
		{{Content: "我看到了已挂载的技能"}, {FinishReason: "stop"}},
	}}
	cp := &capturingProvider{fakeProvider: fp}

	if err := Run(ctx, Deps{Provider: cp}, session, nil, "你能干什么", nil, func(ev Event) {}); err != nil {
		t.Fatalf("run: %v", err)
	}

	req := cp.firstReq()
	sys := req.Messages[0].Content
	if !strings.Contains(sys, "可用 Skills（已挂载）") || !strings.Contains(sys, "**fetch-url**（fetch-url/）") {
		t.Fatalf("启用技能段未注入:\n%s", sys)
	}
	if strings.Contains(sys, "Skill 目录（按需加载）") {
		t.Fatalf("已启用的技能不应再进按需加载目录:\n%s", sys)
	}
	// 无候选（唯一技能已启用）时不应暴露 load_skill
	for _, s := range req.Tools {
		if m, ok := s.(map[string]any); ok {
			if fn, ok := m["function"].(map[string]any); ok && fn["name"] == tools.LoadSkillTool {
				t.Fatal("无候选时不应暴露 load_skill")
			}
		}
	}
	// 文件已在 turn 开始挂载（模型未调用任何工具也可见）
	if _, err := sandbox.ReadFile(ctx, session.SessionID, "skills/fetch-url/SKILL.md"); err != nil {
		t.Fatalf("启用技能未在 turn 开始挂载: %v", err)
	}
}
