package db

import (
	"context"
	"backend/dal/query"
)

const key = "gormDb"

// Ctx 从 context 中获取 query.Query 实例
// 严格模式：未注入时抛出 panic，确保上层遵守 context 传递规范
func Ctx(ctx context.Context) *query.Query {
	if ctx == nil {
		panic("传入的 context 为 nil")
	}

	value := ctx.Value(key)
	if value == nil {
		panic("context 未注入 db，请确保请求经过 InjectQuery 中间件，或显式使用 db.WithContext(ctx)")
	}

	queryModel, ok := value.(*query.Query)
	if !ok {
		panic("context 中注入的 db 类型异常")
	}

	return queryModel
}

// WithContext 为非 Gin 请求（如后台异步 Goroutine、单元测试、定时任务）显式注入默认数据库连接
func WithContext(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, key, defaultQ)
}

// Transition 启动数据库事务
// 闭包函数纯净定义为 func(ctx context.Context) error，完全无需暴露出外部用不上的 Q 参数
// 底层将生成的事务连接 tx 重新以同一个 key 覆盖进 context，
// 使下游被调用的所有 DAO/Service 函数继续调 db.Ctx(ctx) 时无感且精准地接入当前事务中
func Transition(ctx context.Context, fc func(ctx context.Context) error) error {
	Q := Ctx(ctx)
	return Q.Transaction(func(tx *query.Query) error {
		txCtx := context.WithValue(ctx, key, tx)
		return fc(txCtx)
	})
}
