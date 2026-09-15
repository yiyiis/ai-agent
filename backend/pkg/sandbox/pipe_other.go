//go:build !windows

package sandbox

import (
	"context"
	"net"

	"backend/pkg/errors"
)

// dialPipe 非 Windows 平台不支持 npipe 协议（Docker URL 请改用 unix:// 或 tcp://）
func dialPipe(_ context.Context, _ string) (net.Conn, error) {
	return nil, errors.New("npipe transport 仅支持 Windows")
}
