package api

import (
	"context"
	"backend/dao"
	"backend/pkg/db"
	"backend/pkg/errors"
	"backend/pkg/jwt"
)

// UserInfoRequest 用户信息获取请求
type UserInfoRequest struct{}

// UserDetailResp 用户信息响应
type UserDetailResp struct {
	UserId   int32  `json:"user_id"`
	Username string `json:"username"`
	Nickname string `json:"nickname"`
	Phone    string `json:"phone"`
	UserType int32  `json:"user_type"`
}

// UserInfoDetail 获取当前登录人详细信息
func UserInfoDetail(ctx context.Context, req *UserInfoRequest) (*UserDetailResp, error) {
	tc, err := jwt.GetTokenClaimsFromCtx(ctx)
	if err != nil {
		return nil, err
	}

	userInfo, err := dao.GetUserById(ctx, int32(tc.UserID))
	if err != nil {
		return nil, err
	}

	return &UserDetailResp{
		UserId:   userInfo.UserID,
		Username: userInfo.Username,
		Nickname: userInfo.Nickname,
		Phone:    userInfo.Phone,
		UserType: userInfo.UserType,
	}, nil
}

// UpdatePasswordRequest 修改密码请求
type UpdatePasswordRequest struct {
	OldPassword string `json:"old_password" binding:"required"`
	NewPassword string `json:"new_password" binding:"required,min=6"`
}

// UpdatePasswordResp 修改密码响应
type UpdatePasswordResp struct {
	Success bool `json:"success"`
}

// UpdatePassword 用户修改密码（演示事务 db.Transition 无感覆盖 context 的使用）
func UpdatePassword(ctx context.Context, req *UpdatePasswordRequest) (*UpdatePasswordResp, error) {
	tc, err := jwt.GetTokenClaimsFromCtx(ctx)
	if err != nil {
		return nil, err
	}

	// 演示使用 db.Transition 事务保护
	// 闭包函数签名优化为纯粹的 func(ctx context.Context) error
	err = db.Transition(ctx, func(txCtx context.Context) error {
		userInfo, err := dao.GetUserById(txCtx, int32(tc.UserID))
		if err != nil {
			return err
		}

		if !dao.ComparePassword(userInfo, req.OldPassword) {
			return errors.NewMsg("原密码错误")
		}

		return dao.UpdatePassword(txCtx, int32(tc.UserID), req.NewPassword)
	})
	if err != nil {
		return nil, err
	}

	return &UpdatePasswordResp{Success: true}, nil
}
