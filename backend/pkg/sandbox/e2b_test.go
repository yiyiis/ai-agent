package sandbox

import (
	"bytes"
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
			"E2b-Sandbox-Id":   "sbx-1",
			"E2b-Sandbox-Port": "49983",
			"X-Access-Token":   "tok",
			"Authorization":    "Basic " + b64e("user:"),
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
	res := d.RunBash(context.Background(), "s1", "echo hi", nil, 30)
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
// ephemeral 工作区全链路：WriteFile 写入云端 → bash 读到并产出 → ReadFile 读回 → python3 → 销毁
func TestE2BLiveRealCloud(t *testing.T) {
	key := os.Getenv("E2B_LIVE_KEY")
	if key == "" {
		t.Skip("E2B_LIVE_KEY not set")
	}
	d := newE2BDriver(Config{E2BKey: key, E2BTemplate: "base", E2BTimeoutSec: 600})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	ws := t.TempDir()
	defer d.Close("live-s1")

	// 文件写入沙箱（ephemeral：无宿主镜像，字节只在云端）
	if err := d.WriteFile(ctx, "live-s1", "note.txt", []byte("hello-from-driver")); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	// bash 读到驱动写入的文件，并产出新文件
	res := d.RunBash(ctx, "live-s1", "cat note.txt && echo cloud-ok && echo made-in-cloud > made.txt", nil, 120)
	if res.Err != nil {
		t.Fatalf("bash: %v", res.Err)
	}
	if !bytes.Contains([]byte(res.Stdout), []byte("hello-from-driver")) || !bytes.Contains([]byte(res.Stdout), []byte("cloud-ok")) {
		t.Fatalf("bash output wrong: %+v", res)
	}

	// ReadFile 读回沙箱产出的文件（沙箱存活期间状态持续）
	data, err := d.ReadFile(ctx, "live-s1", "made.txt")
	if err != nil || string(data) != "made-in-cloud\n" {
		t.Fatalf("ReadFile wrong: %q %v", data, err)
	}
	// 宿主不落任何镜像
	if _, err := os.Stat(filepath.Join(ws, "made.txt")); !os.IsNotExist(err) {
		t.Fatal("ephemeral 模式不应在宿主留镜像")
	}

	// python3 可用
	res = d.RunBash(ctx, "live-s1", "python3 -c 'print(40+2)'", nil, 60)
	if res.Err != nil || !bytes.Contains([]byte(res.Stdout), []byte("42")) {
		t.Fatalf("python3 wrong: %+v", res)
	}

	// RunPython 全链路：脚本经驱动写入沙箱后执行
	if err := d.WriteFile(ctx, "live-s1", ".pyexec_check.py", []byte("print('py-ok')")); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	pres := d.RunPython(ctx, "live-s1", ".pyexec_check.py", nil, 60)
	if pres.Err != nil || !bytes.Contains([]byte(pres.Stdout), []byte("py-ok")) {
		t.Fatalf("RunPython wrong: %+v", pres)
	}
}
