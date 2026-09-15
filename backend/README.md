# DAO Template 后端脚手架

这是一个基于 **Go + Gin + GORM GEN** 的现代轻量级后端脚手架。基于**泛型 Controller 适配器 + 领域 DAO + GORM GEN 强类型数据层**，致力于提供类型安全、事务优雅、极低心智负担的开发体验。

---

## 目录结构设计

```text
backend/
├── api/                       # 控制层 / 业务用例（基于泛型 Controller 包装）
│   ├── auth.go                # 登录、鉴权等接口
│   └── user.go                # 用户业务接口
├── config/                    # Viper 配置解析
│   └── config.go
├── constants/                 # 常量与枚举
│   └── var.go
├── dao/                       # 业务数据访问层（组装 Where、Between、排序、分页等具体查询）
│   └── user.go                # 用户相关的数据访问与单表/多表业务查询
├── dal/                       # 底层数据访问设施（Data Access Layer）
│   ├── gen.go                 # GORM GEN 代码生成器脚本
│   ├── model/                 # GEN 自动生成的数据库原始表实体 (PO)
│   └── query/                 # GEN 自动生成的强类型 Query 门面
├── etc/                       # 配置文件目录
│   └── config.yaml
├── pkg/                       # 基础设施与通用组件
│   ├── apiwarp/               # 泛型 Controller 适配器（参数自动绑定校验、统一 JSON 响应）
│   ├── db/                    # 数据库初始化、Context 事务隐式传播、连接管理
│   ├── errors/                # 统一错误系统（面向前端的 MsgErr 与调用栈 StackErr）
│   ├── jwt/                   # JWT 鉴权与中间件
│   ├── log/                   # 结构化日志 (slog + lumberjack 轮转)
│   ├── sandbox/               # 多驱动执行沙箱（本地隔离 / Docker 容器 / E2B 云端）与生命周期治理
│   ├── storage/               # 统一对象存储（本地 uploads / 腾讯云 COS 签名直传）
│   └── validate/              # 参数校验器与中文翻译
├── .gitignore
├── go.mod
├── main.go                    # 程序启动入口
├── router.go                  # 路由注册与装配
└── README.md
```

---

## 核心设计思想与亮点

### 1. 泛型 Controller 包装器 (`pkg/apiwarp`)
告别繁琐手写 `c.JSON(200, ...)` 和 `c.ShouldBind`。业务接口声明为纯粹的输入输出函数：
```go
func UserLogin(ctx context.Context, req *UserLoginRequest) (*UserLoginResp, error)
```
通过 `Controller(api.UserLogin)` 自动包装为标准的 `gin.HandlerFunc`：
* 自动完成参数绑定与基于 validator 的中文错误翻译；
* 统一标准化 JSON 响应格式（`{"code": 0, "msg": "", "data": ...}`）；
* 自动捕获错误与 panic，记录带调用栈的结构化日志。

### 2. Context 隐式事务无感传播 (`pkg/db`)
彻底解决 Go 开发中为了事务不得不给每个函数塞 `tx *gorm.DB` 的痛点：
```go
err := db.Transition(ctx, func(txCtx context.Context) error {
    // 闭包函数纯净定义为 func(ctx context.Context) error，无需暴露无用的 Q 参数
    // 下游被调用的所有函数继续传递 txCtx，内部 db.Ctx(txCtx) 自动精准接入当前事务！
    if err := dao.UpdateUser(txCtx, ...); err != nil {
        return err
    }
    return dao.SaveLog(txCtx, ...)
})
```
* **嵌套事务支持**：基于 GORM 底层 `SavePoint` 保存点机制，事务套事务安全无害。
* **严格约束与显式注入**：默认 `db.Ctx(ctx)` 实行严格检查；如需在后台异步协程、定时任务或单元测试中使用纯净 context，可通过 `db.WithContext(ctx)` 显式注入。

### 3. GORM GEN 强类型数据底座 (`dal/`)
* 由 `gorm.io/gen` 逆向数据库表一键生成；
* 消除手写 SQL 字符串拼写错误，具备编译期类型检查与 IDE 自动补全；
* 集中统一管理 `dal/query` 与 `dal/model`，杜绝 Go 跨包循环引用问题。

---

## 快速上手

### 1. 安装依赖
```bash
go mod tidy
```

### 2. 初始化数据库

表结构以 `deploy/sql/schema.sql` 为唯一源头（DB-first）：修改表结构先改本文件，再执行建表：

```bash
docker exec -i ai-agent-mysql mysql -uroot -p"你的密码" --default-character-set=utf8mb4 < deploy/sql/schema.sql
```

> 注意：脚本包含 `DROP TABLE` 与 `USE` 语句，执行会清空目标库中的表数据，执行前请确认连接的库名。

首次运行前必须先执行本脚本建表，应用启动只负责连接数据库，不自动建表。

### 3. 生成数据库代码 (`dal/`)

表建好后，运行生成器逆向生成 model 与强类型 query（新增表先在 `dal/gen.go` 的 `tables` 中登记）：

```bash
cd backend/dal
go run .
```

连接串来源优先级：`-dsn` 参数 > 环境变量 `DB_DSN` > 配置文件（默认读 `etc/config.yaml`，存在 `etc/config.local.yaml` 时优先；可用 `-conf` 指定其他路径）。

### 4. 运行项目
```bash
go run . -conf="./etc/config.yaml"
```

---

## 新功能开发步骤

1. **生成 DAL 代码**：在 `dal/` 下运行 `go run gen.go` 生成新表的 Query 与 Model。
2. **编写 DAO**：在 `dao/` 下编写该实体的查询与写入逻辑，通过 `query := db.Ctx(ctx)` 获取连接。
3. **编写 API**：在 `api/` 下定义请求与响应结构体，编写业务用例函数。
4. **注册路由**：在 `router.go` 中通过 `engine.POST("/url", Controller(api.YourHandler))` 完成挂载。
