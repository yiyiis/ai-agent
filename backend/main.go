package main

import (
	"context"
	"flag"
	"fmt"

	"backend/api"
	"backend/config"
	"backend/dao"
	"backend/pkg/ctxpolicy"
	"backend/pkg/db"
	"backend/pkg/dispatcher"
	"backend/pkg/jwt"
	"backend/pkg/log"
	"backend/pkg/mcp"
	"backend/pkg/provider"
	"backend/pkg/sandbox"
	"backend/pkg/skill"
	"backend/pkg/storage"
	"backend/pkg/validate"
	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
)

var confPath = flag.String("conf", "./etc/config.yaml", "配置文件路径")

func main() {
	flag.Parse()

	// 1. 加载配置
	conf := config.LoadConfig(*confPath)

	// 2. 初始化基础设施
	db.InitDb(conf.DbConf)
	jwt.InitJwt(conf.Auth)
	log.InitSlog(conf.Log)
	validate.InitGinValidate()
	// Single-instance recovery: a previous process cannot still own these runs.
	// Fail startup if the migration/recovery failed instead of hiding lost work.
	if err := dao.InterruptAgentRuns(db.WithContext(context.Background())); err != nil {
		panic(fmt.Sprintf("恢复 Agent 运行状态失败，请确认已执行多 Agent 迁移: %v", err))
	}

	// 3. 注册 LLM Provider 及模型路由（含上下文窗口表装载）
	provider.InitProviders(conf.LLM)
	// 上下文组装分区策略（热区阈值/预算/图片保留数；零值保持默认）
	ctxpolicy.Init(conf.AgentPolicy)

	// 4. 初始化对象存储与执行沙箱（附件上传与工具执行的分发底座）
	storage.Init(conf.COS)
	sandbox.Init(conf.Sandbox)

	// 5. 初始化技能库并同步注册表（阶段五：SKILL.md 目录 → skills 表投影）
	skill.Init(conf.Skill)
	api.InitSkillRegistry()

	// 阶段八：MCP 客户端引擎 + 广场种子投影（config → mcp_servers，system=1）
	mcp.Init(conf.MCP)
	api.InitMcpSeeds()

	// 阶段六：初始化并启动后台异步任务调度器 (Dispatcher)
	disp := dispatcher.Init(10)
	disp.Start(context.Background())
	defer disp.Stop()

	// 6. 初始化 Gin Engine
	gin.SetMode(gin.ReleaseMode)
	engine := gin.Default()

	// 7. 挂载全局中间件
	engine.
		Use(cors.Default()).
		Use(db.InjectQuery). // 注入 DB 门面对象至 context
		Use(jwt.CheckLogin)  // JWT 登录校验与 claims 注入

	// 8. 注册路由
	RegisterRouter(engine)

	// 9. 启动服务
	addr := fmt.Sprintf("%s:%d", conf.Server.IP, conf.Server.Port)
	fmt.Printf("服务启动成功，监听地址: %s\n", addr)
	err := engine.Run(addr)
	if err != nil {
		panic(err)
	}
}
