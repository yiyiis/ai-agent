package sandbox

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// frame 构造一条 docker raw stream 复用帧（stream: 1=stdout 2=stderr）
func frame(stream byte, payload string) []byte {
	head := make([]byte, 8)
	head[0] = stream
	binary.BigEndian.PutUint32(head[4:], uint32(len(payload)))
	return append(head, payload...)
}

func TestDemuxStream(t *testing.T) {
	var raw []byte
	raw = append(raw, frame(1, "out1")...)
	raw = append(raw, frame(2, "err1")...)
	raw = append(raw, frame(1, "out2")...)
	raw = append(raw, frame(1, "")...) // 空载荷应被跳过

	var stdout, stderr bytes.Buffer
	if err := demuxStream(bytes.NewReader(raw), &stdout, &stderr); err != nil {
		t.Fatalf("demux: %v", err)
	}
	if stdout.String() != "out1out2" || stderr.String() != "err1" {
		t.Fatalf("demux wrong: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestDemuxStreamTruncated(t *testing.T) {
	raw := frame(1, "abcdef")
	raw = raw[:len(raw)-3] // 截掉载荷尾部：按流结束容忍
	var stdout, stderr bytes.Buffer
	if err := demuxStream(bytes.NewReader(raw), &stdout, &stderr); err != nil {
		t.Fatalf("truncated frame should be tolerated: %v", err)
	}
}

func TestContainerSpec(t *testing.T) {
	d := &dockerDriver{cfg: Config{
		Image:       "img:1",
		Env:         []string{"K=V"},
		NetworkMode: "none",
		MemoryMB:    512,
		CPUS:        1.5,
		PidsLimit:   256,
	}}
	spec := d.containerSpec("sess-1")
	if spec.WorkingDir != "/workspace" {
		t.Fatalf("workdir: %q", spec.WorkingDir)
	}
	if len(spec.Env) != 2 || spec.Env[0] != "AI_AGENT_SESSION=sess-1" || spec.Env[1] != "K=V" {
		t.Fatalf("env: %v", spec.Env)
	}
	if spec.Labels[labelManaged] != "1" || spec.Labels["ai-agent-session"] != "sess-1" {
		t.Fatalf("labels: %v", spec.Labels)
	}
	if fmt.Sprint(spec.Cmd) != "[sleep infinity]" {
		t.Fatalf("cmd: %v", spec.Cmd)
	}
	// 工作区不再 bind 挂载（文件经 /archive API 传输，与 e2b 模型统一）
	if len(spec.HostConfig.Binds) != 0 {
		t.Fatalf("workspace should not be bind-mounted: %v", spec.HostConfig.Binds)
	}
	if spec.HostConfig.Memory != 512<<20 || spec.HostConfig.NanoCPUs != 1500000000 {
		t.Fatalf("resource limits wrong: %+v", spec.HostConfig)
	}
	if spec.HostConfig.PidsLimit == nil || *spec.HostConfig.PidsLimit != 256 {
		t.Fatalf("pids limit wrong: %+v", spec.HostConfig.PidsLimit)
	}
}

func TestTarSingleFileRoundTrip(t *testing.T) {
	tarball, err := tarSingleFile("docs/report.md", []byte("# hello"))
	if err != nil {
		t.Fatal(err)
	}
	data, err := readTarSingle(tarball)
	if err != nil || string(data) != "# hello" {
		t.Fatalf("round trip wrong: %q %v", data, err)
	}
	if _, err := cleanRel("../escape"); err == nil {
		t.Fatal("cleanRel should reject escape")
	}
	if r, err := cleanRel("a/b.txt"); err != nil || r != "a/b.txt" {
		t.Fatalf("cleanRel wrong: %q %v", r, err)
	}
}

// TestDockerAutoPullOnMissingImage create 404（No such image）→ 自动拉取 → 重试创建
func TestDockerAutoPullOnMissingImage(t *testing.T) {
	var pulled, created atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("GET /containers/ai-agent-sbx-s9/json", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	mux.HandleFunc("POST /containers/create", func(w http.ResponseWriter, r *http.Request) {
		if created.Add(1) == 1 {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"No such image: img:pull-me"}`))
			return
		}
		_, _ = w.Write([]byte(`{"Id":"ctr-9"}`))
	})
	mux.HandleFunc("POST /images/create", func(w http.ResponseWriter, r *http.Request) {
		pulled.Add(1)
		if r.URL.Query().Get("fromImage") != "img:pull-me" {
			t.Errorf("pull ref wrong: %s", r.URL.RawQuery)
		}
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("POST /containers/ctr-9/start", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("PUT /containers/ctr-9/archive", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK) // 工作区镜像推送（tar 流，此处不校验内容）
	})
	mux.HandleFunc("GET /containers/ctr-9/archive", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK) // 回拉 tar：空体即无可变更文件
	})
	mux.HandleFunc("POST /containers/ctr-9/exec", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"Id":"e9"}`))
	})
	mux.HandleFunc("POST /exec/e9/start", func(w http.ResponseWriter, r *http.Request) {
		w.Write(frame(1, "after-pull\n"))
	})
	mux.HandleFunc("GET /exec/e9/json", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ExitCode":0,"Running":false}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected call: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	d := &dockerDriver{cfg: Config{Image: "img:pull-me"}, client: newDockerClient(srv.URL), ctrs: map[string]*ctrState{}}
	res := d.RunBash(context.Background(), "s9", "echo after-pull", 10)
	if res.Err != nil {
		t.Fatalf("run: %v", res.Err)
	}
	if !strings.Contains(res.Stdout, "after-pull") {
		t.Fatalf("stdout wrong: %+v", res)
	}
	if pulled.Load() != 1 || created.Load() != 2 {
		t.Fatalf("pull=%d create=%d, want 1/2", pulled.Load(), created.Load())
	}
}

func TestReapOnceForceRemovesIdleContainers(t *testing.T) {
	var removed atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			removed.Add(1)
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	d := &dockerDriver{
		cfg:         Config{IdleTimeoutSec: 60, MaxLifetimeSec: 3600},
		idleTimeout: time.Minute,
		maxLifetime: time.Hour,
		client:      newDockerClient(srv.URL),
		ctrs:        map[string]*ctrState{},
	}
	now := time.Now()
	d.ctrs["busy"] = &ctrState{id: "c1", created: now.Add(-time.Hour), lastUsed: now}
	d.ctrs["idle"] = &ctrState{id: "c2", created: now.Add(-time.Hour), lastUsed: now.Add(-time.Hour)} // 超空闲
	d.ctrs["stale"] = &ctrState{id: "c3", created: now.Add(-2 * time.Hour), lastUsed: now}            // 超最长存活

	d.reapOnce()

	if got := removed.Load(); got != 2 {
		t.Fatalf("should remove 2 containers, got %d", got)
	}
	if _, ok := d.ctrs["busy"]; !ok {
		t.Fatal("busy container should survive")
	}
	if len(d.ctrs) != 1 {
		t.Fatalf("remaining containers: %v", d.ctrs)
	}
}

// TestDockerRunAgainstFakeDaemon 用假 Engine API 走通 run 主链路：
// inspect(404) → create → start → exec create → exec start(流) → exec inspect(退出码)
func TestDockerRunAgainstFakeDaemon(t *testing.T) {
	mux := http.NewServeMux()
	var created atomic.Bool
	var exitCode atomic.Int32
	mux.HandleFunc("GET /containers/ai-agent-sbx-s1/json", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	mux.HandleFunc("POST /containers/create", func(w http.ResponseWriter, r *http.Request) {
		var spec containerCreateReq
		_ = json.NewDecoder(r.Body).Decode(&spec)
		if spec.Image != "img:test" {
			t.Errorf("image wrong: %s", spec.Image)
		}
		if len(spec.HostConfig.Binds) != 0 {
			t.Errorf("workspace should not be bind-mounted: %v", spec.HostConfig.Binds)
		}
		created.Store(true)
		_, _ = w.Write([]byte(`{"Id":"ctr-1"}`))
	})
	mux.HandleFunc("POST /containers/ctr-1/start", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("PUT /containers/ctr-1/archive", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /containers/ctr-1/archive", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK) // 空 tar：无可回拉变更
	})
	mux.HandleFunc("POST /containers/ctr-1/exec", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		cmd, _ := body["Cmd"].([]any)
		// 外层 timeout 包裹（timeout -k 5 <secs>）+ 内层命令（仅校验主命令）
		if len(cmd) > 4 && cmd[0] == "timeout" && cmd[4] == "bash" && cmd[1] != "-k" {
			t.Errorf("cmd wrong: %v", cmd)
		}
		_, _ = w.Write([]byte(`{"Id":"exec-1"}`))
	})
	mux.HandleFunc("POST /exec/exec-1/start", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.docker.raw-stream")
		_, _ = w.Write(frame(1, "hi\n"))
		_, _ = w.Write(frame(2, "warn"))
	})
	mux.HandleFunc("GET /exec/exec-1/json", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(fmt.Sprintf(`{"ExitCode":%d,"Running":false}`, exitCode.Load())))
	})
	mux.HandleFunc("GET /containers/json", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[]`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected call: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	d := &dockerDriver{cfg: Config{Image: "img:test"}, client: newDockerClient(srv.URL), ctrs: map[string]*ctrState{}}
	res := d.RunBash(context.Background(), "s1", "echo hi", 30)
	if res.Err != nil {
		t.Fatalf("run: %v", res.Err)
	}
	if res.Stdout != "hi\n" || res.Stderr != "warn" || res.ExitCode != 0 || res.Killed {
		t.Fatalf("result wrong: %+v", res)
	}
	if !created.Load() {
		t.Fatal("container should have been created on demand")
	}
	if d.ctrs["s1"] == nil {
		t.Fatal("container state should be registered")
	}

	// 超时退出码（124）应翻译为 Killed
	exitCode.Store(124)
	res = d.RunBash(context.Background(), "s1", "sleep 999", 1)
	if !res.Killed {
		t.Fatalf("exit 124 should map to killed: %+v", res)
	}
}

// TestDockerIntegrationRealDaemon 真实 Docker 冒烟（需本地 Docker 且镜像可拉取）：
// 无守护进程时跳过。验证容器拉起、bind 挂载与 exec 执行的真实链路。
func TestDockerIntegrationRealDaemon(t *testing.T) {
	c := newDockerClient("")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	resp, err := c.do(ctx, http.MethodGet, "/_ping", nil, nil)
	if err != nil {
		t.Skipf("docker daemon unreachable: %v", err)
	}
	resp.Body.Close()

	d := &dockerDriver{cfg: Config{Image: defaultDockerImage}, client: c, ctrs: map[string]*ctrState{}}
	defer d.Close("it-session")
	res := d.RunBash(context.Background(), "it-session", "echo ok-from-container && python3 -c 'print(1+1)'", 60)
	if res.Err != nil {
		t.Skipf("docker exec failed (image pull needed?): %v", res.Err)
	}
	if res.ExitCode != 0 || !bytes.Contains([]byte(res.Stdout), []byte("ok-from-container")) {
		t.Fatalf("unexpected result: %+v", res)
	}
}
