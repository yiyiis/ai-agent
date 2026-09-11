package db

import (
	"context"
	"github.com/gin-gonic/gin"
)

// InjectQuery 中间件：为经过 Gin 路由的请求上下文绑定默认数据库查询对象
func InjectQuery(gCtx *gin.Context) {
	ctx := gCtx.Request.Context()
	gCtx.Request = gCtx.Request.WithContext(context.WithValue(ctx, key, defaultQ))
	gCtx.Next()
}
