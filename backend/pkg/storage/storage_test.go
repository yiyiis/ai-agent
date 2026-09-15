package storage

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// sha1Hex / hmacSHA1Hex 签名测试用的独立推演助手（与实现同算法、分离书写，防止结构性回归）
func sha1Hex(s string) string {
	sum := sha1.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

func hmacSHA1Hex(key, s string) string {
	mac := hmac.New(sha1.New, []byte(key))
	mac.Write([]byte(s))
	return hex.EncodeToString(mac.Sum(nil))
}

func TestSanitizeName(t *testing.T) {
	got := SanitizeName("年度报告 final..pptx")
	if !strings.HasPrefix(got, "0") && !strings.Contains(got, "_") {
		t.Fatalf("random prefix missing: %q", got)
	}
	if !strings.HasSuffix(got, ".pptx") {
		t.Fatalf("ext lost: %q", got)
	}
	if got == SanitizeName("年度报告 final..pptx") {
		t.Fatal("same input should yield different random names")
	}
	// 非法字符逐个替换为下划线（与原 api/uploads storedName 行为一致）
	if b := SanitizeName("///"); !strings.HasSuffix(b, "__") {
		t.Fatalf("unsafe chars should be replaced: %q", b)
	}
}

func TestLocalStoragePut(t *testing.T) {
	root := t.TempDir()
	old := LocalRoot
	LocalRoot = root
	t.Cleanup(func() { LocalRoot = old })

	obj, err := localStorage{}.Put(context.Background(), "a b.txt", strings.NewReader("hello"), 5, "text/plain")
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if !strings.HasPrefix(obj.URL, "/api/uploads/") {
		t.Fatalf("url wrong: %q", obj.URL)
	}
	data, err := os.ReadFile(filepath.Join(root, filepath.Base(obj.URL)))
	if err != nil || string(data) != "hello" {
		t.Fatalf("stored content wrong: %v %q", err, data)
	}

	if _, err := (localStorage{}).Put(context.Background(), "x", strings.NewReader(""), 0, ""); err == nil {
		t.Fatal("empty should be rejected")
	}
	if _, err := (localStorage{}).Put(context.Background(), "x", strings.NewReader("x"), MaxBytes+1, ""); err == nil {
		t.Fatal("oversize should be rejected")
	}
}

// TestCOSAuthorizationDeterministic 固定密钥与时间窗下签名字段应完全可复现，
// 防止签名拼接被无意改动（结构与算法以独立步骤重新推演，不调用被测函数内部零件）
func TestCOSAuthorizationDeterministic(t *testing.T) {
	now := time.Unix(1700000000, 0)
	got := cosAuthorization("AKIDtest", "secret-test", "PUT", "/a/b.txt", now)

	keyTime := "1700000000;1700000600"
	httpStringSum := sha1Hex("PUT\n/a/b.txt\n\n\n")
	sts := "sha1\n" + keyTime + "\n" + httpStringSum + "\n"
	wantSig := hmacSHA1Hex(hmacSHA1Hex("secret-test", keyTime), sts)
	want := "q-sign-algorithm=sha1&q-ak=AKIDtest&q-sign-time=" + keyTime +
		"&q-key-time=" + keyTime + "&q-header-list=&q-url-param-list=&q-signature=" + wantSig

	if got != want {
		t.Fatalf("signature mismatch:\n got %s\nwant %s", got, want)
	}
}

func TestCOSStoragePutRoundTrip(t *testing.T) {
	var gotPath, gotAuth, gotBody string
	var gotLen int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		gotLen = r.ContentLength
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	c := newCOSStorage(Config{
		SecretID: "AKIDx", SecretKey: "k", Bucket: "b", Region: "r",
		BasePath: "ai-agent", Endpoint: srv.URL,
	})
	obj, err := c.Put(context.Background(), "report.md", strings.NewReader("# hi"), 4, "text/markdown")
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if !strings.HasPrefix(gotPath, "/ai-agent/") || !strings.HasSuffix(gotPath, "report.md") {
		t.Fatalf("path wrong: %q", gotPath)
	}
	if gotAuth == "" || !strings.Contains(gotAuth, "q-ak=AKIDx") {
		t.Fatalf("auth header wrong: %q", gotAuth)
	}
	if gotBody != "# hi" || gotLen != 4 {
		t.Fatalf("body/len wrong: %q %d", gotBody, gotLen)
	}
	if obj.URL != srv.URL+gotPath {
		t.Fatalf("object url wrong: %q vs path %q", obj.URL, gotPath)
	}
}

func TestCOSStoragePutServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("<?xml?><Error><Code>AccessDenied</Code></Error>"))
	}))
	t.Cleanup(srv.Close)

	c := newCOSStorage(Config{SecretID: "a", SecretKey: "b", Bucket: "x", Region: "y", Endpoint: srv.URL})
	if _, err := c.Put(context.Background(), "f.txt", strings.NewReader("x"), 1, ""); err == nil {
		t.Fatal("server error should propagate")
	}
}
