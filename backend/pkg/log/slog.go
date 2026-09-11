package log

import (
	"github.com/natefinch/lumberjack"
	"log/slog"
	"path"
)

type Config struct {
	Mode Mode   // 日志打印模式: default (Text) / json
	Path string // 日志存储路径，如果不指定，默认打印到控制台
}

type Mode string

const (
	Default Mode = "default"
	JSON    Mode = "json"
)

func InitSlog(conf Config) {
	if conf.Path == "" {
		return
	}

	writer := &lumberjack.Logger{
		Filename:   path.Join(conf.Path, "run.log"),
		MaxSize:    1024,
		MaxAge:     28,
		MaxBackups: 3,
	}

	switch conf.Mode {
	case JSON:
		logger := slog.New(slog.NewJSONHandler(writer, &slog.HandlerOptions{
			AddSource: false,
			Level:     slog.LevelInfo,
		}))
		slog.SetDefault(logger)
	case Default:
		fallthrough
	default:
		logger := slog.New(slog.NewTextHandler(writer, &slog.HandlerOptions{
			AddSource: false,
			Level:     slog.LevelInfo,
		}))
		slog.SetDefault(logger)
	}
}
