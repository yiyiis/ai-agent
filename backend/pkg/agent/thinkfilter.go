package agent

import "strings"

const (
	thinkOpen  = "<think>"
	thinkClose = "</think>"
)

// thinkFilter 分离模型输出开头的 <think>...</think> 推理块（如 MiniMax-M3）：
// 正文作为 content 放行，推理块作为 reasoning 返回；标签拆分到多个 chunk 也能正确识别。
type thinkFilter struct {
	phase   int    // 0=判定开头是否为 think 块, 1=think 块内, 2=正文透传
	buf     string // phase 0: 待判定缓冲; phase 1: 已累积的 think 文本
	emitted int    // phase 1 中已作为 reasoning 发出的字节数
}

// Filter 输入一个增量 chunk，返回 (应展示的正文, 应展示的推理)
func (f *thinkFilter) Filter(chunk string) (content, reasoning string) {
	switch f.phase {
	case 2:
		return chunk, ""
	case 0:
		f.buf += chunk
		trimmed := strings.TrimLeft(f.buf, " \t\r\n")
		if trimmed == "" {
			f.buf = trimmed
			return "", ""
		}
		if strings.HasPrefix(trimmed, thinkOpen) { // 完整标签，进入 think 块
			f.phase, f.buf, f.emitted = 1, trimmed[len(thinkOpen):], 0
			return f.Filter("")
		}
		if strings.HasPrefix(thinkOpen, trimmed) { // 仍是不完整前缀，继续缓冲
			f.buf = trimmed
			return "", ""
		}
		f.phase = 2 // 开头不是 think 块，后续全部透传
		f.buf = ""
		return trimmed, ""
	case 1:
		f.buf += chunk
		if idx := strings.Index(f.buf, thinkClose); idx >= 0 {
			out := ""
			if idx > f.emitted {
				out = f.buf[f.emitted:idx]
			}
			rest := strings.TrimLeft(f.buf[idx+len(thinkClose):], " \t\r\n")
			f.phase, f.buf, f.emitted = 2, "", 0
			return rest, out
		}
		out := ""
		if len(f.buf) > f.emitted {
			out = f.buf[f.emitted:]
			f.emitted = len(f.buf)
		}
		return "", out
	}
	return chunk, ""
}
