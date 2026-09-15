package main

import (
	"flag"
	"fmt"

	"backend/config"
	"backend/pkg/db"
	"backend/pkg/jwt"
	"backend/pkg/log"
	"backend/pkg/provider"
	"backend/pkg/sandbox"
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

	// 3. 注册 LLM Provider 及模型路由
	provider.InitProviders(conf.LLM)

	// 4. 初始化对象存储与执行沙箱（附件上传与工具执行的分发底座）
	storage.Init(conf.COS)
	sandbox.Init(conf.Sandbox)

	// 5. 初始化 Gin Engine
	gin.SetMode(gin.ReleaseMode)
	engine := gin.Default()

	// 6. 挂载全局中间件
	engine.
		Use(cors.Default()).
		Use(db.InjectQuery). // 注入 DB 门面对象至 context
		Use(jwt.CheckLogin)  // JWT 登录校验与 claims 注入

	// 7. 注册路由
	RegisterRouter(engine)

	// 8. 启动服务
	addr := fmt.Sprintf("%s:%d", conf.Server.IP, conf.Server.Port)
	fmt.Printf("服务启动成功，监听地址: %s\n", addr)
	err := engine.Run(addr)
	if err != nil {
		panic(err)
	}
}
