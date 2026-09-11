package db

import (
	"context"
	gormlog "gorm.io/gorm/logger"
	"log/slog"
	"time"
)

type logger struct {
	LogLevel gormlog.LogLevel
}

func (l *logger) LogMode(level gormlog.LogLevel) gormlog.Interface {
	newLogger := *l
	newLogger.LogLevel = level
	return &newLogger
}

func (l *logger) Info(ctx context.Context, s string, i ...interface{}) {
	if l.LogLevel >= gormlog.Info {
		slog.InfoContext(ctx, s, i...)
	}
}

func (l *logger) Warn(ctx context.Context, s string, i ...interface{}) {
	if l.LogLevel >= gormlog.Warn {
		slog.WarnContext(ctx, s, i...)
	}
}

func (l *logger) Error(ctx context.Context, s string, i ...interface{}) {
	if l.LogLevel >= gormlog.Error {
		slog.ErrorContext(ctx, s, i...)
	}
}

func (l *logger) Trace(ctx context.Context, begin time.Time, fc func() (sql string, rowsAffected int64), err error) {
	sql, affected := fc()
	if err != nil {
		slog.ErrorContext(ctx, "SQL执行异常", "duration", time.Since(begin), "sql", sql, "rows", affected, "err", err)
	} else {
		slog.InfoContext(ctx, "SQL执行", "duration", time.Since(begin), "sql", sql, "rows", affected)
	}
}

var _ gormlog.Interface = (*logger)(nil)
