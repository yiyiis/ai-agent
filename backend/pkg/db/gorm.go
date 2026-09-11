package db

import (
	"backend/constants"
	"backend/dal/model"
	"backend/dal/query"
	"fmt"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	gormlog "gorm.io/gorm/logger"
)

var db *gorm.DB
var defaultQ *query.Query

// InitDb 初始化数据库连接与默认查询门面
func InitDb(conf Config) {
	if db != nil {
		panic("全局db只能初始化一次")
	}

	dsn := fmt.Sprintf("%s:%s@tcp(%s)/%s?%s", conf.Username, conf.Password, conf.Path, conf.DbName, conf.Param)
	dialector := mysql.Open(dsn)

	var err error
	db, err = gorm.Open(dialector, &gorm.Config{
		SkipDefaultTransaction: true,
		PrepareStmt:            true,
		Logger:                 &logger{LogLevel: gormlog.Info},
	})
	if err != nil {
		panic(fmt.Sprintf("数据库连接失败: %v", err))
	}

	// 自动同步全量智能体核心表结构（含用户表）
	if err := db.AutoMigrate(
		&model.UserInfo{},
		&model.Session{},
		&model.Message{},
		&model.Skill{},
		&model.SkillVersion{},
		&model.SessionSkill{},
		&model.ApiKey{},
		&model.SkillRun{},
		&model.TokenUsage{},
		&model.Memory{},
		&model.SessionDigest{},
	); err != nil {
		fmt.Printf("警告: 自动同步数据表失败: %v\n", err)
	}

	// 如果系统用户表为空，自动播种默认管理员用户
	var userCount int64
	db.Model(&model.UserInfo{}).Count(&userCount)
	if userCount == 0 {
		defaultUser := model.UserInfo{
			Username: "admin",
			Nickname: "管理员",
			Password: "your_password",
			UserType: int32(constants.SuperManager),
		}
		if err := db.Create(&defaultUser).Error; err == nil {
			fmt.Printf("[INFO] 初始化默认管理员账号: %s (密码: %s)\n", defaultUser.Username, defaultUser.Password)
		}
	}

	defaultQ = query.Use(db)
}

// GetRawDB 获取原生 *gorm.DB（逃生通道）
func GetRawDB() *gorm.DB {
	return db
}
