package storage

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"backend/pkg/errors"
)

// cosStorage 腾讯云 COS 驱动：按 XML API 规范手工签名后 PUT 直传。
// 不引官方 SDK——签名只涉及两层 HMAC-SHA1，手写可避免整棵依赖树（与 provider 手写 HTTP 客户端同一取向）。
type cosStorage struct {
	cfg    Config
	client *http.Client
}

func newCOSStorage(cfg Config) *cosStorage {
	return &cosStorage{
		cfg:    cfg,
		client: &http.Client{Timeout: 2 * time.Minute},
	}
}

func (c *cosStorage) Name() string { return "cos" }

func (c *cosStorage) endpoint() string {
	if c.cfg.Endpoint != "" {
		return strings.TrimRight(c.cfg.Endpoint, "/")
	}
	return fmt.Sprintf("https://%s.cos.%s.myqcloud.com", c.cfg.Bucket, c.cfg.Region)
}

func (c *cosStorage) Put(ctx context.Context, name string, r io.Reader, size int64, contentType string) (Object, error) {
	if err := CheckSize(size); err != nil {
		return Object{}, err
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	key := path.Join(c.cfg.BasePath, SanitizeName(name))
	uri := "/" + key
	endpoint := c.endpoint()

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint+uri, r)
	if err != nil {
		return Object{}, errors.Join(err, errors.New("build cos request"), errors.NewMsg("上传对象存储失败"))
	}
	req.ContentLength = size
	req.Header.Set("Content-Type", contentType)
	// 请求体不参与 XML API 签名，头部与参数也不锁定（q-header-list 留空），
	// 签名仅由方法 / 路径 / 时间窗构成，足够防第三方伪造写入
	req.Header.Set("Authorization", cosAuthorization(c.cfg.SecretID, c.cfg.SecretKey, http.MethodPut, uri, time.Now()))

	resp, err := c.client.Do(req)
	if err != nil {
		return Object{}, errors.Join(err, errors.New("cos put "+key), errors.NewMsg("上传对象存储失败"))
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return Object{}, errors.Join(
			errors.New(fmt.Sprintf("cos put %s: HTTP %d %s", key, resp.StatusCode, strings.TrimSpace(string(body)))),
			errors.NewMsg("上传对象存储失败"))
	}

	return Object{
		URL:         endpoint + uri,
		Filename:    name,
		Size:        size,
		ContentType: contentType,
	}, nil
}

// signDuration 签名有效窗口：足够覆盖慢速上传，又不会把泄漏的签名暴露太久
const signDuration = 10 * time.Minute

// cosAuthorization 生成 COS XML API 的 Authorization 头（临时密钥方案的正交简化版）：
//
//	KeyTime      = start;end（unix 秒）
//	SignKey      = hex(hmac_sha1(SecretKey, KeyTime))
//	HttpString   = lowercase(method)\nUriPath\nparams\nheaders\n   （method 必须小写；
//	               params/headers 不参与签名时留空段，官方示例 "put\n/test.file\n\n\n"）
//	StringToSign = sha1\nKeyTime\nhex(sha1(HttpString))\n
//	Signature    = hex(hmac_sha1(SignKey, StringToSign))
func cosAuthorization(secretID, secretKey, method, uriPath string, now time.Time) string {
	start := now.Unix()
	end := now.Add(signDuration).Unix()
	keyTime := strconv.FormatInt(start, 10) + ";" + strconv.FormatInt(end, 10)

	mac := hmac.New(sha1.New, []byte(secretKey))
	mac.Write([]byte(keyTime))
	signKey := hex.EncodeToString(mac.Sum(nil))

	httpString := strings.ToLower(method) + "\n" + uriPath + "\n\n\n"
	httpStringSum := sha1.Sum([]byte(httpString))

	sts := "sha1\n" + keyTime + "\n" + hex.EncodeToString(httpStringSum[:]) + "\n"
	mac = hmac.New(sha1.New, []byte(signKey))
	mac.Write([]byte(sts))
	signature := hex.EncodeToString(mac.Sum(nil))

	return strings.Join([]string{
		"q-sign-algorithm=sha1",
		"q-ak=" + url.QueryEscape(secretID),
		"q-sign-time=" + keyTime,
		"q-key-time=" + keyTime,
		"q-header-list=",
		"q-url-param-list=",
		"q-signature=" + signature,
	}, "&")
}
