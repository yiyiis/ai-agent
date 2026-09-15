package config

import (
	"backend/pkg/db"
	"backend/pkg/jwt"
	"backend/pkg/log"
	"backend/pkg/provider"
	"backend/pkg/sandbox"
	"backend/pkg/storage"
	"fmt"
	"os"
	"github.com/spf13/viper"
)

type Server struct {
	Port int    `yaml:"Port" mapstructure:"Port"`
	IP   string `yaml:"IP" mapstructure:"IP"`
}

// Config 全量配置。各段配置由所属包自持（LLM/COS/Sandbox 同 DBConf/Auth/Log），
// 本包只负责装配与解析，业务初始化统一走各包的 Init（见 main.go）。
type Config struct {
	DbConf  db.Config     `yaml:"DBConf" mapstructure:"DBConf"`
	Server  Server        `yaml:"Server" mapstructure:"Server"`
	Auth    jwt.Config    `yaml:"Auth" mapstructure:"Auth"`
	Log     log.Config    `yaml:"Log" mapstructure:"Log"`
	LLM     provider.Config `yaml:"LLM" mapstructure:"LLM"`
	COS     storage.Config `yaml:"COS" mapstructure:"COS"`
	Sandbox sandbox.Config `yaml:"Sandbox" mapstructure:"Sandbox"`
}

var globalConf Config

func LoadConfig(confPath string) Config {
	localPath := "./etc/config.local.yaml"
	if _, err := os.Stat(localPath); err == nil && confPath == "./etc/config.yaml" {
		confPath = localPath
	}
	viper.SetConfigFile(confPath)
	err := viper.ReadInConfig()
	if err != nil {
		panic(fmt.Sprintf("读取配置文件失败: %v", err))
	}

	err = viper.Unmarshal(&globalConf)
	if err != nil {
		panic(fmt.Sprintf("解析配置文件失败: %v", err))
	}

	return globalConf
}

func GetConfig() Config {
	return globalConf
}
