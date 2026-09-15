//go:build windows

package sandbox

import (
	"context"
	"net"

	"github.com/Microsoft/go-winio"
)

// dialPipe Windows 命名管道拨号（Docker Desktop 默认的 Engine API 通道）
func dialPipe(ctx context.Context, path string) (net.Conn, error) {
	return winio.DialPipeContext(ctx, path)
}
