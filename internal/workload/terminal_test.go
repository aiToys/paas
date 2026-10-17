package workload

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"
)

// fakeExecSession 最小 ExecSession 桩。
type fakeExecSession struct {
	stdin  *io.PipeWriter
	stdout *io.PipeReader
	closed bool
}

func newFakeExec() *fakeExecSession {
	_, stdinW := io.Pipe() // 读端丢弃（测试不往 stdin 写）
	stdoutR, _ := io.Pipe()
	return &fakeExecSession{stdin: stdinW, stdout: stdoutR}
}
func (f *fakeExecSession) Stdin() io.WriteCloser    { return f.stdin }
func (f *fakeExecSession) Stdout() io.Reader        { return f.stdout }
func (f *fakeExecSession) Resize(cols, rows uint16) {}
func (f *fakeExecSession) Wait() error              { return nil }
func (f *fakeExecSession) Close()                   { f.closed = true; _ = f.stdin.Close(); _ = f.stdout.Close() }

// fakeStreamer 记录 Stream 调用并返回受控会话。
type fakeStreamer struct {
	calls int
	err   error
}

func (f *fakeStreamer) Stream(ctx context.Context, workloadID, podName, podUID, container, shell string) (ExecSession, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return newFakeExec(), nil
}

func TestTerminalDisabledWithoutStreamer(t *testing.T) {
	m := NewTerminalManager(nil)
	if _, _, err := m.Create("acme", "u1", "wl-x", "pod-a", "uid-a", "", "/bin/sh", "test"); !errors.Is(err, ErrTerminalDisabled) {
		t.Fatalf("nil Streamer 应返 ErrTerminalDisabled，got %v", err)
	}
	if m.Enabled() {
		t.Fatal("Enabled 应为 false")
	}
}

func TestTerminalShellWhitelist(t *testing.T) {
	m := NewTerminalManager(&fakeStreamer{})
	if _, _, err := m.Create("acme", "u1", "wl-x", "pod-a", "uid-a", "", "/bin/zsh", "test"); err == nil {
		t.Fatal("非白名单 shell 应拒绝")
	}
	if _, _, err := m.Create("acme", "u1", "wl-x", "pod-a", "uid-a", "", "/bin/bash", "test"); err != nil {
		t.Fatalf("/bin/bash 应允许，got %v", err)
	}
}

func TestTerminalConcurrencyLimit(t *testing.T) {
	m := NewTerminalManager(&fakeStreamer{})
	for i := 0; i < TerminalMaxPerUser; i++ {
		if _, _, err := m.Create("acme", "u1", "wl-x", "pod-a", "uid-a", "", "/bin/sh", "test"); err != nil {
			t.Fatalf("第 %d 个会话应允许，got %v", i+1, err)
		}
	}
	if _, _, err := m.Create("acme", "u1", "wl-x", "pod-a", "uid-a", "", "/bin/sh", "test"); !errors.Is(err, ErrTerminalBusy) {
		t.Fatalf("超并发上限应返 ErrTerminalBusy，got %v", err)
	}
	// 其他用户不受影响。
	if _, _, err := m.Create("acme", "u2", "wl-x", "pod-a", "uid-a", "", "/bin/sh", "test"); err != nil {
		t.Fatalf("不同用户应独立计数，got %v", err)
	}
}

func TestTerminalConsumeSingleUse(t *testing.T) {
	streamer := &fakeStreamer{}
	m := NewTerminalManager(streamer)
	id, _, err := m.Create("acme", "u1", "wl-x", "pod-a", "uid-a", "", "/bin/sh", "test")
	if err != nil {
		t.Fatal(err)
	}
	sess, err := m.Consume(context.Background(), id)
	if err != nil || sess.Exec == nil {
		t.Fatalf("首次消费应成功建流，got err=%v", err)
	}
	m.Close(id) // Close 后会话彻底移除（含 exec 流关闭）
	if _, ok := m.Get(id); ok {
		t.Fatal("Close 后 Get 应 miss")
	}
}

func TestTerminalHandshakeExpiry(t *testing.T) {
	m := NewTerminalManager(&fakeStreamer{})
	id, exp, err := m.Create("acme", "u1", "wl-x", "pod-a", "uid-a", "", "/bin/sh", "test")
	if err != nil {
		t.Fatal(err)
	}
	if !exp.After(time.Now()) {
		t.Fatal("握手窗口应在未来")
	}
	// 手动把过期时间改到过去（模拟 60s 窗口错过）。
	m.mu.Lock()
	if s, ok := m.sessions[id]; ok {
		s.ExpiresAt = time.Now().Add(-time.Second)
	}
	m.mu.Unlock()
	if _, err := m.Consume(context.Background(), id); !errors.Is(err, ErrTerminalStale) {
		t.Fatalf("过期会话应返 ErrTerminalStale，got %v", err)
	}
}

func TestTerminalStreamFailureCleansSession(t *testing.T) {
	streamer := &fakeStreamer{err: errors.New("pod not running")}
	m := NewTerminalManager(streamer)
	id, _, err := m.Create("acme", "u1", "wl-x", "pod-a", "uid-a", "", "/bin/sh", "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Consume(context.Background(), id); err == nil {
		t.Fatal("建流失败应返回错误")
	}
	if _, ok := m.Get(id); ok {
		t.Fatal("建流失败后会话应被清理")
	}
}
