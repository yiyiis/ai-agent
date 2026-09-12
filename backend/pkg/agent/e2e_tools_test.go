//go:build e2e

// 对话级工具调用 E2E 测试：用真实 LLM 驱动完整 ReAct 循环，逐个验证内置工具。
// 默认 `go test` 不编译本文件；手动运行：
//
//	go test -tags e2e -run TestE2E -v ./pkg/agent/ -timeout 30m
//
// 依赖本地 MySQL（etc/config.local.yaml）与 config 中可用的模型 Provider。
package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"backend/config"
	"backend/dal/model"
	"backend/dao"
	"backend/pkg/db"
	"backend/pkg/provider"
	backendtools "backend/pkg/tools"
)

var (
	e2eOnce  chan struct{}
	e2eModel string
)

// e2eSetup 注册 config 里的全部 Provider，返回注入了 db 的 ctx
func e2eSetup(t *testing.T) context.Context {
	t.Helper()
	if e2eOnce == nil {
		e2eOnce = make(chan struct{})
		confPath := "../../etc/config.yaml"
		if _, err := os.Stat("../../etc/config.local.yaml"); err == nil {
			confPath = "../../etc/config.local.yaml"
		}
		conf := config.LoadConfig(confPath)
		db.InitDb(conf.DbConf)
		e2eModel = os.Getenv("E2E_MODEL")
		if e2eModel == "" {
			e2eModel = conf.LLM.DefaultModel
		}
		for _, p := range conf.LLM.Providers {
			pvd := provider.NewOpenAICompatProvider(p.BaseURL, p.APIKey)
			provider.RegisterProvider(p.Name, pvd)
			for _, m := range p.Models {
				provider.RegisterModelRoute(m, p.Name)
			}
		}
		close(e2eOnce)
	}
	return db.WithContext(context.Background())
}

func e2eSession(t *testing.T, ctx context.Context) *model.Session {
	t.Helper()
	s := &model.Session{
		SessionID: "e2e-" + fmt.Sprint(time.Now().UnixNano()),
		UserID:    1,
		CompanyID: 1,
		Title:     "新会话",
		Model:     e2eModel,
	}
	if err := dao.CreateSession(ctx, s); err != nil {
		t.Fatalf("create session: %v", err)
	}
	t.Cleanup(func() {
		_ = dao.DeleteSession(ctx, s.SessionID)
		os.RemoveAll(filepath.Join(backendtools.WorkspaceRoot, s.SessionID))
	})
	return s
}

// runTurnWithRealModel 跑一轮真实对话，返回全部事件与落库的 tool 消息
func runTurnWithRealModel(t *testing.T, ctx context.Context, session *model.Session, content string, attachments []Attachment) ([]Event, []model.Message) {
	t.Helper()
	p, ok := provider.GetProviderForModel(session.Model)
	if !ok {
		t.Fatalf("provider not found for %s", session.Model)
	}
	var events []Event
	start := time.Now()
	err := Run(ctx, Deps{Provider: p}, session, nil, content, attachments, func(ev Event) { events = append(events, ev) })
	if err != nil {
		t.Fatalf("agent run: %v", err)
	}
	t.Logf("turn finished in %s, %d events", time.Since(start).Round(time.Millisecond), len(events))

	msgs, err := dao.ListMessagesBySessionID(ctx, session.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	return events, msgs
}

// toolOutputs 取落库的工具结果，按 (名称 -> 输出列表)
func toolOutputs(t *testing.T, msgs []model.Message) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	for _, m := range msgs {
		if m.Role != "tool" {
			continue
		}
		name := ""
		if m.Name != nil {
			name = *m.Name
		}
		out[name] = append(out[name], m.Content)
	}
	return out
}

func assertToolOk(t *testing.T, outs map[string][]string, tool, mustContain string) {
	t.Helper()
	results, ok := outs[tool]
	if !ok || len(results) == 0 {
		t.Fatalf("%s 未被调用；实际调用: %v", tool, keysOf(outs))
	}
	last := results[len(results)-1]
	if backendtools.IsErrorResult(last) {
		t.Fatalf("%s 最后一次执行失败: %s", tool, last)
	}
	if mustContain != "" && !strings.Contains(last, mustContain) {
		t.Fatalf("%s 输出应包含 %q，实际: %s", tool, mustContain, last)
	}
	t.Logf("%s ✓ %.80s", tool, strings.ReplaceAll(last, "\n", " | "))
}

func keysOf(m map[string][]string) []string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}

// TestE2EWorkspaceTools 通过自然语言对话驱动文件三件套 + bash
func TestE2EWorkspaceTools(t *testing.T) {
	ctx := e2eSetup(t)
	session := e2eSession(t, ctx)

	prompt := "请严格按顺序完成下面的任务，每步都使用对应的工具：" +
		"1) 用 write_file 创建 notes.txt，内容为一行文本：hello from e2e；" +
		"2) 用 read_file 读取 notes.txt 全文；" +
		"3) 用 edit_file 把 notes.txt 里的 hello 替换为 goodbye；" +
		"4) 用 bash 执行 cat notes.txt 确认替换结果；" +
		"最后用一句话总结文件最终内容。"

	_, msgs := runTurnWithRealModel(t, ctx, session, prompt, nil)
	outs := toolOutputs(t, msgs)

	assertToolOk(t, outs, "write_file", "")
	assertToolOk(t, outs, "read_file", "hello from e2e")
	assertToolOk(t, outs, "edit_file", "")
	assertToolOk(t, outs, "bash", "goodbye from e2e")
}

// TestE2EPythonExec 验证 python_exec（环境无 Python 时降级为友好失败提示）
func TestE2EPythonExec(t *testing.T) {
	ctx := e2eSetup(t)
	session := e2eSession(t, ctx)

	_, msgs := runTurnWithRealModel(t, ctx, session,
		"用 python_exec 执行 print(40+2) 并告诉我输出。", nil)

	outs := toolOutputs(t, msgs)
	results, ok := outs["python_exec"]
	if !ok || len(results) == 0 {
		t.Fatalf("python_exec 未被调用；实际调用: %v", keysOf(outs))
	}
	last := results[len(results)-1]
	if backendtools.IsErrorResult(last) {
		// 环境确实没有解释器时，必须返回带指引的失败提示而非空/挂起
		if !strings.Contains(last, "Python") {
			t.Fatalf("python_exec 失败信息应说明 Python 缺失: %s", last)
		}
		t.Skipf("环境无 Python 解释器（符合预期的降级路径）: %.100s", last)
	}
	if !strings.Contains(last, "42") {
		t.Fatalf("python_exec 应输出 42: %s", last)
	}
	t.Logf("python_exec ✓ %s", strings.ReplaceAll(last, "\n", " | "))
}

// TestE2ELargeWrite 验证写大文件不再被 token 上限截断成半截 JSON
func TestE2ELargeWrite(t *testing.T) {
	ctx := e2eSetup(t)
	session := e2eSession(t, ctx)

	_, msgs := runTurnWithRealModel(t, ctx, session,
		"用 write_file 生成 big.html：一个包含约 120 行重复 <p>hello world paragraph N</p> 的完整 HTML 页面，一次写完，然后告诉我写入了多少字符。", nil)

	outs := toolOutputs(t, msgs)
	for _, results := range outs {
		for _, r := range results {
			if strings.Contains(r, "参数解析失败") {
				t.Fatalf("出现参数截断: %s", r)
			}
		}
	}
	assertToolOk(t, outs, "write_file", "")
}

// TestE2EAttachmentRead 模拟"上传附件 → 对话引用"：附件应落入会话工作区，
// 模型凭文件名 read_file 即可读到内容（中文名也保持可用）
func TestE2EAttachmentRead(t *testing.T) {
	ctx := e2eSetup(t)
	session := e2eSession(t, ctx)

	// 模拟 api.UploadFile 的落盘产物：随机前缀存储名 + 原始文件名
	if err := os.MkdirAll(UploadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stored := fmt.Sprintf("e2e%d_说明文档.txt", time.Now().UnixNano())
	marker := "e2e-upload-marker-42"
	if err := os.WriteFile(filepath.Join(UploadsDir, stored),
		[]byte(marker+"\n这是上传附件的正文第二行。"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(filepath.Join(UploadsDir, stored)) })

	_, msgs := runTurnWithRealModel(t, ctx, session,
		"读取我上传的附件《说明文档.txt》，告诉我第一行的原文。", []Attachment{
			{URL: "/api/uploads/" + stored, Filename: "说明文档.txt", ContentType: "text/plain"},
		})

	outs := toolOutputs(t, msgs)
	assertToolOk(t, outs, "read_file", marker)
}
