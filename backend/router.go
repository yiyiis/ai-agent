package main

import (
	"time"

	"backend/api"
	. "backend/pkg/apiwarp"
	"github.com/gin-gonic/gin"
)

func RegisterRouter(engine *gin.Engine) {
	// 健康检查
	engine.GET("/api/ping", TimeOut(time.Second*2), Controller(api.Ping))

	// 模型矩阵接口
	engine.GET("/api/models", TimeOut(time.Second*3), Controller(api.ListModels))

	// 会话管理模块
	sessionGroup := engine.Group("/api/sessions")
	{
		sessionGroup.POST("", TimeOut(time.Second*5), Controller(api.CreateSession))
		sessionGroup.GET("", TimeOut(time.Second*5), Controller(api.ListSessions))
		sessionGroup.GET("/:id", TimeOut(time.Second*5), Controller(api.GetSessionDetail))
		sessionGroup.PATCH("/:id", TimeOut(time.Second*5), Controller(api.RenameSession))
		sessionGroup.DELETE("/:id", TimeOut(time.Second*5), Controller(api.DeleteSession))

		// SSE 流式对话长连接
		sessionGroup.POST("/:id/messages", api.SendMessageStream)
	}

	// 认证模块
	engine.POST("/api/auth/login", TimeOut(time.Second*5), Controller(api.UserLogin))
	engine.GET("/api/user/info", TimeOut(time.Second*3), Controller(api.UserInfoDetail))
	engine.POST("/api/user/password", TimeOut(time.Second*3), Controller(api.UpdatePassword))
}
