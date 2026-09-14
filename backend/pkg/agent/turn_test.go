package agent

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// waitTurnDone 轮询等待 Turn 结束（后台 Goroutine 异步收尾）
func waitTurnDone(t *testing.T, r *TurnRegistry, sessionID string) *Turn {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		turn := r.Get(sessionID)
		if turn != nil && !turn.Running() {
			return turn
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("等待 Turn 结束超时")
	return nil
}

func emitAll(events ...Event) func(ctx context.Context, emit func(Event)) error {
	return func(ctx context.Context, emit func(Event)) error {
		for _, ev := range events {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			emit(ev)
		}
		return nil
	}
}

// 正常 Turn：事件进缓冲、序号连续、结束后允许同会话再起新轮
func TestTurnBuffersEventsAndAllowsRestart(t *testing.T) {
	r := NewTurnRegistry()
	turn, err := r.Start("s1", emitAll(
		Event{Type: "user_message_id", ID: "u1"},
		Event{Type: "delta", Content: "你好"},
		Event{Type: "done", ID: "a1"},
	))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if next := turn.NextSeq(); next != 0 {
		t.Fatalf("起始 NextSeq 应为 0，实际 %d", next)
	}
	done := waitTurnDone(t, r, "s1")
	if next := done.NextSeq(); next != 3 {
		t.Fatalf("结束 NextSeq 应为 3，实际 %d", next)
	}

	// 同会话结束后允许再起
	if _, err := r.Start("s1", emitAll(Event{Type: "done"})); err != nil {
		t.Fatalf("结束后再起应成功: %v", err)
	}
	waitTurnDone(t, r, "s1")
}

// 会话互斥：running 期间并发 Start 被拒绝
func TestTurnRegistryRejectsConcurrentStart(t *testing.T) {
	r := NewTurnRegistry()
	release := make(chan struct{})
	started := make(chan struct{})
	turn, err := r.Start("s2", func(ctx context.Context, emit func(Event)) error {
		close(started)
		<-release
		emit(Event{Type: "done"})
		return nil
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	<-started

	if _, err := r.Start("s2", emitAll()); !errors.Is(err, ErrTurnRunning) {
		t.Fatalf("并发 Start 应返回 ErrTurnRunning，实际 %v", err)
	}
	if !r.Running("s2") {
		t.Fatal("Running 应为 true")
	}
	// 其他会话不受影响
	if _, err := r.Start("s3", emitAll(Event{Type: "done"})); err != nil {
		t.Fatalf("其他会话 Start 应成功: %v", err)
	}
	waitTurnDone(t, r, "s3")

	close(release)
	waitTurnDone(t, r, "s2")
	_ = turn
}

// Follow：先补播缓冲再实时跟随；Turn 结束且追平后自然返回
func TestTurnFollowReplaysThenFollowsLive(t *testing.T) {
	r := NewTurnRegistry()
	release := make(chan struct{})
	turn, err := r.Start("s4", func(ctx context.Context, emit func(Event)) error {
		emit(Event{Type: "delta", Content: "a"})
		emit(Event{Type: "delta", Content: "b"})
		<-release
		emit(Event{Type: "delta", Content: "c"})
		emit(Event{Type: "done", ID: "x"})
		return nil
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	// 等前两个事件进缓冲
	deadline := time.Now().Add(time.Second)
	for turn.NextSeq() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}

	var mu sync.Mutex
	var got []string
	followDone := make(chan struct{})
	go func() {
		defer close(followDone)
		r.Follow(context.Background(), turn, 0, func(seq int, ev Event) {
			if ev.Type != "delta" {
				return
			}
			mu.Lock()
			got = append(got, ev.Content)
			mu.Unlock()
		}, nil)
	}()

	// 稍等确保订阅已追平缓冲、进入等待
	time.Sleep(20 * time.Millisecond)
	close(release)
	<-followDone

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Fatalf("Follow 应补播+跟随全部 3 个 delta 事件，实际 %v", got)
	}
}

// Follow：from_seq 超出窗口起点时钳位到最早可用事件
func TestTurnFollowClampsDroppedSeq(t *testing.T) {
	r := NewTurnRegistry()
	events := make([]Event, MaxBufferedEvents+50)
	for i := range events {
		events[i] = Event{Type: "delta", Content: "x"}
	}
	events[len(events)-1] = Event{Type: "done", ID: "last"}
	turn, err := r.Start("s5", emitAll(events...))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitTurnDone(t, r, "s5")

	// NextSeq = dropped(50) + 窗口内保留数(3000) = 总产出事件数
	if turn.NextSeq() != MaxBufferedEvents+50 {
		t.Fatalf("NextSeq 应等于总产出 %d，实际 %d", MaxBufferedEvents+50, turn.NextSeq())
	}
	turn.mu.Lock()
	retained := len(turn.events)
	turn.mu.Unlock()
	if retained != MaxBufferedEvents {
		t.Fatalf("窗口应只保留 %d 条，实际 %d", MaxBufferedEvents, retained)
	}

	var count int
	var lastType string
	r.Follow(context.Background(), turn, 0, func(seq int, ev Event) {
		count++
		lastType = ev.Type
		if seq < 50 {
			t.Errorf("seq 不应早于被丢弃的 50 条，实际 %d", seq)
		}
	}, nil)
	if count != MaxBufferedEvents {
		t.Fatalf("钳位后应收到 %d 条，实际 %d", MaxBufferedEvents, count)
	}
	if lastType != "done" {
		t.Fatalf("最后一条应为 done，实际 %s", lastType)
	}
}

// Abort：取消传播到执行体 ctx，且事件流保证收尾（error/done）
func TestTurnAbortCancelsRunAndAppendsTerminalEvents(t *testing.T) {
	r := NewTurnRegistry()
	ctxSeen := make(chan error, 1)
	_, err := r.Start("s6", func(ctx context.Context, emit func(Event)) error {
		emit(Event{Type: "delta", Content: "partial"})
		<-ctx.Done()
		ctxSeen <- ctx.Err()
		return nil // 模拟 Run 的取消路径：自行收尾返回（Run 会 emit done）
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	r.Abort("s6")
	select {
	case err := <-ctxSeen:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("执行体 ctx 应被取消，实际 %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("等待取消传播超时")
	}
	// 执行体返回后 pump 兜底补 done（本用例未 emit done）
	done := waitTurnDone(t, r, "s6")
	if done.NextSeq() < 2 {
		t.Fatalf("中止后应有收尾事件，实际 %d 条", done.NextSeq())
	}

	// 幂等：对已结束 Turn 再 Abort 不 panic
	r.Abort("s6")
	r.Abort("not-exist")
}

// 执行体返回 error 且未发 done：pump 补 error + done，订阅端可明确终止
func TestTurnRunErrorAppendsErrorAndDone(t *testing.T) {
	r := NewTurnRegistry()
	_, err := r.Start("s7", func(ctx context.Context, emit func(Event)) error {
		emit(Event{Type: "delta", Content: "half"})
		return errors.New("boom")
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	done := waitTurnDone(t, r, "s7")
	if next := done.NextSeq(); next != 3 {
		t.Fatalf("应有 delta+error+done 共 3 条事件，实际 %d", next)
	}
	// Follow 全量重放能看到终态
	var last Event
	r.Follow(context.Background(), done, 0, func(seq int, ev Event) { last = ev }, nil)
	if last.Type != "done" {
		t.Fatalf("最后事件应为 done，实际 %s", last.Type)
	}
}

// 执行体 panic：兜底补事件，进程不崩
func TestTurnPanicIsRecovered(t *testing.T) {
	r := NewTurnRegistry()
	_, err := r.Start("s8", func(ctx context.Context, emit func(Event)) error {
		panic("kaboom")
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	done := waitTurnDone(t, r, "s8")
	if next := done.NextSeq(); next != 2 {
		t.Fatalf("panic 应兜底 error+done 两条事件，实际 %d", next)
	}
}

// Follow：订阅者 ctx 取消只结束订阅，不影响后台 Turn
func TestTurnFollowCancelDoesNotAffectTurn(t *testing.T) {
	r := NewTurnRegistry()
	release := make(chan struct{})
	turn, err := r.Start("s9", func(ctx context.Context, emit func(Event)) error {
		emit(Event{Type: "delta", Content: "a"})
		<-release
		emit(Event{Type: "done"})
		return nil
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	subCtx, cancel := context.WithCancel(context.Background())
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		r.Follow(subCtx, turn, 0, func(seq int, ev Event) {}, nil)
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case <-exited:
	case <-time.After(time.Second):
		t.Fatal("订阅取消后 Follow 应退出")
	}
	if !turn.Running() {
		t.Fatal("订阅退出不应影响后台 Turn")
	}
	close(release)
	waitTurnDone(t, r, "s9")
}

// GC：结束超过保留期的 Turn 在下次 Start 时被回收
func TestTurnRegistryGCAfterRetain(t *testing.T) {
	r := NewTurnRegistry()
	turn, err := r.Start("s10", emitAll(Event{Type: "done"}))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitTurnDone(t, r, "s10")

	// 保留期内可重连
	if r.Get("s10") == nil {
		t.Fatal("保留期内 Turn 应可查询")
	}
	// 直接篡改 finishedAt 模拟超期（避免真实等待 5 分钟）
	turn.mu.Lock()
	turn.finishedAt = time.Now().Add(-RetainAfterDone - time.Second)
	turn.mu.Unlock()

	if _, err := r.Start("s11", emitAll(Event{Type: "done"})); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitTurnDone(t, r, "s11")
	if r.Get("s10") != nil {
		t.Fatal("超期 Turn 应被 GC 回收")
	}
}
