package apiwarp

import (
	"context"
	"github.com/gin-gonic/gin"
	"time"
)

// TimeOut 请求超时控制中间件
func TimeOut(duration time.Duration) gin.HandlerFunc {
	return func(gCtx *gin.Context) {
		ctx := gCtx.Request.Context()
		ctx, stopFunc := context.WithTimeout(ctx, duration)
		defer stopFunc()
		gCtx.Request = gCtx.Request.WithContext(ctx)
		gCtx.Next()
	}
}
