package sandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestDockerCancellationWaitsForCleanupAndQuarantinesFailures(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(fmt.Sprint(failed), func(t *testing.T) {
			entered, cleanupEntered, releaseCleanup := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var created atomic.Int32
			mux := http.NewServeMux()
			mux.HandleFunc("POST /containers/ctr/exec", func(w http.ResponseWriter, r *http.Request) {
				var payload struct{ Cmd []string }
				_ = json.NewDecoder(r.Body).Decode(&payload)
				id := "main"
				if len(payload.Cmd) > 2 && payload.Cmd[2] == remoteProcessCleanup {
					id = "cleanup"
				}
				created.Add(1)
				fmt.Fprintf(w, `{"Id":%q}`, id)
			})
			mux.HandleFunc("POST /exec/main/start", func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(200)
				w.(http.Flusher).Flush()
				close(entered)
				<-r.Context().Done()
			})
			mux.HandleFunc("POST /exec/cleanup/start", func(w http.ResponseWriter, r *http.Request) {
				close(cleanupEntered)
				<-releaseCleanup
				if failed {
					w.WriteHeader(500)
					return
				}
				_, _ = w.Write(frame(1, ""))
			})
			mux.HandleFunc("GET /exec/cleanup/json", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"Running":false,"ExitCode":0}`)) })
			srv := httptest.NewServer(mux)
			defer srv.Close()
			d := &dockerDriver{client: newDockerClient(srv.URL), ctrs: map[string]*ctrState{"s": {id: "ctr"}}}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			result := make(chan Result, 1)
			go func() { result <- d.RunBash(ctx, "s", "echo private", []string{"TOKEN=private"}, 30) }()
			<-entered
			cancel()
			<-cleanupEntered
			select {
			case <-result:
				t.Fatal("returned before remote cleanup acknowledged")
			default:
			}
			close(releaseCleanup)
			select {
			case res := <-result:
				if res.Err == nil || !res.Killed {
					t.Fatalf("cancelled command looked successful: %+v", res)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("cleanup did not finish")
			}
			if failed {
				if _, err := d.ensure(context.Background(), "s"); err == nil {
					t.Fatal("unconfirmed process left workspace usable")
				}
				if err := d.WriteFile(context.Background(), "s", "x", []byte("x")); err == nil {
					t.Fatal("quarantined write accepted")
				}
				if _, err := d.ReadFile(context.Background(), "s", "x"); err == nil {
					t.Fatal("quarantined read accepted")
				}
				d.RemoveFile("s", "x")
				if created.Load() != 2 {
					t.Fatal("quarantined cleanup executed another command")
				}
			} else if _, err := d.ensure(context.Background(), "s"); err != nil {
				t.Fatalf("confirmed cleanup quarantined workspace: %v", err)
			}
		})
	}
}

func TestE2BCancellationWaitsForCleanupAndQuarantinesFailures(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(fmt.Sprint(failed), func(t *testing.T) {
			entered, cleanupEntered, releaseCleanup := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var requests atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				body, _ := io.ReadAll(r.Body)
				var request startRequest
				if len(body) < 5 || json.Unmarshal(body[5:], &request) != nil {
					t.Error("invalid request")
					w.WriteHeader(400)
					return
				}
				if len(request.Process.Args) > 1 && request.Process.Args[1] == remoteProcessCleanup {
					close(cleanupEntered)
					<-releaseCleanup
					if failed {
						w.WriteHeader(500)
						return
					}
					_, _ = w.Write(connectFrame(nil, []byte(`{"event":{"end":{"exitCode":0,"exited":true}}}`)))
					return
				}
				if request.Process.Envs["TOKEN"] != "private" {
					t.Error("command environment not passed")
				}
				w.WriteHeader(200)
				w.(http.Flusher).Flush()
				close(entered)
				<-r.Context().Done()
			}))
			defer srv.Close()
			d := &e2bDriver{http: srv.Client(), envdBase: srv.URL, boxes: map[string]*e2bBox{"s": {sandboxID: "box"}}}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			result := make(chan Result, 1)
			go func() { result <- d.RunBash(ctx, "s", "echo private", []string{"TOKEN=private"}, 30) }()
			<-entered
			cancel()
			<-cleanupEntered
			select {
			case <-result:
				t.Fatal("returned before remote cleanup acknowledged")
			default:
			}
			close(releaseCleanup)
			select {
			case res := <-result:
				if res.Err == nil || !res.Killed {
					t.Fatalf("cancelled command looked successful: %+v", res)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("cleanup did not finish")
			}
			if failed {
				if _, err := d.ensure(context.Background(), "s"); err == nil {
					t.Fatal("unconfirmed command workspace still usable")
				}
				if err := d.WriteFile(context.Background(), "s", "x", []byte("x")); err == nil {
					t.Fatal("quarantined write accepted")
				}
				if _, err := d.ReadFile(context.Background(), "s", "x"); err == nil {
					t.Fatal("quarantined read accepted")
				}
				d.RemoveFile("s", "x")
				if requests.Load() != 2 {
					t.Fatal("quarantined operation reached service")
				}
			} else if _, err := d.ensure(context.Background(), "s"); err != nil {
				t.Fatalf("confirmed cleanup quarantined workspace: %v", err)
			}
		})
	}
}

func TestE2BTruncatedStreamCannotReportSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(connectFrame(nil, []byte(`{"event":{"start":{"pid":42}}}`)))
	}))
	defer srv.Close()
	d := &e2bDriver{http: srv.Client(), envdBase: srv.URL}
	if _, err := d.startProcess(context.Background(), &e2bBox{}, []string{"echo", "x"}, nil); err == nil {
		t.Fatal("EOF before exit was treated as successful exit")
	}
}

func TestDockerReaperPreservesQuarantineAndFailedRemoval(t *testing.T) {
	var removes atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		removes.Add(1)
		w.WriteHeader(500)
	}))
	defer srv.Close()
	old := time.Now().Add(-time.Hour)
	d := &dockerDriver{client: newDockerClient(srv.URL), idleTimeout: time.Minute, maxLifetime: time.Minute,
		ctrs:    map[string]*ctrState{"blocked": {id: "blocked", created: old, lastUsed: old}, "ordinary": {id: "ordinary", created: old, lastUsed: old}},
		blocked: map[string]bool{"blocked": true}}
	d.reapOnce()
	if removes.Load() != 1 || d.ctrs["blocked"] == nil || d.ctrs["ordinary"] == nil {
		t.Fatal("reaper removed quarantine or forgot a failed removal")
	}
	d.Close("blocked") // Explicit removal fails; preserve the block and handle.
	d.Close("blocked")
	if !d.blocked["blocked"] || d.ctrs["blocked"] == nil {
		t.Fatal("failed close unblocked an existing container")
	}
}

func TestDockerEnsureWaitsForRetiringContainer(t *testing.T) {
	removing, release := make(chan struct{}), make(chan struct{})
	var inspected atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("DELETE /containers/old", func(w http.ResponseWriter, r *http.Request) { close(removing); <-release; w.WriteHeader(204) })
	mux.HandleFunc("GET /containers/ai-agent-sbx-race/json", func(w http.ResponseWriter, r *http.Request) { inspected.Add(1); w.WriteHeader(404) })
	mux.HandleFunc("POST /containers/create", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"Id":"new"}`)) })
	mux.HandleFunc("POST /containers/new/start", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	srv := httptest.NewServer(mux)
	defer srv.Close()
	old := time.Now().Add(-time.Hour)
	d := &dockerDriver{client: newDockerClient(srv.URL), idleTimeout: time.Minute, maxLifetime: time.Minute, ctrs: map[string]*ctrState{"race": {id: "old", created: old, lastUsed: old}}}
	reaped := make(chan struct{})
	go func() { d.reapOnce(); close(reaped) }()
	<-removing
	releaseLease := AcquireLease("race")
	defer releaseLease()
	ensured := make(chan string, 1)
	go func() {
		id, err := d.ensure(context.Background(), "race")
		if err != nil {
			ensured <- err.Error()
			return
		}
		ensured <- id
	}()
	select {
	case <-ensured:
		t.Fatal("ensure adopted container while it was being removed")
	case <-time.After(30 * time.Millisecond):
	}
	if inspected.Load() != 0 {
		t.Fatal("ensure inspected a retiring container")
	}
	close(release)
	<-reaped
	if id := <-ensured; id != "new" {
		t.Fatalf("wrong post-retirement container: %s", id)
	}
}

// Opt-in real Engine verification. The test never sweeps existing containers;
// it owns and removes only its randomly named sandbox.
func TestRemoteProcessLiveDockerCancellation(t *testing.T) {
	if os.Getenv("SANDBOX_LIVE_DOCKER") != "1" {
		t.Skip("set SANDBOX_LIVE_DOCKER=1 to verify real process cancellation")
	}
	d := &dockerDriver{cfg: Config{Image: defaultDockerImage}, client: newDockerClient(""), ctrs: map[string]*ctrState{}}
	sid := "canceltest-" + uuid.NewString()
	defer d.Close(sid)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	id, err := d.ensure(ctx, sid)
	if err != nil {
		t.Fatal(err)
	}

	// Cancellation before the supervisor has recorded a process ID leaves a
	// tombstone. Starting that same prepared command later must do nothing.
	token, wrapped := supervisedRemoteCommand("python3", 10, []string{"bash", "-c", "echo bad > never-started.txt"})
	if code, err := d.execQuiet(ctx, id, cleanupRemoteCommand("python3", token)); err != nil || code != 0 {
		t.Fatalf("pre-start cancellation: code=%d err=%v", code, err)
	}
	if code, err := d.execQuiet(ctx, id, wrapped); err != nil || code != 125 {
		t.Fatalf("delayed start ignored tombstone: code=%d err=%v", code, err)
	}
	if _, err := d.ReadFile(ctx, sid, "never-started.txt"); err != ErrFileNotFound {
		t.Fatalf("cancelled command started late: %v", err)
	}

	// Force the supervisor to die in the Popen/pgid-registration window. The
	// gated child must exit without executing any user command.
	crashToken, crashCommand := supervisedRemoteCommand("python3", 10, []string{"bash", "-c", "echo bad > unregistered.txt"})
	crashCommand[2] = strings.Replace(crashCommand[2], "with open(pid_path, \"w\") as target:", "os._exit(99)\n    with open(pid_path, \"w\") as target:", 1)
	if code, err := d.execQuiet(ctx, id, crashCommand); err != nil || code != 99 {
		t.Fatalf("supervisor crash setup: code=%d err=%v", code, err)
	}
	if code, err := d.execQuiet(ctx, id, cleanupRemoteCommand("python3", crashToken)); err != nil || code != 0 {
		t.Fatalf("supervisor crash cleanup: code=%d err=%v", code, err)
	}
	if _, err := d.ReadFile(ctx, sid, "unregistered.txt"); err != ErrFileNotFound {
		t.Fatalf("unregistered child ran after supervisor death: %v", err)
	}

	// A sibling process group in the same sandbox must survive cancellation.
	sibling := make(chan Result, 1)
	go func() { sibling <- d.RunBash(ctx, sid, "sleep 0.8; printf unaffected > sibling.txt", nil, 5) }()
	commandCtx, commandCancel := context.WithTimeout(ctx, 150*time.Millisecond)
	res := d.RunBash(commandCtx, sid, "sleep 0.6; printf late > forbidden-late.txt", nil, 10)
	commandCancel()
	if res.Err == nil || !res.Killed {
		t.Fatalf("deadline did not cancel: %+v", res)
	}
	if other := <-sibling; other.Err != nil || other.ExitCode != 0 {
		t.Fatalf("sibling command killed: %+v", other)
	}
	if content, err := d.ReadFile(ctx, sid, "sibling.txt"); err != nil || string(content) != "unaffected" {
		t.Fatalf("sibling result: %q %v", content, err)
	}
	if _, err := d.ReadFile(ctx, sid, "forbidden-late.txt"); err != ErrFileNotFound {
		t.Fatalf("late write after cancellation: %v", err)
	}

	// A background descendant cannot outlive normal tool completion either.
	res = d.RunBash(ctx, sid, "(sleep 0.4; echo late > detached.txt) >/dev/null 2>&1 & echo finished", nil, 5)
	if res.Err != nil || !strings.Contains(res.Stdout, "finished") {
		t.Fatalf("background cleanup: %+v", res)
	}
	res = d.RunBash(ctx, sid, "sleep 0.5; test ! -f detached.txt", nil, 5)
	if res.Err != nil || res.ExitCode != 0 {
		t.Fatalf("background descendant outlived command: %+v", res)
	}
}
