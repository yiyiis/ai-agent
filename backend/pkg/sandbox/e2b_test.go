package sandbox

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestConnectFrameRoundTrip(t *testing.T) {
	payload := []byte(`{"event":{"start":{"pid":7}}}`)
	framed := connectFrame(nil, payload)
	if framed[0] != 0 || len(framed) != 5+len(payload) {
		t.Fatalf("frame header wrong: %v", framed[:5])
	}

	stream := append([]byte{}, framed...)
	stream = append(stream, connectFrame(nil, []byte(`{"event":{"end":{"exitCode":0,"exited":true}}}`))...)
	endJSON := []byte(`{"error":{"code":"internal","message":"boom"}}`)
	stream = append(stream, connectFrame(endJSON, endJSON)...)

	var msgs []string
	err := readConnectStream(bytes.NewReader(stream), func(frame []byte) error {
		msgs = append(msgs, string(frame))
		return nil
	})
	if err == nil || err.Error() != "connect error internal: boom" {
		t.Fatalf("end-stream error should surface: %v", err)
	}
	if len(msgs) != 2 || !bytes.Contains([]byte(msgs[0]), []byte(`"pid":7`)) {
		t.Fatalf("messages wrong: %v", msgs)
	}
}

func TestTarAndSnapshotHelpers(t *testing.T) {
	src := t.TempDir()
	write := func(rel, content string) {
		p := filepath.Join(src, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a.txt", "hello")
	write("nested/b.py", "print(1)")
	write(e2bSyncMarker, "skip")
	write(".pyexec_tmp.py", "skip")

	snap1, err := fileSnapshot(src)
	if err != nil {
		t.Fatal(err)
	}
	// marker 被跳过；.pyexec_* 临时脚本参与同步（e2b 驱动需上传执行）
	if len(snap1) != 3 {
		t.Fatalf("snapshot content wrong: %v", snap1)
	}

	data, err := tarWorkspace(src)
	if err != nil {
		t.Fatal(err)
	}

	dst := t.TempDir()
	if err := untarTo(dst, data); err != nil {
		t.Fatal(err)
	}
	for rel, want := range map[string]string{"a.txt": "hello", "nested/b.py": "print(1)"} {
		got, err := os.ReadFile(filepath.Join(dst, filepath.FromSlash(rel)))
		if err != nil || string(got) != want {
			t.Fatalf("%s wrong: %q %v", rel, got, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dst, e2bSyncMarker)); !os.IsNotExist(err) {
		t.Fatal("marker should be skipped in tar")
	}

	// 快照变更检测：mtime 更新后才认为变化
	snap2, _ := fileSnapshot(src)
	if !sameSnapshot(snap1, snap2) {
		t.Fatal("unchanged dir should yield same snapshot")
	}
	future := time.Now().Add(time.Second)
	if err := os.Chtimes(filepath.Join(src, "a.txt"), future, future); err != nil {
		t.Fatal(err)
	}
	snap3, _ := fileSnapshot(src)
	if sameSnapshot(snap1, snap3) {
		t.Fatal("touched file should change snapshot")
	}

	// 解包时的路径穿越防护
	var evil bytes.Buffer
	gw := gzip.NewWriter(&evil)
	tw := tar.NewWriter(gw)
	hdr := &tar.Header{Name: "../../evil.txt", Typeflag: tar.TypeReg, Size: 3}
	_ = tw.WriteHeader(hdr)
	_, _ = tw.Write([]byte("bad"))
	_ = tw.Close()
	_ = gw.Close()
	if err := untarTo(dst, evil.Bytes()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dst, "..", "..", "evil.txt")); !os.IsNotExist(err) {
		t.Fatal("path traversal should be blocked")
	}
}

// TestE2BRunAgainstFakeEnvd 假 E2B 服务走协议主链路：
// 创建（API）→ Start（Connect 帧 + 头路由 + base64 输出）→ exitCode 映射
func TestE2BRunAgainstFakeEnvd(t *testing.T) {
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-Key") != "k1" {
			t.Errorf("api call wrong: %s %s", r.Method, r.URL.Path)
		}
		switch r.URL.Path {
		case "/sandboxes": // 创建
			_ = json.NewDecoder(r.Body).Decode(&map[string]any{})
			_, _ = w.Write([]byte(`{"sandboxID":"sbx-1","clientID":"c","envdVersion":"1.0.0","envdAccessToken":"tok"}`))
		case "/sandboxes/sbx-1": // Close 时的销毁
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected api call: %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(apiSrv.Close)

	var startCalls atomic.Int32
	var lastStart startRequest
	envdSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for k, want := range map[string]string{
			"E2b-Sandbox-Id":    "sbx-1",
			"E2b-Sandbox-Port":  "49983",
			"X-Access-Token":    "tok",
			"Authorization":     "Basic " + b64e("user:"),
		} {
			if got := r.Header.Get(k); got != want {
				t.Errorf("header %s = %q, want %q", k, got, want)
			}
		}
		switch r.URL.Path {
		case "/process.Process/Start":
			startCalls.Add(1)
			body, _ := io.ReadAll(r.Body)
			// 请求体应为单条信封帧
			if len(body) < 5 || body[0] != 0 {
				t.Errorf("request not connect-framed: %v", body[:min(5, len(body))])
			}
			// 主命令带 timeout 包裹，同步辅助命令是裸 bash——只记录主命令
			if bytes.Contains(body, []byte("/usr/bin/timeout")) {
				_ = json.Unmarshal(body[5:], &lastStart)
			}

			w.Header().Set("Content-Type", "application/connect+json")
			_, _ = w.Write(connectFrame(nil, []byte(`{"event":{"start":{"pid":9}}}`)))
			_, _ = w.Write(connectFrame(nil, []byte(`{"event":{"data":{"stdout":"`+b64e("hi\n")+`"}}}`)))
			_, _ = w.Write(connectFrame(nil, []byte(`{"event":{"data":{"stderr":"`+b64e("warn")+`"}}}`)))
			_, _ = w.Write(connectFrame(nil, []byte(`{"event":{"end":{"exitCode":0,"exited":true}}}`)))
			_, _ = w.Write(connectFrame([]byte(`{}`), []byte(`{}`)))
		case "/files":
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected envd call: %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(envdSrv.Close)

	d := &e2bDriver{
		cfg:      Config{E2BKey: "k1", E2BTemplate: "base"},
		apiBase:  apiSrv.URL,
		envdBase: envdSrv.URL,
		http:     &http.Client{},
		boxes:    map[string]*e2bBox{},
	}
	ws := t.TempDir()
	res := d.RunBash(context.Background(), "s1", ws, "echo hi", 30)
	if res.Err != nil {
		t.Fatalf("run: %v", res.Err)
	}
	if res.Stdout != "hi\n" || res.Stderr != "warn" || res.ExitCode != 0 || res.Killed {
		t.Fatalf("result wrong: %+v", res)
	}
	// 外层 timeout 包裹 + /bin/bash -l -c
	if lastStart.Process.Cmd != "/usr/bin/timeout" || lastStart.Process.Args[3] != "/bin/bash" ||
		lastStart.Process.Args[4] != "-l" || lastStart.Process.Args[5] != "-c" {
		t.Fatalf("argv wrong: %+v", lastStart.Process)
	}
	if lastStart.Process.Cwd != "/workspace" {
		t.Fatalf("cwd wrong: %s", lastStart.Process.Cwd)
	}
	if startCalls.Load() == 0 {
		t.Fatal("no start call recorded")
	}
	d.Close("s1")
}

func b64e(s string) string {
	return base64.StdEncoding.EncodeToString([]byte(s))
}

// TestE2BLiveRealCloud 真实 E2B 联调（需 E2B_LIVE_KEY；按需执行）：
// 创建云端沙箱 → 工作区上行同步 → bash 读到宿主文件 → 沙箱产出文件回传宿主 → python3 执行 → 销毁
func TestE2BLiveRealCloud(t *testing.T) {
	key := os.Getenv("E2B_LIVE_KEY")
	if key == "" {
		t.Skip("E2B_LIVE_KEY not set")
	}
	d := newE2BDriver(Config{E2BKey: key, E2BTemplate: "base", E2BTimeoutSec: 600})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "note.txt"), []byte("hello-from-host"), 0o644); err != nil {
		t.Fatal(err)
	}
	defer d.Close("live-s1")

	// 上行同步 + bash 读取
	res := d.RunBash(ctx, "live-s1", ws, "cat note.txt && echo cloud-ok && echo made-in-cloud > made.txt && printf 'bin' > bin.dat", 120)
	if res.Err != nil {
		t.Fatalf("bash: %v", res.Err)
	}
	if !bytes.Contains([]byte(res.Stdout), []byte("hello-from-host")) || !bytes.Contains([]byte(res.Stdout), []byte("cloud-ok")) {
		t.Fatalf("bash output wrong: %+v", res)
	}

	// 下行同步：沙箱产出的文件回到宿主
	res = d.RunBash(ctx, "live-s1", ws, "test -f made.txt && echo pulled", 60)
	if res.Err != nil || !bytes.Contains([]byte(res.Stdout), []byte("pulled")) {
		t.Fatalf("sync down wrong: %+v", res)
	}
	if data, err := os.ReadFile(filepath.Join(ws, "made.txt")); err != nil || string(data) != "made-in-cloud\n" {
		t.Fatalf("made.txt not pulled back: %q %v", data, err)
	}

	// python3 可用
	res = d.RunBash(ctx, "live-s1", ws, "python3 -c 'print(40+2)'", 60)
	if res.Err != nil || !bytes.Contains([]byte(res.Stdout), []byte("42")) {
		t.Fatalf("python3 wrong: %+v", res)
	}

	// RunPython 全链路（脚本由 tools 层写入宿主工作区，驱动负责上行）
	if err := os.WriteFile(filepath.Join(ws, ".pyexec_check.py"), []byte("print('py-ok')"), 0o644); err != nil {
		t.Fatal(err)
	}
	pres := d.RunPython(ctx, "live-s1", ws, ".pyexec_check.py", 60)
	if pres.Err != nil || !bytes.Contains([]byte(pres.Stdout), []byte("py-ok")) {
		t.Fatalf("RunPython wrong: %+v", pres)
	}
}
