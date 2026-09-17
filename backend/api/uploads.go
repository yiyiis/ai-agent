package api

import (
	"context"
	"fmt"
	"mime"
	"mime/multipart"
	"os"
	"path/filepath"
	"strings"

	"backend/pkg/apiwarp"
	"backend/pkg/errors"
	"backend/pkg/storage"
)

const maxUploadBytes = 30 << 20 // 30MB，与 storage.MaxBytes 对齐

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
// 存储经 storage.Default 统一分发：配置 COS 时附件直传对象存储（前端拿公网
// 链接，每轮开始由后端从 COS 拉回物化进沙箱）；未配置时落本地 uploads/
//（零依赖开箱即用，链接走 /api/uploads/:name）。
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
		return nil, errors.NewMsg(fmt.Sprintf("文件超过 %dMB 上限", maxUploadBytes>>20))
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

	// 本地驱动不落 Content-Type 元数据，按扩展名推断并归一化（文本类补
	// charset=utf-8，否则浏览器按本地默认编码解码 UTF-8 内容会乱码）
	fileType := storage.NormalizeContentType(mime.TypeByExtension(filepath.Ext(name)))
	return &apiwarp.FilePathData{FileType: fileType, Path: p}, nil
}
