package main

import (
	"time"

	"backend/api"
	. "backend/pkg/apiwarp"
	"github.com/gin-gonic/gin"
)

func RegisterRouter(engine *gin.Engine) {
	// 健康检查（探活直读，不走信封）
	engine.GET("/api/ping", api.Ping)

	// 模型矩阵接口
	engine.GET("/api/models", Controller(api.ListModels))

	// 认证与用户模块
	authGroup := engine.Group("/api/auth")
	{
		authGroup.POST("/login", Controller(api.UserLogin))
		authGroup.GET("/me", Controller(api.AuthMe))
		authGroup.POST("/register", Controller(api.UserRegister))
		authGroup.POST("/logout", Controller(api.UserLogout))
	}

	// 会话管理模块（严格基于登录态进行用户隔离）
	sessionGroup := engine.Group("/api/sessions")
	{
		sessionGroup.POST("", Controller(api.CreateSession))
		sessionGroup.GET("", Controller(api.ListSessions))
		sessionGroup.GET("/:id", Controller(api.GetSessionDetail))
		sessionGroup.PATCH("/:id", Controller(api.UpdateSession))
		sessionGroup.DELETE("/:id", Controller(api.DeleteSession))
		sessionGroup.PUT("/:id/skills", Controller(api.SetEnabledSkills))

		// SSE 流式对话长连接
		sessionGroup.POST("/:id/messages", Controller(api.SendMessageStream))
		// 重新生成最新回答
		sessionGroup.POST("/:id/messages/regenerate", Controller(api.RegenerateLastMessage))
		// 编辑历史提问重发（截断其后全部记录再重开一轮）
		sessionGroup.PUT("/:id/messages/:mid", Controller(api.EditUserMessage))
	}

	// 技能与记忆模块
	engine.GET("/api/skills", Controller(api.ListSkills))
	engine.GET("/api/memories/status", Controller(api.MemoryStatus))
	engine.GET("/api/memories", Controller(api.ListMemories))

	// 聊天附件上传与下载
	engine.POST("/api/uploads", Controller(api.UploadFile))
	engine.GET("/api/uploads/:name", Controller(api.DownloadAttachment))

	// 兼容中台与管理端接口
	engine.GET("/api/user/info", TimeOut(time.Second*3), Controller(api.UserInfoDetail))
	engine.POST("/api/user/password", TimeOut(time.Second*3), Controller(api.UpdatePassword))
}
