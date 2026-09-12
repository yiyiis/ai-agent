package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"os"
	"path/filepath"
	"strings"

	"backend/pkg/agent"
	"backend/pkg/apiwarp"
	"backend/pkg/errors"
)

// UploadsRoot 上传文件的本地存储目录（相对后端运行目录；阶段四接入 COS 后替换为对象存储），
// 与 agent 落工作区时读取的目录保持同源
var UploadsRoot = agent.UploadsDir

const maxUploadBytes = 20 << 20 // 20MB

// blockedExts 可执行文件作为聊天附件没有正当用途，且公开下载链接等于给了一个分发通道
var blockedExts = map[string]bool{
	"exe": true, "dll": true, "so": true, "dylib": true, "msi": true,
	"scr": true, "com": true, "pif": true, "bat": true, "cmd": true,
	"ps1": true, "vbs": true, "jar": true, "apk": true, "app": true,
}

func storedName(orig string) string {
	ext := strings.ToLower(filepath.Ext(orig))
	base := strings.TrimSuffix(filepath.Base(orig), filepath.Ext(orig))
	// 只保留常见安全字符，避免路径注入与怪异文件名
	var b strings.Builder
	for _, r := range base {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.', r == '(', r == ')':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	if b.Len() == 0 {
		base = "file"
	} else {
		base = b.String()
	}
	buf := make([]byte, 6)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf) + "_" + base + ext
}

type UploadFileReq struct {
	File *multipart.FileHeader `form:"file" binding:"required"`
}

type UploadOut struct {
	URL         string `json:"url"`
	Filename    string `json:"filename"`
	Size        int64  `json:"size"`
	ContentType string `json:"content_type"`
}

// UploadFile 处理 POST /api/uploads：multipart 字段 file，返回附件元数据
func UploadFile(ctx context.Context, req *UploadFileReq) (*UploadOut, error) {
	fh := req.File

	orig := fh.Filename
	if orig == "" {
		orig = "upload.bin"
	}
	ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(orig)), ".")
	if blockedExts[ext] {
		return nil, errors.NewMsg(fmt.Sprintf("不支持上传 .%s 类型的文件", ext))
	}
	if fh.Size > maxUploadBytes {
		return nil, errors.NewMsg(fmt.Sprintf("文件超过 %dMB 上限", maxUploadBytes>>20))
	}

	src, err := fh.Open()
	if err != nil {
		return nil, errors.Join(err, errors.New("open upload"), errors.NewMsg("读取上传文件失败"))
	}
	defer src.Close()

	if err := os.MkdirAll(UploadsRoot, 0o755); err != nil {
		return nil, errors.Join(err, errors.New("mkdir uploads"), errors.NewMsg("创建存储目录失败"))
	}
	name := storedName(orig)
	dst, err := os.Create(filepath.Join(UploadsRoot, name))
	if err != nil {
		return nil, errors.Join(err, errors.New("create upload"), errors.NewMsg("保存文件失败"))
	}
	defer dst.Close()

	// 限额拷贝：超限立刻断流，不会先把整个文件读进内存/磁盘
	written, err := io.Copy(dst, io.LimitReader(src, maxUploadBytes+1))
	if err != nil {
		return nil, errors.Join(err, errors.New("copy upload"), errors.NewMsg("保存文件失败"))
	}
	if written > maxUploadBytes {
		_ = os.Remove(filepath.Join(UploadsRoot, name))
		return nil, errors.NewMsg(fmt.Sprintf("文件超过 %dMB 上限", maxUploadBytes>>20))
	}

	return &UploadOut{
		URL:         "/api/uploads/" + name,
		Filename:    orig,
		Size:        written,
		ContentType: fh.Header.Get("Content-Type"),
	}, nil
}

type DownloadAttachmentReq struct {
	Name string `uri:"name"`
}

// DownloadAttachment 处理 GET /api/uploads/:name（登录态走 cookie 通道，<img>/预览可直接加载）
func DownloadAttachment(ctx context.Context, req *DownloadAttachmentReq) (*apiwarp.FilePathData, error) {
	name := filepath.Base(req.Name) // 防路径穿越，只取文件名部分
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		return nil, errors.NewMsg("非法文件名")
	}
	p := filepath.Join(UploadsRoot, name)
	if _, err := os.Stat(p); err != nil {
		return nil, errors.NewMsg("文件不存在")
	}

	fileType := mime.TypeByExtension(filepath.Ext(name))
	if fileType == "" {
		fileType = "application/octet-stream"
	}
	return &apiwarp.FilePathData{FileType: fileType, Path: p}, nil
}
