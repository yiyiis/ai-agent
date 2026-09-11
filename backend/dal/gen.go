// GORM GEN 代码生成器（DB-first 流程）
// 表结构由 etc/schema.sql 维护并先行建表，本程序从库逆向生成 model 与强类型 query。
// 运行（在 dal 目录下）：go run .
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"backend/config"

	"gorm.io/driver/mysql"
	"gorm.io/gen"
	"gorm.io/gen/field"
	"gorm.io/gorm"
)

var (
	dsn      = flag.String("dsn", "", "MySQL DSN，优先级最高；不传则从配置文件读取 DBConf")
	confPath = flag.String("conf", "../etc/config.yaml", "配置文件路径，默认相对 dal 目录")
)

// tables 表 → 结构体名。新增表：先改 schema.sql 建表，再在此登记后重新运行生成器。
var tables = []struct {
	Table string
	Model string
	Opts  []gen.ModelOpt
}{
	{"user_info", "UserInfo", []gen.ModelOpt{
		// deleted_at 生成 gorm.DeletedAt 保持软删除（gen 对软删除无约定式推断，需显式标记）
		gen.FieldGORMTag("deleted_at", func(tag field.GormTag) field.GormTag {
			return tag.Set("softDelete", "")
		}),
	}},
	{"sessions", "Session", nil},
	{"messages", "Message", nil},
	{"skills", "Skill", nil},
	{"skill_versions", "SkillVersion", nil},
	{"session_skills", "SessionSkill", nil},
	{"api_keys", "ApiKey", nil},
	{"skill_runs", "SkillRun", nil},
	{"token_usages", "TokenUsage", nil},
	{"memories", "Memory", nil},
	{"session_digests", "SessionDigest", nil},
}

// jsonColumns DB 中为 JSON 类型的列，业务代码按 JSON 文本（*string）读写，
// 覆盖 gen 默认的 datatypes.JSON 映射
var jsonColumns = []string{
	"tool_calls", "attachments", "tags", "allowed_skills",
	"params", "result", "artifacts", "progress", "exec_context", "attempts",
}

// resolveDSN 连接串来源优先级：-dsn 参数 > 环境变量 DB_DSN > 配置文件 DBConf
// 配置文件与后端启动共用同一份，优先取 config.local.yaml
func resolveDSN() string {
	if *dsn != "" {
		return *dsn
	}
	if envDSN := os.Getenv("DB_DSN"); envDSN != "" {
		return envDSN
	}

	path := *confPath
	if path == "../etc/config.yaml" {
		if local := strings.TrimSuffix(path, ".yaml") + ".local.yaml"; pathExists(local) {
			path = local
		}
	}
	cf := config.LoadConfig(path)
	dbc := cf.DbConf
	return fmt.Sprintf("%s:%s@tcp(%s)/%s?%s", dbc.Username, dbc.Password, dbc.Path, dbc.DbName, dbc.Param)
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func main() {
	flag.Parse()

	dbDSN := resolveDSN()
	fmt.Println("Connecting to DB:", dbDSN)

	g := gen.NewGenerator(gen.Config{
		OutPath:           "./query",
		ModelPkgPath:      "./model",
		Mode:              gen.WithDefaultQuery | gen.WithQueryInterface,
		FieldNullable:     true, // 可空列生成指针类型，与手写模型语义一致
		FieldWithTypeTag:  true, // gorm 标签携带列类型（type:varchar(36) 等），保证启动时表结构零漂移
		FieldWithIndexTag: true, // gorm 标签携带索引信息（uniqueIndex/index），保证启动时表结构零漂移
	})

	gormdb, err := gorm.Open(mysql.Open(dbDSN))
	if err != nil {
		panic(fmt.Sprintf("连接数据库失败: %v", err))
	}
	g.UseDB(gormdb)

	fieldOpts := []gen.ModelOpt{}
	for _, col := range jsonColumns {
		fieldOpts = append(fieldOpts, gen.FieldType(col, "*string"))
	}

	metas := make([]interface{}, 0, len(tables))
	for _, t := range tables {
		opts := append(append([]gen.ModelOpt{}, fieldOpts...), t.Opts...)
		metas = append(metas, g.GenerateModelAs(t.Table, t.Model, opts...))
	}
	g.ApplyBasic(metas...)

	// 执行生成
	g.Execute()
	fmt.Println("GEN 代码生成完成！")
}
