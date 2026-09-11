package jwt

import (
	"context"
	"net/http"
	"slices"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

// CheckLogin 全局 JWT 鉴权中间件：白名单放行、多源提取 Token、解析后注入 Context
func CheckLogin(gCtx *gin.Context) {
	reqPath := gCtx.Request.URL.Path

	// 1. 白名单路径直接放行
	if slices.Contains(config.IgnorePath, reqPath) || slices.Contains(config.IgnorePath, gCtx.FullPath()) {
		gCtx.Next()
		return
	}

	// 2. 仅对 /api 前缀的路由强制鉴权
	if !strings.HasPrefix(reqPath, "/api") {
		gCtx.Next()
		return
	}

	// 3. 多源提取 Token：Header x-token > Header Authorization > Cookie agents_token > Cookie jwt_token
	var tokenStr string
	if t := gCtx.Request.Header.Get("x-token"); t != "" {
		tokenStr = strings.TrimSpace(t)
	} else if auth := gCtx.Request.Header.Get("Authorization"); auth != "" {
		tokenStr = strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
	} else if cookie, err := gCtx.Cookie("agents_token"); err == nil && cookie != "" {
		tokenStr = strings.TrimSpace(cookie)
	} else if cookie, err := gCtx.Cookie("jwt_token"); err == nil && cookie != "" {
		tokenStr = strings.TrimSpace(cookie)
	}

	// 4. 校验并解析 Token
	token, err := jwt.ParseWithClaims(tokenStr, &TokenClaims{}, func(t *jwt.Token) (interface{}, error) {
		return []byte(config.Secret), nil
	}, jwt.WithExpirationRequired())
	if err != nil || !token.Valid {
		detail := "未登录或登录态已失效"
		if err != nil && strings.Contains(err.Error(), "expired") {
			detail = "登录已过期，请重新登录"
		}
		gCtx.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized", "detail": detail})
		gCtx.Abort()
		return
	}

	// 5. 将登录身份植入 Context
	claims := token.Claims.(*TokenClaims)
	ctx := context.WithValue(gCtx.Request.Context(), "tokenClaims", *claims)
	gCtx.Request = gCtx.Request.WithContext(ctx)

	gCtx.Next()
}
