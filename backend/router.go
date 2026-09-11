package main

import (
	"time"

	"backend/api"
	. "backend/pkg/apiwarp"
	"github.com/gin-gonic/gin"
)

func RegisterRouter(engine *gin.Engine) {
	// 健康检查
	engine.GET("/api/ping", api.Ping)

	// 模型矩阵接口
	engine.GET("/api/models", api.ListModels)

	// 认证与用户模块
	authGroup := engine.Group("/api/auth")
	{
		authGroup.POST("/login", api.UserLogin)
		authGroup.GET("/me", api.AuthMe)
		authGroup.POST("/register", api.UserRegister)
		authGroup.POST("/logout", api.UserLogout)
	}

	// 会话管理模块（严格基于登录态进行用户隔离）
	sessionGroup := engine.Group("/api/sessions")
	{
		sessionGroup.POST("", api.CreateSession)
		sessionGroup.GET("", api.ListSessions)
		sessionGroup.GET("/:id", api.GetSessionDetail)
		sessionGroup.PATCH("/:id", api.UpdateSession)
		sessionGroup.DELETE("/:id", api.DeleteSession)
		sessionGroup.PUT("/:id/skills", api.SetEnabledSkills)

		// SSE 流式对话长连接
		sessionGroup.POST("/:id/messages", api.SendMessageStream)
	}

	// 技能与记忆模块
	engine.GET("/api/skills", api.ListSkills)
	engine.GET("/api/memories/status", api.MemoryStatus)
	engine.GET("/api/memories", api.ListMemories)

	// 兼容中台与管理端接口
	engine.GET("/api/user/info", TimeOut(time.Second*3), Controller(api.UserInfoDetail))
	engine.POST("/api/user/password", TimeOut(time.Second*3), Controller(api.UpdatePassword))
}

