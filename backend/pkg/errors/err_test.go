package errors

import (
	stderr "errors"
	"fmt"
	"strings"
	"testing"
)

func TestLogStack(t *testing.T) {

	err := New("携带错误堆栈的信息")

	fmt.Printf("%+v", err)
}

func TestJoinAndIs(t *testing.T) {
	var err1 = New("err1")
	var err2 = New("err2")

	err3 := Join(err1, err2)

	if !Is(err3, err1) {
		t.Fatal("无法判断错误链中有目标error")
	}
}

type DataErr struct {
	Code int
	Msg  string
}

func (e *DataErr) Error() string {
	return e.Msg
}

func TestAs(t *testing.T) {
	var err error = &DataErr{
		Code: 1,
		Msg:  "错误信息",
	}

	err2 := New("err2")

	err = Join(err, err2)

	var data *DataErr

	if !As(err, &data) {
		t.Fatal("无法从错误链中获取error原始类型数据")
	}

	t.Log(data.Code)
	t.Log(data.Msg)
}

// 回归：StackErr 位于错误链中间时，errors.Is 必须能穿过它命中内层错误
func TestStackErr_UnwrapKeepsChain(t *testing.T) {
	inner := stderr.New("record not found")
	mid := &StackErr{fmt.Errorf("query user: %w", inner)}

	if !stderr.Is(mid, inner) {
		t.Fatal("errors.Is 应能穿过 StackErr 命中内层错误")
	}
	// 内层错误不实现 fmt.Formatter 时，格式化不应 panic
	if got := fmt.Sprintf("%v", mid); got != "query user: record not found" {
		t.Fatalf("Format 回退输出不符: %q", got)
	}
}

func TestWrap(t *testing.T) {
	inner := stderr.New("record not found")
	wrapped := Wrap(inner, "query user")

	if !Is(wrapped, inner) {
		t.Fatal("Wrap 之后 errors.Is 应仍能命中内层错误")
	}
	if wrapped.Error() != "query user: record not found" {
		t.Fatalf("Error() = %q", wrapped.Error())
	}
	if stack := fmt.Sprintf("%+v", wrapped); !strings.Contains(stack, "err_test.go") {
		t.Fatalf("%%+v 应包含调用栈, got:\n%s", stack)
	}
	if Wrap(nil, "noop") != nil {
		t.Fatal("Wrap(nil) 应返回 nil")
	}
}

func TestWrapf_AsThroughStackErr(t *testing.T) {
	wrapped := Wrapf(&DataErr{Code: 7, Msg: "boom"}, "step %d", 2)

	var target *DataErr
	if !As(wrapped, &target) || target.Code != 7 {
		t.Fatalf("errors.As 应能穿过 StackErr 取到原始类型, got %+v", target)
	}
	if wrapped.Error() != "step 2: boom" {
		t.Fatalf("Error() = %q", wrapped.Error())
	}
	if Wrapf(nil, "noop %d", 1) != nil {
		t.Fatal("Wrapf(nil) 应返回 nil")
	}
}

// Stack 不论错误是什么形状都能挖到堆栈
func TestStack(t *testing.T) {
	se := New("boom")
	shapes := map[string]error{
		"StackErr 本身":      se,
		"被 fmt.Errorf 包一层": fmt.Errorf("ctx: %w", se),
		"被标准库 Join":        stderr.Join(stderr.New("a"), se),
		"被本包 Join":         Join(stderr.New("a"), se),
		"被 Wrap":           Wrap(se, "outer"),
	}
	for name, err := range shapes {
		if out := Stack(err); !strings.Contains(out, "err_test.go") {
			t.Errorf("%s：Stack() 应包含堆栈, got:\n%s", name, out)
		}
	}

	if got := Stack(stderr.New("plain")); got != "plain" {
		t.Errorf("没有 StackErr 时应退化为 Error(), got %q", got)
	}
	if Stack(nil) != "" {
		t.Error("Stack(nil) 应返回空串")
	}
}

// MsgErr 做哨兵：身份按指针比对，被 Wrapf 包上堆栈后 Is 仍能命中，Stack 挖到的是外层的堆栈
func TestMsgErr_AsSentinel(t *testing.T) {
	sentinel := NewMsg("用户不存在")
	if sentinel.Error() != "用户不存在" {
		t.Fatalf("Error() = %q", sentinel.Error())
	}
	if Is(NewMsg("用户不存在"), sentinel) {
		t.Fatal("消息相同的两个 MsgErr 不应被视为同一个哨兵")
	}

	wrapped := Wrapf(sentinel, "query user %d", 7)
	if !Is(wrapped, sentinel) {
		t.Fatal("Wrapf 之后 Is 应仍能命中哨兵")
	}
	if wrapped.Error() != "query user 7: 用户不存在" {
		t.Fatalf("Error() = %q", wrapped.Error())
	}
	if !strings.Contains(Stack(wrapped), "err_test.go") {
		t.Fatal("Stack 应挖到 Wrapf 附加的堆栈")
	}
	if Stack(sentinel) != sentinel.Error() {
		t.Fatal("裸哨兵没有堆栈，Stack 应退化为 Error()")
	}
}

// Join：与标准库语义一致，且 %+v 会连子错误的堆栈一起打印
func TestJoin(t *testing.T) {
	if Join(nil, nil) != nil {
		t.Fatal("全为 nil 时应返回 nil")
	}

	sentinel := NewMsg("用户不存在")
	joined := Join(nil, sentinel, New("query failed"))

	if joined.Error() != "用户不存在\nquery failed" {
		t.Fatalf("Error() 应过滤 nil 并以换行拼接, got %q", joined.Error())
	}
	if !Is(joined, sentinel) {
		t.Fatal("Is 应能在 Join 的子错误里找到哨兵")
	}

	out := fmt.Sprintf("%+v", joined)
	if !strings.Contains(out, "用户不存在") || !strings.Contains(out, "err_test.go") {
		t.Fatalf("%%+v 应包含全部子错误消息与子错误的堆栈, got:\n%s", out)
	}
	if got := fmt.Sprintf("%v", joined); got != joined.Error() {
		t.Fatalf("%%v 应等于 Error()（不带堆栈）, got %q", got)
	}
}
