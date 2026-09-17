package api_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"backend/api"
	"backend/pkg/db"
	"backend/pkg/skill"
)

// TestSkillFileEditorFlow 从空新建 → 在线编辑（tree / 读 / 写 / 新建 / 重命名）→ 下载 → 删除
func TestSkillFileEditorFlow(t *testing.T) {
	setupTestServer()

	oldRoot := skill.Root()
	root := t.TempDir()
	skill.Init(skill.Config{Dir: root})
	t.Cleanup(func() {
		skill.Init(skill.Config{Dir: oldRoot})
		q := db.Ctx(db.WithContext(context.Background()))
		sv := q.SkillVersion
		ctx := context.Background()
		row, _ := q.Skill.WithContext(ctx).Where(q.Skill.DirName.Eq("blank-tool")).First()
		if row != nil {
			_, _ = sv.WithContext(ctx).Where(sv.SkillID.Eq(row.ID)).Delete()
			_, _ = q.Skill.WithContext(ctx).Where(q.Skill.ID.Eq(row.ID)).Delete()
		}
	})

	token1 := manageToken(t, 911, "编辑甲")
	token2 := manageToken(t, 912, "编辑乙")

	do := func(method, path, token string, body []byte) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		var req *http.Request
		if body != nil {
			req, _ = http.NewRequest(method, path, bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
		} else {
			req, _ = http.NewRequest(method, path, nil)
		}
		req.Header.Set("x-token", token)
		testEngine.ServeHTTP(w, req)
		return w
	}

	// 1. 从空新建：骨架 SKILL.md + v1
	w := do("POST", "/api/skills", token1, []byte(`{"name":"Blank Tool","description":"空白技能"}`))
	env := decodeEnvelope(t, w)
	if env.Code != 0 {
		t.Fatalf("create empty: %s", env.Msg)
	}
	var created api.SkillOut
	if err := json.Unmarshal(env.Data, &created); err != nil || created.ID == 0 || !created.IsOwner {
		t.Fatalf("create 返回异常: %v %s", err, env.Data)
	}
	id := strconv.FormatInt(created.ID, 10)

	// 2. tree：仅骨架 SKILL.md
	w = do("GET", "/api/skills/"+id+"/tree", token1, nil)
	env = decodeEnvelope(t, w)
	var tree []skill.FileNode
	if err := json.Unmarshal(env.Data, &tree); err != nil || len(tree) != 1 || tree[0].Path != "SKILL.md" {
		t.Fatalf("tree 异常: %v %s", err, env.Data)
	}

	// 3. 新建 + 写 + 读回
	w = do("POST", "/api/skills/"+id+"/file?path=scripts/run.py", token1, []byte(`{"content":"print(1)"}`))
	if env = decodeEnvelope(t, w); env.Code != 0 {
		t.Fatalf("create file: %s", env.Msg)
	}
	w = do("POST", "/api/skills/"+id+"/file?path=scripts/run.py", token1, []byte(`{"content":"x"}`))
	if env = decodeEnvelope(t, w); env.Code == 0 {
		t.Fatal("重复新建应报错")
	}
	w = do("GET", "/api/skills/"+id+"/file?path=scripts/run.py", token1, nil)
	env = decodeEnvelope(t, w)
	var content api.SkillFileContentOut
	if err := json.Unmarshal(env.Data, &content); err != nil || content.Content != "print(1)" || content.IsBinary {
		t.Fatalf("read file 异常: %v %s", err, env.Data)
	}

	// 4. 重命名
	w = do("POST", "/api/skills/"+id+"/file/rename", token1,
		[]byte(`{"old_path":"scripts/run.py","new_path":"scripts/main.py"}`))
	if env = decodeEnvelope(t, w); env.Code != 0 {
		t.Fatalf("rename: %s", env.Msg)
	}

	// 5. 非所有者只读可浏览、不可改
	w = do("GET", "/api/skills/"+id+"/tree", token2, nil)
	if env = decodeEnvelope(t, w); env.Code != 0 {
		t.Fatalf("非所有者应可浏览 tree: %s", env.Msg)
	}
	w = do("PUT", "/api/skills/"+id+"/file?path=SKILL.md", token2, []byte(`{"content":"篡改"}`))
	if env = decodeEnvelope(t, w); env.Code == 0 {
		t.Fatal("非所有者写入应被拒")
	}

	// 6. 下载：zip 字节流 + Content-Disposition 文件名
	w = do("GET", "/api/skills/"+id+"/download", token1, nil)
	if w.Code != 200 {
		t.Fatalf("download status=%d", w.Code)
	}
	dispo := w.Header().Get("Content-Disposition")
	if !strings.Contains(dispo, "Blank Tool") && !strings.Contains(dispo, "Blank%20Tool") {
		t.Fatalf("Content-Disposition 缺文件名: %q", dispo)
	}
	zr, err := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
	if err != nil {
		t.Fatalf("下载内容不是合法 zip: %v", err)
	}
	names := map[string]bool{}
	for _, f := range zr.File {
		names[f.Name] = true
	}
	if !names["SKILL.md"] || !names["scripts/main.py"] {
		t.Fatalf("zip 内容异常: %v", names)
	}

	// 7. 清理：所有者删除
	w = do("DELETE", "/api/skills/"+id, token1, nil)
	if env = decodeEnvelope(t, w); env.Code != 0 {
		t.Fatalf("delete: %s", env.Msg)
	}
}
