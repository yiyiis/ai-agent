package api_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"backend/api"
	"backend/constants"
	"backend/pkg/db"
	"backend/pkg/jwt"
	"backend/pkg/skill"
)

// buildSkillZip 构造上传用 zip（条目：路径 → 内容）
func buildSkillZip(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// postSkillZip 构造 multipart 上传请求并执行
func postSkillZip(t *testing.T, url string, zipBytes []byte, confidential string, token string) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", "skill.zip")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(zipBytes); err != nil {
		t.Fatal(err)
	}
	if confidential != "" {
		_ = mw.WriteField("confidential", confidential)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", url, &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("x-token", token)
	testEngine.ServeHTTP(w, req)
	return w
}

func manageToken(t *testing.T, uid int64, name string) string {
	t.Helper()
	token, err := jwt.GenAccessToken(jwt.TokenClaims{
		UserID: uid, CompanyID: 1, Name: name, Username: name, UserType: constants.User,
	})
	if err != nil {
		t.Fatalf("gen token: %v", err)
	}
	return token
}

// TestSkillManageFlow 上传 → 详情 → 机密 → 版本 → 编辑 → 权限 → 删除 全链路。
// 技能库根指向临时目录，避免污染 backend/skills；注册行在测试末尾清理。
func TestSkillManageFlow(t *testing.T) {
	setupTestServer()

	oldRoot := skill.Root()
	root := t.TempDir()
	skill.Init(skill.Config{Dir: root})
	t.Cleanup(func() {
		skill.Init(skill.Config{Dir: oldRoot})
		q := db.Ctx(db.WithContext(context.Background()))
		sv := q.SkillVersion
		row, _ := q.Skill.WithContext(context.Background()).Where(q.Skill.DirName.Eq("demo-skill")).First()
		if row != nil {
			_, _ = sv.WithContext(context.Background()).Where(sv.SkillID.Eq(row.ID)).Delete()
			_, _ = q.Skill.WithContext(context.Background()).Where(q.Skill.ID.Eq(row.ID)).Delete()
		}
	})

	token1 := manageToken(t, 901, "管理甲")
	token2 := manageToken(t, 902, "管理乙")

	skillZip := buildSkillZip(t, map[string]string{
		"demo-skill/SKILL.md":       "---\nname: demo-skill\ndescription: 上传演示\n---\n# Demo\n用法见 scripts",
		"demo-skill/scripts/run.py": "print('run')",
		"demo-skill/secrets.env":    "DEMO_KEY=from-zip\n",
	})

	// 1. 上传成功：裸 JSON（无信封），v1 激活
	w := postSkillZip(t, "/api/skills/upload", skillZip, "false", token1)
	if w.Code != 200 {
		t.Fatalf("upload status=%d body=%s", w.Code, w.Body.String())
	}
	var uploaded struct {
		Skill                api.SkillOut `json:"skill"`
		CreatedVersionNumber int64    `json:"created_version_number"`
		Activated            bool     `json:"activated"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &uploaded); err != nil {
		t.Fatalf("上传响应应为裸 JSON（前端不解信封）: %v\nbody=%s", err, w.Body.String())
	}
	if uploaded.Skill.ID == 0 || uploaded.Skill.Name != "demo-skill" || !uploaded.Skill.IsOwner {
		t.Fatalf("上传返回技能异常: %+v", uploaded.Skill)
	}
	if uploaded.CreatedVersionNumber != 1 || !uploaded.Activated {
		t.Fatalf("首传应为激活的 v1: %+v", uploaded)
	}
	skillID := uploaded.Skill.ID

	// zip 携带的 secrets 已抽出且工作副本无 secrets.env
	secrets := skill.ReadSecrets("demo-skill")
	if secrets["DEMO_KEY"] != "from-zip" {
		t.Fatalf("zip secrets 未落盘: %+v", secrets)
	}
	if _, err := os.Stat(filepath.Join(root, "demo-skill", "secrets.env")); !os.IsNotExist(err) {
		t.Fatal("secrets.env 不应进工作副本")
	}

	// 2. 他人同名上传被拒
	w = postSkillZip(t, "/api/skills/upload", skillZip, "false", token2)
	env := decodeEnvelope(t, w)
	if env.Code == 0 || bytes.Contains(w.Body.Bytes(), []byte(`"skill"`)) {
		t.Fatalf("他人覆盖应走信封报错: code=%d msg=%s", env.Code, env.Msg)
	}

	// 3. 详情：所有者可见正文与机密键名
	w = httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/skills/"+strconv.FormatInt(skillID, 10)+"/body", nil)
	req.Header.Set("x-token", token1)
	testEngine.ServeHTTP(w, req)
	env = decodeEnvelope(t, w)
	if env.Code != 0 {
		t.Fatalf("body: %s", env.Msg)
	}
	var body api.SkillBodyOut
	if err := json.Unmarshal(env.Data, &body); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains([]byte(body.Body), []byte("# Demo")) || !body.HasSecrets || body.SecretKeys[0] != "DEMO_KEY" {
		t.Fatalf("body 异常: %+v", body)
	}

	// 4. 机密整体替换（PUT 语义）+ 回读
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("PUT", "/api/skills/"+strconv.FormatInt(skillID, 10)+"/secrets", bytes.NewReader([]byte(`{"secrets":{"NEW_KEY":"v2"}}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-token", token1)
	testEngine.ServeHTTP(w, req)
	if env = decodeEnvelope(t, w); env.Code != 0 {
		t.Fatalf("put secrets: %s", env.Msg)
	}
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("GET", "/api/skills/"+strconv.FormatInt(skillID, 10)+"/secrets", nil)
	req.Header.Set("x-token", token1)
	testEngine.ServeHTTP(w, req)
	env = decodeEnvelope(t, w)
	var got map[string]string
	if err := json.Unmarshal(env.Data, &got); err != nil || got["NEW_KEY"] != "v2" || len(got) != 1 {
		t.Fatalf("secrets 回读应为全量替换: %v %v", got, err)
	}

	// 5. 版本历史：v1 当前生效
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("GET", "/api/skills/"+strconv.FormatInt(skillID, 10)+"/versions", nil)
	req.Header.Set("x-token", token1)
	testEngine.ServeHTTP(w, req)
	env = decodeEnvelope(t, w)
	var versions []api.SkillVersionOut
	if err := json.Unmarshal(env.Data, &versions); err != nil || len(versions) != 1 {
		t.Fatalf("versions: %v %s", err, env.Data)
	}
	if !versions[0].IsCurrent || versions[0].Source != "upload" || versions[0].CreatedByName != "管理甲" {
		t.Fatalf("版本元数据异常: %+v", versions[0])
	}

	// 6. PATCH 设为机密 → 非所有者预览被拒、所有者照常
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("PATCH", "/api/skills/"+strconv.FormatInt(skillID, 10), bytes.NewReader([]byte(`{"confidential":true}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-token", token1)
	testEngine.ServeHTTP(w, req)
	env = decodeEnvelope(t, w)
	if env.Code != 0 {
		t.Fatalf("patch: %s", env.Msg)
	}

	w = httptest.NewRecorder()
	req, _ = http.NewRequest("GET", "/api/skills/"+strconv.FormatInt(skillID, 10)+"/body", nil)
	req.Header.Set("x-token", token2)
	testEngine.ServeHTTP(w, req)
	if env = decodeEnvelope(t, w); env.Code == 0 {
		t.Fatal("机密技能非所有者预览应被拒")
	}
	// 他人删 / 改机密同样被拒；但机密读取返回空表（详情弹窗并行拉取，报错会打挂弹窗）
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("DELETE", "/api/skills/"+strconv.FormatInt(skillID, 10), nil)
	req.Header.Set("x-token", token2)
	testEngine.ServeHTTP(w, req)
	if env = decodeEnvelope(t, w); env.Code == 0 {
		t.Fatal("非所有者删除应被拒")
	}
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("PUT", "/api/skills/"+strconv.FormatInt(skillID, 10)+"/secrets", bytes.NewReader([]byte(`{"secrets":{"X":"1"}}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-token", token2)
	testEngine.ServeHTTP(w, req)
	if env = decodeEnvelope(t, w); env.Code == 0 {
		t.Fatal("非所有者改机密应被拒")
	}
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("GET", "/api/skills/"+strconv.FormatInt(skillID, 10)+"/secrets", nil)
	req.Header.Set("x-token", token2)
	testEngine.ServeHTTP(w, req)
	env = decodeEnvelope(t, w)
	var stranger map[string]string
	if env.Code != 0 || json.Unmarshal(env.Data, &stranger) != nil || len(stranger) != 0 {
		t.Fatalf("非所有者读机密应为空表: code=%d %s", env.Code, env.Data)
	}

	// 7. 同名升级：v2，版本历史 +1
	upgradeZip := buildSkillZip(t, map[string]string{
		"demo-skill/SKILL.md": "---\nname: demo-skill\ndescription: 升级版\n---\n# v2",
	})
	w = postSkillZip(t, "/api/skills/upload", upgradeZip, "true", token1)
	if w.Code != 200 {
		t.Fatalf("upgrade status=%d body=%s", w.Code, w.Body.String())
	}
	var upgraded struct {
		CreatedVersionNumber int64 `json:"created_version_number"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &upgraded); err != nil || upgraded.CreatedVersionNumber != 2 {
		t.Fatalf("升级应为 v2: %v %s", err, w.Body.String())
	}

	// 8. 所有者删除：目录、注册行、版本一并清理
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("DELETE", "/api/skills/"+strconv.FormatInt(skillID, 10), nil)
	req.Header.Set("x-token", token1)
	testEngine.ServeHTTP(w, req)
	if env = decodeEnvelope(t, w); env.Code != 0 {
		t.Fatalf("delete: %s", env.Msg)
	}
	if _, err := os.Stat(filepath.Join(root, "demo-skill")); !os.IsNotExist(err) {
		t.Fatal("删除后技能目录应不存在")
	}
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("GET", "/api/skills/"+strconv.FormatInt(skillID, 10)+"/body", nil)
	req.Header.Set("x-token", token1)
	testEngine.ServeHTTP(w, req)
	if env = decodeEnvelope(t, w); env.Code == 0 {
		t.Fatal("删除后详情应报技能不存在")
	}
}


