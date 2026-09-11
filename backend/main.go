package main

import (
	"flag"
	"fmt"

	"backend/config"
	"backend/pkg/db"
	"backend/pkg/jwt"
	"backend/pkg/log"
	"backend/pkg/provider"
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

	// 3. 注册 LLM Provider
	for _, p := range conf.LLM.Providers {
		openAIProvider := provider.NewOpenAICompatProvider(p.BaseURL, p.APIKey)
		provider.RegisterProvider(p.Name, openAIProvider)
		fmt.Printf("[INFO] 成功注册模型 Provider: %s (BaseURL: %s)\n", p.Name, p.BaseURL)
	}

	// 4. 初始化 Gin Engine
	gin.SetMode(gin.ReleaseMode)
	engine := gin.Default()

	// 5. 挂载全局中间件
	engine.
		Use(cors.Default()).
		Use(db.InjectQuery). // 注入 DB 门面对象至 context
		Use(jwt.CheckLogin)  // JWT 登录校验与 claims 注入

	// 6. 注册路由
	RegisterRouter(engine)

	// 7. 启动服务
	addr := fmt.Sprintf("%s:%d", conf.Server.IP, conf.Server.Port)
	fmt.Printf("服务启动成功，监听地址: %s\n", addr)
	err := engine.Run(addr)
	if err != nil {
		panic(err)
	}
}
