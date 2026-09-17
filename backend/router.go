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
		// 优雅中止当前回答（后台 Turn 不随断连终止，需显式中止）
		sessionGroup.POST("/:id/messages/stop", Controller(api.StopSessionTurn))
		// 断线重连补播（携带 from_seq 从环形缓冲续传）
		sessionGroup.GET("/:id/stream", Controller(api.AttachSessionStream))
	}

	// 技能与记忆模块（管理闭环：zip 上传 / 删除 / 编辑 / 详情 / 机密 / 版本历史 /
	// 在线文件编辑 / 从空新建 / 下载）
	engine.GET("/api/skills", Controller(api.ListSkills))
	engine.POST("/api/skills", Controller(api.CreateEmptySkill))
	engine.POST("/api/skills/upload", Controller(api.UploadSkill))
	engine.GET("/api/skills/:id/download", Controller(api.DownloadSkill))
	engine.PATCH("/api/skills/:id", Controller(api.UpdateSkill))
	engine.DELETE("/api/skills/:id", Controller(api.DeleteSkill))
	engine.GET("/api/skills/:id/body", Controller(api.GetSkillBody))
	engine.GET("/api/skills/:id/secrets", Controller(api.GetSkillSecrets))
	engine.PUT("/api/skills/:id/secrets", Controller(api.UpdateSkillSecrets))
	engine.GET("/api/skills/:id/versions", Controller(api.ListSkillVersions))
	engine.GET("/api/skills/:id/tree", Controller(api.GetSkillTree))
	engine.GET("/api/skills/:id/file", Controller(api.GetSkillFile))
	engine.PUT("/api/skills/:id/file", Controller(api.WriteSkillFile))
	engine.POST("/api/skills/:id/file", Controller(api.CreateSkillFile))
	engine.DELETE("/api/skills/:id/file", Controller(api.DeleteSkillFile))
	engine.POST("/api/skills/:id/file/rename", Controller(api.RenameSkillFile))
	engine.GET("/api/memories/status", Controller(api.MemoryStatus))
	engine.GET("/api/memories", Controller(api.ListMemories))

	// 聊天附件上传与下载
	engine.POST("/api/uploads", Controller(api.UploadFile))
	engine.GET("/api/uploads/:name", Controller(api.DownloadAttachment))

	// 兼容中台与管理端接口
	engine.GET("/api/user/info", TimeOut(time.Second*3), Controller(api.UserInfoDetail))
	engine.POST("/api/user/password", TimeOut(time.Second*3), Controller(api.UpdatePassword))
}
