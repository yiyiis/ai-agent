package jwt

import (
	"context"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

// CheckLogin 全局 JWT 鉴权中间件，支持多源 Token 提取与用户隔离
func CheckLogin(gCtx *gin.Context) {
	reqPath := gCtx.Request.URL.Path
	fullPath := gCtx.FullPath()

	// 1. 判断是否属于白名单开放路由
	isIgnored := false
	for _, pattern := range config.IgnorePath {
		if pattern == fullPath || pattern == reqPath {
			isIgnored = true
			break
		}
		if strings.HasSuffix(pattern, "*") {
			prefix := strings.TrimSuffix(pattern, "*")
			if strings.HasPrefix(reqPath, prefix) || strings.HasPrefix(fullPath, prefix) {
				isIgnored = true
				break
			}
		}
	}

	// 2. 多源提取 Token：Header x-token > Header Authorization > Cookie agents_token > Cookie jwt_token
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

	// 3. 校验并解析 Token
	var identity AuthIdentity
	var parseErr error

	if tokenStr != "" {
		token, err := jwt.ParseWithClaims(tokenStr, &TokenClaims{}, func(token *jwt.Token) (interface{}, error) {
			return []byte(config.Secret), nil
		})

		if err == nil && token.Valid {
			if claims, ok := token.Claims.(*TokenClaims); ok {
				uid := claims.Uid
				if uid == 0 {
					uid = claims.UserId
				}
				cid := claims.CompanyId
				if cid == 0 {
					cid = claims.CompanyID
				}
				name := claims.Name
				if name == "" {
					name = claims.Username
				}

				identity = AuthIdentity{
					UserID:    uid,
					CompanyID: cid,
					Name:      name,
					Username:  claims.Username,
					UserType:  claims.UserType,
				}
			}
		} else {
			parseErr = err
		}
	}

	// 4. 将身份信息植入 Context
	if identity.UserID > 0 {
		ctx := context.WithValue(gCtx.Request.Context(), "authIdentity", identity)
		gCtx.Request = gCtx.Request.WithContext(ctx)
	}

	// 5. 如果路由需要强制鉴权
	if !isIgnored && strings.HasPrefix(reqPath, "/api") {
		if identity.UserID == 0 {
			msg := "未登录或登录态已失效"
			if parseErr != nil && strings.Contains(parseErr.Error(), "expired") {
				msg = "登录已过期，请重新登录"
			}
			gCtx.JSON(http.StatusUnauthorized, gin.H{
				"error":  "unauthorized",
				"detail": msg,
			})
			gCtx.Abort()
			return
		}
	}

	gCtx.Next()
}
