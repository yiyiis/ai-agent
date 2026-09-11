package db

import (
	"backend/dal/query"
	"fmt"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	gormlog "gorm.io/gorm/logger"
)

var defaultQ *query.Query

// InitDb 初始化数据库连接与默认查询门面
// 表结构由 etc/schema.sql 维护并先行建表（DB-first），此处只负责连接
func InitDb(conf Config) {
	if defaultQ != nil {
		panic("全局db只能初始化一次")
	}

	dsn := fmt.Sprintf("%s:%s@tcp(%s)/%s?%s", conf.Username, conf.Password, conf.Path, conf.DbName, conf.Param)

	gormDB, err := gorm.Open(mysql.Open(dsn), &gorm.Config{
		SkipDefaultTransaction: true,
		PrepareStmt:            true,
		Logger:                 &logger{LogLevel: gormlog.Info},
	})
	if err != nil {
		panic(fmt.Sprintf("数据库连接失败: %v", err))
	}

	defaultQ = query.Use(gormDB)
}
