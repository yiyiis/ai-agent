// transport_stdio.go stdio 传输：本地子进程 + 换行分隔 JSON-RPC。
//
// 子进程 stdout 每行一条 JSON-RPC 消息，读循环 goroutine 按 id 分发到 pending
// map；通知经 notify 回调上抛。进程退出时清空 pending（全部返回连接错误），
// 重连与重新握手由 Manager 统一负责（退避重拨走全新的传输与 client）。
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
)

// ErrClosed 传输已关闭（连接被 Manager 回收）
var ErrClosed = errors.New("MCP 传输已关闭")

// stdioTransport stdio 传输实现
type stdioTransport struct {
	command []string // 完整命令行（含程序名）
	env     []string // os.Environ() + 自定义 KV
	notify  func(*RPCMessage)

	mu      sync.Mutex
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	pending map[int64]chan *RPCMessage
	closed  bool

	writeMu sync.Mutex // 串行化 stdin 写（Request 与 Notify 并发）
}

// newStdioTransport 启动子进程并进入读循环；进程拉起失败即返回错误
func newStdioTransport(command []string, env map[string]string, notify func(*RPCMessage)) (Transport, error) {
	if len(command) == 0 {
		return nil, errors.New("stdio 传输缺少 Command 配置")
	}
	envList := os.Environ()
	for k, v := range env {
		envList = append(envList, k+"="+v)
	}
	t := &stdioTransport{
		command: command,
		env:     envList,
		notify:  notify,
		pending: make(map[int64]chan *RPCMessage),
	}
	if err := t.start(); err != nil {
		return nil, err
	}
	return t, nil
}

func (t *stdioTransport) start() error {
	cmd := exec.Command(t.command[0], t.command[1:]...) // #nosec G204 —— 命令来自管理员 config 种子
	cmd.Env = t.env
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("MCP 子进程 stdin 管道失败: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("MCP 子进程 stdout 管道失败: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("MCP 子进程 stderr 管道失败: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动 MCP 子进程失败（%s）: %w", strings.Join(t.command, " "), err)
	}
	t.cmd = cmd
	t.stdin = stdin
	go t.readLoop(stdout)
	go t.drainStderr(stderr)
	return nil
}

// readLoop 消费 stdout：响应按 id 分发 pending，通知上抛回调
func (t *stdioTransport) readLoop(stdout io.Reader) {
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), maxLineBytes)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var msg RPCMessage
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			slog.Warn("MCP stdio 收到无法解析的行", "err", err.Error(), "len", len(line))
			continue
		}
		switch {
		case msg.ID != nil && msg.Method == "":
			t.dispatch(*msg.ID, &msg)
		case msg.ID != nil:
			// 服务器反向请求（sampling/elicitation 等，v1 未声明支持）：
			// 按 JSON-RPC 约定回 method not found，避免服务器挂起等待
			slog.Debug("MCP 服务器发起反向请求，v1 不支持已回绝", "method", msg.Method)
			t.writeMessage(map[string]any{
				"jsonrpc": "2.0",
				"id":      *msg.ID,
				"error":   map[string]any{"code": errCodeMethodNotFound, "message": "client capability not supported"},
			})
		default:
			if t.notify != nil {
				t.notify(&msg)
			}
		}
	}
	if err := sc.Err(); err != nil {
		slog.Debug("MCP stdio 读循环结束", "err", err.Error())
	}
	t.failAllPending(fmt.Errorf("MCP 子进程已退出: %w", ErrClosed))
}

func (t *stdioTransport) dispatch(id int64, msg *RPCMessage) {
	t.mu.Lock()
	ch, ok := t.pending[id]
	if ok {
		delete(t.pending, id)
	}
	t.mu.Unlock()
	if ok {
		ch <- msg
	}
}

func (t *stdioTransport) failAllPending(err error) {
	t.mu.Lock()
	pending := t.pending
	t.pending = make(map[int64]chan *RPCMessage)
	t.mu.Unlock()
	for id, ch := range pending {
		select {
		case ch <- &RPCMessage{ID: &id, Error: &RPCError{Code: -32000, Message: err.Error()}}:
		default:
		}
	}
}

// drainStderr 服务器日志惯例输出到 stderr，转 Debug 日志
func (t *stdioTransport) drainStderr(stderr io.Reader) {
	sc := bufio.NewScanner(stderr)
	sc.Buffer(make([]byte, 0, 16*1024), 1<<20)
	for sc.Scan() {
		slog.Debug("MCP stdio stderr", "line", sc.Text())
	}
}

func (t *stdioTransport) Request(ctx context.Context, req *RPCRequest) (*RPCResponse, error) {
	ch := make(chan *RPCMessage, 1)
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil, ErrClosed
	}
	t.pending[req.ID] = ch
	t.mu.Unlock()

	body, err := json.Marshal(req)
	if err != nil {
		t.removePending(req.ID)
		return nil, fmt.Errorf("序列化 MCP 请求失败: %w", err)
	}
	t.writeMu.Lock()
	_, werr := t.stdin.Write(append(body, '\n'))
	t.writeMu.Unlock()
	if werr != nil {
		t.removePending(req.ID)
		return nil, fmt.Errorf("写入 MCP 子进程失败: %w", werr)
	}

	select {
	case msg := <-ch:
		return &RPCResponse{ID: req.ID, Result: msg.Result, Error: msg.Error}, nil
	case <-ctx.Done():
		t.removePending(req.ID)
		return nil, ctx.Err()
	}
}

func (t *stdioTransport) removePending(id int64) {
	t.mu.Lock()
	delete(t.pending, id)
	t.mu.Unlock()
}

// writeMessage 原样写一条已序列化好的 JSON-RPC 消息（换行结尾）
func (t *stdioTransport) writeMessage(v any) {
	body, err := json.Marshal(v)
	if err != nil {
		return
	}
	t.writeMu.Lock()
	_, _ = t.stdin.Write(append(body, '\n'))
	t.writeMu.Unlock()
}

func (t *stdioTransport) Notify(_ context.Context, n *RPCNotification) error {
	body, err := json.Marshal(n)
	if err != nil {
		return fmt.Errorf("序列化 MCP 通知失败: %w", err)
	}
	t.mu.Lock()
	closed := t.closed
	t.mu.Unlock()
	if closed {
		return ErrClosed
	}
	t.writeMu.Lock()
	_, werr := t.stdin.Write(append(body, '\n'))
	t.writeMu.Unlock()
	if werr != nil {
		return fmt.Errorf("写入 MCP 子进程失败: %w", werr)
	}
	return nil
}

func (t *stdioTransport) Close() error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.closed = true
	cmd := t.cmd
	stdin := t.stdin
	t.mu.Unlock()
	t.failAllPending(ErrClosed)
	if stdin != nil {
		_ = stdin.Close()
	}
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	return nil
}
