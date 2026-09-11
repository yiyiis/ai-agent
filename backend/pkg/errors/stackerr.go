package errors

import (
	"fmt"
	"io"
	"strings"

	"github.com/pkg/errors"

	stderr "errors"
)

// 本包是项目唯一的错误工具箱，业务代码不再直接 import 标准库 errors：
//
//	NewMsg            纯消息错误，做业务哨兵（不带堆栈，身份按指针比对）
//	New / Wrap / Wrapf 带堆栈的错误，用于基础设施与初始化路径
//	Join              组合多个错误，%+v 时连子错误的堆栈一起打印
//	Is / As           标准库转手，遍历整棵错误树
//	Stack             从任意形状的错误里挖出堆栈文本

// ---------- MsgErr：纯消息 ----------

// MsgErr 纯消息错误：不带堆栈、不用排查，只要一句话。
// 适合做业务哨兵（domain/<模块>/errors.go），errors.Is 按指针比对身份。
type MsgErr struct {
	msg string
}

func NewMsg(msg string) *MsgErr {
	return &MsgErr{msg: msg}
}

func (e *MsgErr) Error() string {
	return e.msg
}

// ---------- StackErr：带堆栈 ----------

// StackErr 带堆栈的错误，适合基础设施与初始化路径：日志用 %+v 打印即可看到调用栈。
type StackErr struct {
	err error
}

func (e *StackErr) Error() string {
	return e.err.Error()
}

// Unwrap 让 errors.Is / errors.As 能穿过 StackErr 继续向内比对；
// 没有它，StackErr 一旦出现在错误链中间，链就在这里断掉。
func (e *StackErr) Unwrap() error {
	return e.err
}

// Format 支持 %+v 打印堆栈；内层错误不实现 fmt.Formatter 时回退到普通格式化，避免 panic
func (e *StackErr) Format(s fmt.State, verb rune) {
	if f, ok := e.err.(fmt.Formatter); ok {
		f.Format(s, verb)
		return
	}
	fmt.Fprintf(s, fmt.FormatString(s, verb), e.err)
}

// New 创建一个 error，携带创建点的堆栈信息
func New(msg string) *StackErr {
	return &StackErr{
		errors.New(msg),
	}
}

// Wrap 给已有错误附加说明和当前位置的堆栈，并保留错误链（errors.Is/As 仍能命中内层错误）。
// err 为 nil 时返回 nil。
func Wrap(err error, msg string) error {
	if err == nil {
		return nil
	}
	return &StackErr{errors.Wrap(err, msg)}
}

// Wrapf 同 Wrap，说明文字支持格式化
func Wrapf(err error, format string, args ...any) error {
	if err == nil {
		return nil
	}
	return &StackErr{errors.Wrapf(err, format, args...)}
}

// Stack 返回 err 中第一个 StackErr 的「消息 + 堆栈」文本，不论它被 Join 还是 fmt.Errorf 包了几层；
// 没有 StackErr 时退化为 err.Error()。日志里直接用：slog.Error("db failed", "stack", errors.Stack(err))
func Stack(err error) string {
	if err == nil {
		return ""
	}
	var se *StackErr
	if stderr.As(err, &se) {
		return fmt.Sprintf("%+v", se)
	}
	return err.Error()
}

// ---------- Join：组合多个错误 ----------

// joinErr Join 的返回类型：与标准库语义一致（过滤 nil、Error 换行拼接、Unwrap 返回全部子错误），
// 额外实现 Format——%+v 时逐个子错误转交打印，子错误里的堆栈不会被埋掉。
type joinErr struct {
	errs []error
}

// Join 将多个错误合成一个错误，忽略 nil；全为 nil 时返回 nil
func Join(errs ...error) error {
	kept := make([]error, 0, len(errs))
	for _, err := range errs {
		if err != nil {
			kept = append(kept, err)
		}
	}
	if len(kept) == 0 {
		return nil
	}
	return &joinErr{errs: kept}
}

func (e *joinErr) Error() string {
	if len(e.errs) == 1 {
		return e.errs[0].Error()
	}
	var b strings.Builder
	for i, err := range e.errs {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(err.Error())
	}
	return b.String()
}

// Unwrap 暴露全部子错误，errors.Is / errors.As 据此遍历整棵树
func (e *joinErr) Unwrap() []error {
	return e.errs
}

// Format 把 fmt 的打印请求原样转交给每个子错误：%+v 时有堆栈的子错误会打出堆栈
func (e *joinErr) Format(s fmt.State, verb rune) {
	if verb != 'v' {
		fmt.Fprintf(s, fmt.FormatString(s, verb), e.Error())
		return
	}
	directive := fmt.FormatString(s, verb) // 保留 + 等旗标，例如 %+v
	for i, err := range e.errs {
		if i > 0 {
			io.WriteString(s, "\n")
		}
		fmt.Fprintf(s, directive, err)
	}
}

// ---------- Is / As：标准库转手 ----------

// Is 判断 err 的错误树中是否有 target
func Is(err error, target error) bool {
	return stderr.Is(err, target)
}

// As 从错误树中提取指定类型的错误
func As(err error, target any) bool {
	return stderr.As(err, target)
}
