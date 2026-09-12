package api

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"backend/pkg/agent"

	"github.com/gin-gonic/gin"
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

// UploadFile 处理 POST /api/uploads：multipart 字段 file，返回附件元数据
func UploadFile(c *gin.Context) {
	fh, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "缺少 file 字段"})
		return
	}

	orig := fh.Filename
	if orig == "" {
		orig = "upload.bin"
	}
	ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(orig)), ".")
	if blockedExts[ext] {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("不支持上传 .%s 类型的文件", ext)})
		return
	}
	if fh.Size > maxUploadBytes {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": fmt.Sprintf("文件超过 %dMB 上限", maxUploadBytes>>20)})
		return
	}

	src, err := fh.Open()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "读取上传文件失败"})
		return
	}
	defer src.Close()

	name := storedName(orig)
	if err := os.MkdirAll(UploadsRoot, 0o755); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "创建存储目录失败"})
		return
	}
	dst, err := os.Create(filepath.Join(UploadsRoot, name))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存文件失败"})
		return
	}
	defer dst.Close()

	// 限额拷贝：超限立刻断流，不会先把整个文件读进内存/磁盘
	written, err := io.Copy(dst, io.LimitReader(src, maxUploadBytes+1))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存文件失败"})
		return
	}
	if written > maxUploadBytes {
		_ = os.Remove(filepath.Join(UploadsRoot, name))
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": fmt.Sprintf("文件超过 %dMB 上限", maxUploadBytes>>20)})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"url":          "/api/uploads/" + name,
		"filename":     orig,
		"size":         written,
		"content_type": fh.Header.Get("Content-Type"),
	})
}

// DownloadAttachment 处理 GET /api/uploads/:name（登录态走 cookie 通道，<img>/预览可直接加载）
func DownloadAttachment(c *gin.Context) {
	name := filepath.Base(c.Param("name")) // 防路径穿越，只取文件名部分
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "非法文件名"})
		return
	}
	p := filepath.Join(UploadsRoot, name)
	if _, err := os.Stat(p); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "文件不存在"})
		return
	}
	c.File(p)
}
