package jwt

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"strings"

	"backend/constants"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

func CheckLogin(gCtx *gin.Context) {
	fullPath := gCtx.FullPath()
	reqPath := gCtx.Request.URL.Path

	// 白名单路径直接放行（支持全匹配与前缀通配符）
	for _, pattern := range config.IgnorePath {
		if pattern == fullPath || pattern == reqPath {
			gCtx.Next()
			return
		}
		if strings.HasSuffix(pattern, "*") {
			prefix := strings.TrimSuffix(pattern, "*")
			if strings.HasPrefix(reqPath, prefix) || strings.HasPrefix(fullPath, prefix) {
				gCtx.Next()
				return
			}
		}
	}

	if !strings.HasPrefix(fullPath, "/api") {
		gCtx.Next()
		return
	}

	token := gCtx.Request.Header.Get("Authorization")
	if token == "" {
		// 方便未登录调试
		tc := TokenClaims{UserId: 1, UserType: constants.User}
		ctx := gCtx.Request.Context()
		gCtx.Request = gCtx.Request.WithContext(context.WithValue(ctx, "tokenClaims", tc))
		gCtx.Next()
		return
	}

	token = strings.TrimPrefix(token, "Bearer ")

	parse, err := jwt.Parse(token, func(token *jwt.Token) (interface{}, error) {
		return []byte(config.Secret), nil
	}, jwt.WithExpirationRequired())
	if err != nil {
		slog.Error("jwt验证失败", "err", err, "jwt", token)
		gCtx.Status(401)
		gCtx.Abort()
		return
	}

	var tc TokenClaims
	split := strings.Split(parse.Raw, ".")
	if len(split) < 2 {
		slog.Error("jwt结构不合法", "jwt", token)
		gCtx.Status(401)
		gCtx.Abort()
		return
	}

	jsonData, err := io.ReadAll(base64.NewDecoder(base64.RawStdEncoding, strings.NewReader(split[1])))
	if err != nil {
		slog.Error("jwt base64解码错误", "err", err)
		gCtx.Status(401)
		gCtx.Abort()
		return
	}

	err = json.Unmarshal(jsonData, &tc)
	if err != nil {
		slog.Error("jwt payload解析错误", "err", err)
		gCtx.Status(401)
		gCtx.Abort()
		return
	}

	ctx := gCtx.Request.Context()
	gCtx.Request = gCtx.Request.WithContext(context.WithValue(ctx, "tokenClaims", tc))
}
