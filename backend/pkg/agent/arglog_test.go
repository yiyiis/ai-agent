package agent

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestArgPreview(t *testing.T) {
	if got := argPreview("{}"); got != "{}" {
		t.Fatalf("短串应原样返回: %q", got)
	}

	// 多字节中文：截断点必须落在 UTF-8 序列边界上
	long := strings.Repeat("参数内容", 500) // 4000 字节
	got := argPreview(long)
	if !utf8.ValidString(got) {
		t.Fatalf("截断后不是合法 UTF-8: %q", got)
	}
	if !strings.Contains(got, "已截断") || len(got) > 512+64 {
		t.Fatalf("截断标注或长度不符: len=%d", len(got))
	}

	got = argPreview(strings.Repeat("a", 1000))
	if !strings.HasPrefix(got, strings.Repeat("a", 512)) || !strings.Contains(got, "已截断") {
		t.Fatalf("ASCII 截断异常: %q…", got[:20])
	}
}
