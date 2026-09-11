package api

import (
	"context"
	"backend/constants"
	"backend/dao"
	"backend/pkg/errors"
	"backend/pkg/jwt"
	"gorm.io/gorm"
)

// PingRequest 健康检查请求
type PingRequest struct{}

// PingResp 健康检查响应
type PingResp struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}

// Ping 健康检查接口
func Ping(ctx context.Context, req *PingRequest) (*PingResp, error) {
	return &PingResp{
		Status:  "ok",
		Message: "pong",
	}, nil
}

// UserLoginRequest 用户登录请求
type UserLoginRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}

// UserLoginResp 用户登录响应
type UserLoginResp struct {
	Token string `json:"token"`
}

// UserLogin 用户登录接口
func UserLogin(ctx context.Context, req *UserLoginRequest) (*UserLoginResp, error) {
	userModel, err := dao.GetUserByUsername(ctx, req.Username)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errors.NewMsg("用户不存在")
		}
		return nil, errors.Join(err, errors.New("查询用户记录错误"), errors.NewMsg("系统异常"))
	}

	if !dao.ComparePassword(userModel, req.Password) {
		return nil, errors.NewMsg("密码错误")
	}

	token, err := jwt.GenAccessToken(jwt.TokenClaims{
		UserId:   userModel.UserID,
		UserType: constants.UserType(userModel.UserType),
	})
	if err != nil {
		return nil, errors.Join(err, errors.New("生成jwt失败"), errors.NewMsg("系统异常"))
	}

	return &UserLoginResp{
		Token: token,
	}, nil
}
