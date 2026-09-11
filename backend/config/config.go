package config

import (
	"backend/pkg/db"
	"backend/pkg/jwt"
	"backend/pkg/log"
	"fmt"
	"os"
	"github.com/spf13/viper"
)

type Server struct {
	Port int    `yaml:"Port" mapstructure:"Port"`
	IP   string `yaml:"IP" mapstructure:"IP"`
}

type ProviderConfig struct {
	Name    string `yaml:"Name" mapstructure:"Name"`
	BaseURL string `yaml:"BaseURL" mapstructure:"BaseURL"`
	APIKey  string `yaml:"ApiKey" mapstructure:"ApiKey"`
}

type LLMConfig struct {
	DefaultModel string           `yaml:"DefaultModel" mapstructure:"DefaultModel"`
	Providers    []ProviderConfig `yaml:"Providers" mapstructure:"Providers"`
}

type COSConfig struct {
	SecretID  string `yaml:"SecretId" mapstructure:"SecretId"`
	SecretKey string `yaml:"SecretKey" mapstructure:"SecretKey"`
	Bucket    string `yaml:"Bucket" mapstructure:"Bucket"`
	Region    string `yaml:"Region" mapstructure:"Region"`
	BasePath  string `yaml:"BasePath" mapstructure:"BasePath"`
}

type SandboxConfig struct {
	E2BKey    string `yaml:"E2BKey" mapstructure:"E2BKey"`
	DockerURL string `yaml:"DockerURL" mapstructure:"DockerURL"`
}

type Config struct {
	DbConf  db.Config     `yaml:"DBConf" mapstructure:"DBConf"`
	Server  Server        `yaml:"Server" mapstructure:"Server"`
	Auth    jwt.Config    `yaml:"Auth" mapstructure:"Auth"`
	Log     log.Config    `yaml:"Log" mapstructure:"Log"`
	LLM     LLMConfig     `yaml:"LLM" mapstructure:"LLM"`
	COS     COSConfig     `yaml:"COS" mapstructure:"COS"`
	Sandbox SandboxConfig `yaml:"Sandbox" mapstructure:"Sandbox"`
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
