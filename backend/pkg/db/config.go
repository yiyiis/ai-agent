package db

type Config struct {
	Driver   string `mapstructure:"Driver"`
	DbName   string `mapstructure:"DbName"`
	Path     string `mapstructure:"Path"`
	Username string `mapstructure:"Username"`
	Password string `mapstructure:"Password"`
	Param    string `mapstructure:"Param"`
}
