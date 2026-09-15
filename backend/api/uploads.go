package api

import (
	"context"
	"mime"
	"mime/multipart"
	"os"
	"path/filepath"
	"strings"

	"backend/pkg/apiwarp"
	"backend/pkg/errors"
	"backend/pkg/storage"
)

const maxUploadBytes = 20 << 20 // 20MB

// blockedExts 可执行文件作为聊天附件没有正当用途，且公开下载链接等于给了一个分发通道
var blockedExts = map[string]bool{
	"exe": true, "dll": true, "so": true, "dylib": true, "msi": true,
	"scr": true, "com": true, "pif": true, "bat": true, "cmd": true,
	"ps1": true, "vbs": true, "jar": true, "apk": true, "app": true,
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

// UploadFile 处理 POST /api/uploads：multipart 字段 file，返回附件元数据。
// 存储经 pkg/storage 统一分发：未配置 COS 时落本地 uploads/，配置后直传对象存储。
func UploadFile(ctx context.Context, req *UploadFileReq) (*UploadOut, error) {
	fh := req.File

	orig := fh.Filename
	if orig == "" {
		orig = "upload.bin"
	}
	ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(orig)), ".")
	if blockedExts[ext] {
		return nil, errors.NewMsg("不支持上传 ." + ext + " 类型的文件")
	}
	if fh.Size > maxUploadBytes {
		return nil, errors.NewMsg("文件超过 20MB 上限")
	}

	src, err := fh.Open()
	if err != nil {
		return nil, errors.Join(err, errors.New("open upload"), errors.NewMsg("读取上传文件失败"))
	}
	defer src.Close()

	obj, err := storage.Default().Put(ctx, orig, src, fh.Size, fh.Header.Get("Content-Type"))
	if err != nil {
		return nil, err
	}
	return &UploadOut{
		URL:         obj.URL,
		Filename:    obj.Filename,
		Size:        obj.Size,
		ContentType: obj.ContentType,
	}, nil
}

type DownloadAttachmentReq struct {
	Name string `uri:"name"`
}

// DownloadAttachment 处理 GET /api/uploads/:name（登录态走 cookie 通道，<img>/预览可直接加载）。
// 仅服务本地存储驱动落盘的附件；COS 直传的附件走公网 URL，不经此路由。
func DownloadAttachment(ctx context.Context, req *DownloadAttachmentReq) (*apiwarp.FilePathData, error) {
	name := filepath.Base(req.Name) // 防路径穿越，只取文件名部分
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		return nil, errors.NewMsg("非法文件名")
	}
	p := filepath.Join(storage.LocalRoot, name)
	if _, err := os.Stat(p); err != nil {
		return nil, errors.NewMsg("文件不存在")
	}

	fileType := mime.TypeByExtension(filepath.Ext(name))
	if fileType == "" {
		fileType = "application/octet-stream"
	}
	return &apiwarp.FilePathData{FileType: fileType, Path: p}, nil
}
