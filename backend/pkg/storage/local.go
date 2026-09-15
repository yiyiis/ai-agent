package storage

import (
	"context"
	"io"
	"os"
	"path/filepath"

	"backend/pkg/errors"
)

// localStorage 本地磁盘驱动：对象落 uploads/ 目录，URL 走 /api/uploads/<name> 下载路由。
// 未配置 COS 时的默认驱动，链路与旧的本地直存完全兼容。
type localStorage struct{}

func (localStorage) Name() string { return "local" }

func (s localStorage) Put(_ context.Context, name string, r io.Reader, size int64, contentType string) (Object, error) {
	if err := CheckSize(size); err != nil {
		return Object{}, err
	}
	if err := os.MkdirAll(LocalRoot, 0o755); err != nil {
		return Object{}, errors.Join(err, errors.New("mkdir "+LocalRoot), errors.NewMsg("创建存储目录失败"))
	}

	stored := SanitizeName(name)
	p := filepath.Join(LocalRoot, stored)
	dst, err := os.Create(p)
	if err != nil {
		return Object{}, errors.Join(err, errors.New("create "+p), errors.NewMsg("保存文件失败"))
	}
	defer dst.Close()

	// 限额拷贝：超限立刻断流，不会把整个文件先读进内存
	written, err := io.Copy(dst, io.LimitReader(r, MaxBytes+1))
	if err != nil {
		_ = os.Remove(p)
		return Object{}, errors.Join(err, errors.New("copy upload"), errors.NewMsg("保存文件失败"))
	}
	if written > MaxBytes {
		_ = os.Remove(p)
		return Object{}, CheckSize(written)
	}

	return Object{
		URL:         "/api/uploads/" + stored,
		Filename:    name,
		Size:        written,
		ContentType: contentType,
	}, nil
}
