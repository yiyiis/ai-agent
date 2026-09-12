package api

import (
	"fmt"
	"log/slog"

	"backend/pkg/errors"
)

// failMsg 手写 handler 的统一错误出口，与 apiwarp.Controller 同语义：
// 完整错误树（含堆栈）进日志，只把 MsgErr 的纯消息返回给前端；
// 没有 MsgErr 时回退"系统异常"，避免把根因（SQL 细节等）泄漏给用户。
func failMsg(err error) string {
	slog.Error(fmt.Sprintf("\n%+v\n", err))
	var me *errors.MsgErr
	if errors.As(err, &me) {
		return me.Error()
	}
	return "系统异常"
}
