package constants

type UserType int32

const (
	SuperManager UserType = 1 // 超级管理员
	Manager      UserType = 2 // 管理员
	User         UserType = 3 // 普通用户
)
