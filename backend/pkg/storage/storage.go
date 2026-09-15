// Package storage 统一对象存储（阶段四）：沙箱产物导出的对外通道。
//
// 聊天附件不走这里——附件每轮开始都要物化进会话工作区（后端本地），
// 传对象存储再拉回来只是多余的公网往返，因此附件一律落本地 uploads/
// （见 api.UploadFile 的 storage.Local()）。对象存储只服务 export_artifact
// 导出的产物：需要可分享、可公网预览、不占后端带宽的链接。
// 驱动在 Init 时按配置定死：Bucket 留空用 local，配置后直传腾讯云 COS。
package storage

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"backend/pkg/errors"
)

// LocalRoot 本地驱动的存储目录（相对后端运行目录；下载路由与会话附件落工作区时读同源目录，测试可覆写）
var LocalRoot = "uploads"

// MaxBytes 单对象上限：30MB，覆盖常见报告 / 图片 / 表格类产物
const MaxBytes = 30 << 20

// Object 上传产物元数据（与前端 Attachment 契约一致）
type Object struct {
	URL         string
	Filename    string
	Size        int64
	ContentType string
}

// Storage 对象存储驱动
type Storage interface {
	Name() string
	// Put 上传一个对象。name 为用户侧原始文件名（驱动负责清洗与随机命名防碰撞），
	// r 由调用方关闭；size 用于预检与 Content-Length。
	Put(ctx context.Context, name string, r io.Reader, size int64, contentType string) (Object, error)
}

// CheckSize 上传前的体积预检：空文件与超限直接拒绝，避免读流读一半才发现
func CheckSize(size int64) error {
	if size <= 0 {
		return errors.NewMsg("空文件")
	}
	if size > MaxBytes {
		return errors.NewMsg(fmt.Sprintf("文件超过 %dMB 上限", MaxBytes>>20))
	}
	return nil
}

// SanitizeName 生成存储名：6 字节随机前缀（防碰撞与路径猜测）+ 清洗后的原始文件名。
// 只保留常见安全字符，避免路径注入与怪异文件名；清洗后为空则退回 file。
func SanitizeName(orig string) string {
	ext := strings.ToLower(filepath.Ext(orig))
	base := strings.TrimSuffix(filepath.Base(orig), filepath.Ext(orig))
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

// Config 对象存储配置段（包自持配置、由 config 包组合，yaml 键与 etc/config.yaml 对齐）；
// Bucket 为空时使用本地驱动，无需任何凭据
type Config struct {
	SecretID  string `yaml:"SecretId" mapstructure:"SecretId"`
	SecretKey string `yaml:"SecretKey" mapstructure:"SecretKey"`
	Bucket    string `yaml:"Bucket" mapstructure:"Bucket"`
	Region    string `yaml:"Region" mapstructure:"Region"`
	BasePath  string `yaml:"BasePath" mapstructure:"BasePath"` // COS 对象键前缀（如 ai-agent/prod）
	Endpoint  string `yaml:"Endpoint" mapstructure:"Endpoint"` // 覆盖默认域名（自建网关/测试），一般留空
}

var defaultStorage Storage = localStorage{}

// Init 按配置装配全局存储驱动（main 启动时调用一次）
func Init(cfg Config) {
	if cfg.Bucket == "" {
		defaultStorage = localStorage{}
	} else {
		defaultStorage = newCOSStorage(cfg)
	}
	fmt.Printf("[INFO] 对象存储驱动: %s\n", defaultStorage.Name())
}

// Default 全局默认存储驱动
func Default() Storage {
	return defaultStorage
}

// Local 强制本地驱动（不受 Init 配置影响）：聊天附件用——无论是否配置 COS
// 都落本地 uploads/，前端经 /api/uploads/:name 预览，工作区物化零网络开销
func Local() Storage {
	return localStorage{}
}
