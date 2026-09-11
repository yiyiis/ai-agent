package main

import (
	"flag"
	"fmt"
	"gorm.io/driver/mysql"
	"gorm.io/gen"
	"gorm.io/gorm"
	"os"
)

var (
	dsn = flag.String("dsn", "", "MySQL DSN, 例如: root:123456@(127.0.0.1:3306)/your_db?charset=utf8mb4&parseTime=True&loc=Local")
)

func main() {
	flag.Parse()

	dbDSN := *dsn
	if dbDSN == "" {
		dbDSN = os.Getenv("DB_DSN")
	}
	if dbDSN == "" {
		// 默认开发连接串，可根据实际数据库调整
		dbDSN = "root:123456@(127.0.0.1:3306)/ai_agent?charset=utf8mb4&parseTime=True&loc=Local"
	}

	fmt.Println("Connecting to DB:", dbDSN)

	g := gen.NewGenerator(gen.Config{
		OutPath:      "./query",
		ModelPkgPath: "./model",
		Mode:         gen.WithDefaultQuery | gen.WithQueryInterface,
	})

	gormdb, err := gorm.Open(mysql.Open(dbDSN))
	if err != nil {
		panic(fmt.Sprintf("连接数据库失败: %v", err))
	}
	g.UseDB(gormdb)

	// 根据数据库全部表生成强类型 DAO
	g.ApplyBasic(g.GenerateAllTable()...)

	// 执行生成
	g.Execute()
	fmt.Println("GEN 代码生成完成！")
}
