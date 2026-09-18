package dispatcher_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"backend/pkg/dispatcher"
)

func TestClassifyFailure(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		stdout   string
		stderr   string
		exitCode int
		killed   bool
		expected dispatcher.FailureKind
	}{
		{
			name:     "超时强杀",
			err:      context.DeadlineExceeded,
			killed:   true,
			expected: dispatcher.FailureKindTimeout,
		},
		{
			name:     "429 限流",
			stderr:   "HTTP 429 Too Many Requests: Rate limit exceeded",
			exitCode: 1,
			expected: dispatcher.FailureKindRateLimited,
		},
		{
			name:     "网络抖动连接被拒",
			stderr:   "dial tcp 127.0.0.1:80: connection refused",
			exitCode: 1,
			expected: dispatcher.FailureKindNetworkError,
		},
		{
			name:     "沙箱容器启动失败",
			err:      errors.New("docker daemon error: container failed to start"),
			exitCode: -1,
			expected: dispatcher.FailureKindSandbox,
		},
		{
			name:     "脚本自身语法错误",
			stderr:   "SyntaxError: invalid syntax",
			exitCode: 1,
			expected: dispatcher.FailureKindScriptError,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			actual := dispatcher.ClassifyFailure(tc.err, tc.stdout, tc.stderr, tc.exitCode, tc.killed)
			if actual != tc.expected {
				t.Fatalf("expected %s, got %s", tc.expected, actual)
			}
		})
	}
}

func TestIsRetryable(t *testing.T) {
	if !dispatcher.IsRetryable(dispatcher.FailureKindSandbox) {
		t.Fatal("sandbox error should be retryable")
	}
	if !dispatcher.IsRetryable(dispatcher.FailureKindRateLimited) {
		t.Fatal("rate limited error should be retryable")
	}
	if !dispatcher.IsRetryable(dispatcher.FailureKindNetworkError) {
		t.Fatal("network error should be retryable")
	}
	if dispatcher.IsRetryable(dispatcher.FailureKindScriptError) {
		t.Fatal("script error should not be retryable")
	}
	if dispatcher.IsRetryable(dispatcher.FailureKindParamInvalid) {
		t.Fatal("param invalid should not be retryable")
	}
	if dispatcher.IsRetryable(dispatcher.FailureKindTimeout) {
		t.Fatal("timeout should not be retryable")
	}
}

func TestCalculateBackoff(t *testing.T) {
	b1 := dispatcher.CalculateBackoff(1)
	if b1 < 2*time.Second || b1 > 3*time.Second {
		t.Fatalf("backoff attempt 1 should be around 2s-3s (with jitter), got %v", b1)
	}

	b2 := dispatcher.CalculateBackoff(2)
	if b2 < 4*time.Second || b2 > 5*time.Second {
		t.Fatalf("backoff attempt 2 should be around 4s-5s (with jitter), got %v", b2)
	}

	bMax := dispatcher.CalculateBackoff(10)
	if bMax > 60*time.Second {
		t.Fatalf("backoff should not exceed 60s, got %v", bMax)
	}
}
