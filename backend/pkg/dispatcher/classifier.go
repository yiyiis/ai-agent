package dispatcher

import (
	"context"
	"crypto/rand"
	"math/big"
	"strings"
	"time"

	"backend/pkg/errors"
)

// ClassifyFailure 结合系统错误、沙箱杀停标记、退出码与 stderr 关键词对故障进行分类
func ClassifyFailure(err error, stdout, stderr string, exitCode int, killed bool) FailureKind {
	lowerErr := ""
	if err != nil {
		lowerErr = strings.ToLower(err.Error())
	}
	lowerStderr := strings.ToLower(stderr)

	// 1. 超时强杀识别
	if killed ||
		errors.Is(err, context.DeadlineExceeded) ||
		strings.Contains(lowerErr, "timeout") ||
		strings.Contains(lowerErr, "deadline exceeded") ||
		strings.Contains(lowerStderr, "timeout") {
		return FailureKindTimeout
	}

	// 2. 限流识别 (429 Rate Limit)
	if strings.Contains(lowerErr, "429") ||
		strings.Contains(lowerErr, "rate limit") ||
		strings.Contains(lowerErr, "too many requests") ||
		strings.Contains(lowerStderr, "429") ||
		strings.Contains(lowerStderr, "rate limit") ||
		strings.Contains(lowerStderr, "too many requests") {
		return FailureKindRateLimited
	}

	// 3. 网络基础设施故障
	if strings.Contains(lowerErr, "connection reset") ||
		strings.Contains(lowerErr, "connection refused") ||
		strings.Contains(lowerErr, "no such host") ||
		strings.Contains(lowerErr, "dial tcp") ||
		strings.Contains(lowerErr, "network is unreachable") ||
		strings.Contains(lowerStderr, "connection reset") ||
		strings.Contains(lowerStderr, "connection refused") ||
		strings.Contains(lowerStderr, "no such host") ||
		strings.Contains(lowerStderr, "dial tcp") ||
		strings.Contains(lowerStderr, "network is unreachable") {
		return FailureKindNetworkError
	}

	// 4. 沙箱环境与驱动故障
	if strings.Contains(lowerErr, "sandbox") ||
		strings.Contains(lowerErr, "docker") ||
		strings.Contains(lowerErr, "container") ||
		strings.Contains(lowerErr, "workspace") ||
		strings.Contains(lowerStderr, "cannot connect to the docker daemon") {
		return FailureKindSandbox
	}

	// 5. 参数错误已在入参层拦截；若进入执行期表明脚本/代码自身崩溃
	return FailureKindScriptError
}

// IsRetryable 依据规范故障决策表判断是否属于可自动恢复的重试故障
func IsRetryable(kind FailureKind) bool {
	switch kind {
	case FailureKindSandbox, FailureKindRateLimited, FailureKindNetworkError:
		return true
	case FailureKindParamInvalid, FailureKindScriptError, FailureKindTimeout:
		return false
	default:
		return false
	}
}

// CalculateBackoff 计算带随机抖动的指数退避延迟时间
// 公式: min(BaseInterval * 2^(attempt-1) + Jitter, MaxInterval)
func CalculateBackoff(attempt int64) time.Duration {
	if attempt <= 0 {
		attempt = 1
	}

	baseInterval := 2 * time.Second
	maxInterval := 60 * time.Second

	// 计算指数因子: 2^(attempt - 1)
	factor := int64(1)
	if attempt > 1 {
		shift := attempt - 1
		if shift > 6 { // 避免溢出，2^6 * 2s = 128s > 60s
			shift = 6
		}
		factor = int64(1) << shift
	}

	delay := baseInterval * time.Duration(factor)

	// 添加 0-1000ms 随机 Jitter，防止并发任务惊群
	jitterMs := int64(0)
	if n, err := rand.Int(rand.Reader, big.NewInt(1000)); err == nil {
		jitterMs = n.Int64()
	}
	delay += time.Duration(jitterMs) * time.Millisecond

	if delay > maxInterval {
		delay = maxInterval
	}
	return delay
}
