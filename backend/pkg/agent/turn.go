// turn.go 阶段三核心：异步解耦后台 Turn 引擎与会话级注册表。
//
// 传统 SSE 模式下执行体绑定在 HTTP 请求生命周期上，客户端刷新或断连会
// 连带杀掉模型推理、工具执行与落库。TurnRegistry 将单轮对话托管至独立
// Goroutine：HTTP/SSE 仅作为订阅者"旁听"，断连只结束订阅，Turn 照常
// 跑完并落库；客户端重连时携带 from_seq，从环形事件缓冲区补播丢失帧。
//
// 单实例内存态实现（多实例部署需引入共享通道，见 README 路线图）。
package agent

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

const (
	// MaxBufferedEvents 单轮 Turn 的事件滑动窗口上限，超出丢弃最旧事件
	MaxBufferedEvents = 3000
	// RetainAfterDone Turn 结束后事件缓冲保留时长，给"刷新后重连"留窗口
	RetainAfterDone = 5 * time.Minute
	// HeartbeatInterval 订阅者空闲心跳间隔，防止反向代理掐断空闲连接
	HeartbeatInterval = 15 * time.Second
)

// ErrTurnRunning 会话级互斥：同一会话已有正在进行的 Turn
var ErrTurnRunning = errors.New("该会话已有正在进行的对话轮次")

// Turn 单轮对话的后台执行载体：事件环形缓冲 + 生命周期状态 + Abort 句柄
type Turn struct {
	sessionID string

	mu         sync.Mutex
	events     []Event   // 滑动窗口；events[i] 的真实 seq = dropped + i
	dropped    int       // 因超窗被丢弃的事件数
	finishedAt time.Time // 零值表示仍在执行
	cancel     context.CancelFunc
	wake       chan struct{} // close-and-replace 广播：append/finish 后唤醒全部订阅者
}

// SessionID 所属会话
func (t *Turn) SessionID() string { return t.sessionID }

// NextSeq 下一个事件的序号（即已产出的总事件数）
func (t *Turn) NextSeq() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.dropped + len(t.events)
}

// Running Turn 是否仍在执行
func (t *Turn) Running() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.finishedAt.IsZero()
}

func (t *Turn) append(ev Event) {
	t.mu.Lock()
	t.events = append(t.events, ev)
	if len(t.events) > MaxBufferedEvents {
		t.events = t.events[1:]
		t.dropped++
	}
	t.wakeLocked()
	t.mu.Unlock()
}

func (t *Turn) finish() {
	t.mu.Lock()
	t.finishedAt = time.Now()
	t.wakeLocked()
	t.mu.Unlock()
}

// wakeLocked 唤醒全部订阅者；持有 t.mu 调用。
// "先 close 再换新"：已在等待的订阅者被唤醒，新订阅者拿到新 channel 不会错过后续信号
func (t *Turn) wakeLocked() {
	close(t.wake)
	t.wake = make(chan struct{})
}

// lastIsDone 最后一个事件是否为 done（判断执行体是否已自行收尾）
func (t *Turn) lastIsDone() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.events) > 0 && t.events[len(t.events)-1].Type == "done"
}

// ensureDone 兜底补发 done 收尾事件，防止订阅端停留在 streaming 态
func (t *Turn) ensureDone() {
	if !t.lastIsDone() {
		t.append(Event{Type: "done"})
	}
}

// pump 后台工作协程：驱动执行体，把产出事件灌入缓冲，并保证任何路径下
// 订阅者都能看到终态事件（error/done）与 finished 标记
func (t *Turn) pump(ctx context.Context, cancel context.CancelFunc, run func(ctx context.Context, emit func(Event)) error) {
	defer func() {
		if v := recover(); v != nil {
			t.append(Event{Type: "error", Error: fmt.Sprintf("后台任务异常：%v", v)})
		}
		t.ensureDone()
		t.finish()
		cancel() // 释放 ctx 资源
	}()

	err := run(ctx, t.append)
	if err != nil {
		// 执行体未走协议收尾就返回了错误——补 error + done，让订阅端明确终止
		if !t.lastIsDone() {
			t.append(Event{Type: "error", Error: err.Error()})
		}
		return
	}
	if ctx.Err() != nil && !t.lastIsDone() {
		t.append(Event{Type: "error", Error: "本轮对话已被中止"})
	}
}

// TurnRegistry 进程内会话级 Turn 注册表：会话互斥 + 生命周期管理 + 事件订阅
type TurnRegistry struct {
	mu    sync.Mutex
	turns map[string]*Turn // sessionID -> Turn（单实例内存态）
}

// NewTurnRegistry 创建独立注册表（生产用包级单例 DefaultRegistry，测试可独立实例化）
func NewTurnRegistry() *TurnRegistry {
	return &TurnRegistry{turns: map[string]*Turn{}}
}

// DefaultRegistry 全局默认注册表
var DefaultRegistry = NewTurnRegistry()

// Get 获取会话当前 Turn（可能已结束但仍在保留窗口内，可重连补看）
func (r *TurnRegistry) Get(sessionID string) *Turn {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.turns[sessionID]
}

// Running 会话是否存在正在执行的 Turn
func (r *TurnRegistry) Running(sessionID string) bool {
	t := r.Get(sessionID)
	return t != nil && t.Running()
}

// Abort 中止会话当前 Turn（幂等：无在跑 Turn 时静默返回）
func (r *TurnRegistry) Abort(sessionID string) {
	t := r.Get(sessionID)
	if t == nil {
		return
	}
	t.mu.Lock()
	cancel := t.cancel
	running := t.finishedAt.IsZero()
	t.mu.Unlock()
	if running && cancel != nil {
		cancel()
	}
}

// Start 启动会话的后台 Turn：
//  1. 顺带 GC 超过保留期的已完成 Turn；
//  2. 会话互斥——已有在跑 Turn 时返回 ErrTurnRunning（并发两轮同时读写
//     同一份会话历史，错乱比拒绝难查，故直接拒绝而非排队）；
//  3. 派生后台 Goroutine 驱动 run，注册表持有强引用防止 Turn 语义丢失。
//
// run 收到的 ctx 独立于任何 HTTP 请求；调用方需自行完成 DB 等依赖注入。
func (r *TurnRegistry) Start(sessionID string, run func(ctx context.Context, emit func(Event)) error) (*Turn, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.gcLocked(time.Now())
	if t, ok := r.turns[sessionID]; ok && t.Running() {
		return nil, ErrTurnRunning
	}

	ctx, cancel := context.WithCancel(context.Background())
	t := &Turn{sessionID: sessionID, cancel: cancel, wake: make(chan struct{})}
	r.turns[sessionID] = t
	go t.pump(ctx, cancel, run)
	return t, nil
}

// gcLocked 清理结束超过保留期的 Turn；持有 r.mu 调用
func (r *TurnRegistry) gcLocked(now time.Time) {
	for id, t := range r.turns {
		t.mu.Lock()
		expired := !t.finishedAt.IsZero() && now.Sub(t.finishedAt) > RetainAfterDone
		t.mu.Unlock()
		if expired {
			delete(r.turns, id)
		}
	}
}

// Follow 订阅 Turn 事件流：先从 fromSeq 补播缓冲区内的事件，再实时跟随，
// 空闲超过 HeartbeatInterval 时回调 onPing 作为心跳；Turn 结束且追平后返回。
//
// fromSeq 早于窗口起点时钳位到最早可用事件（窗口外的旧事件已丢弃）。
// ctx 取消（客户端断连）只是结束本次订阅，不影响后台 Turn 执行。
func (r *TurnRegistry) Follow(ctx context.Context, t *Turn, fromSeq int, onEvent func(seq int, ev Event), onPing func()) {
	i := fromSeq
	for {
		t.mu.Lock()
		if i < t.dropped {
			i = t.dropped
		}
		if i < t.dropped+len(t.events) {
			ev := t.events[i-t.dropped]
			t.mu.Unlock()
			onEvent(i, ev)
			i++
			continue
		}
		finished := !t.finishedAt.IsZero()
		wake := t.wake
		t.mu.Unlock()

		if finished {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-wake:
		case <-time.After(HeartbeatInterval):
			if onPing != nil {
				onPing()
			}
		}
	}
}
