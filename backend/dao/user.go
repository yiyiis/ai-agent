package dao

import (
	"context"
	"backend/constants"
	"backend/dal/model"
	"backend/pkg/db"
	"backend/pkg/errors"
	"backend/pkg/jwt"
	"gorm.io/gorm"
)

// GetUserByUsername 根据用户名查询用户
func GetUserByUsername(ctx context.Context, username string) (*model.UserInfo, error) {
	query := db.Ctx(ctx)
	u := query.UserInfo

	userModel, err := query.WithContext(ctx).
		UserInfo.
		Where(u.Username.Eq(username)).
		First()
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
		return nil, errors.Join(err, errors.New("查询用户记录失败"), errors.NewMsg("系统异常"))
	}

	return userModel, nil
}

// GetUserById 根据用户 ID 查询用户
func GetUserById(ctx context.Context, userId int32) (*model.UserInfo, error) {
	query := db.Ctx(ctx)
	u := query.UserInfo

	userInfo, err := query.WithContext(ctx).
		UserInfo.
		Where(u.UserID.Eq(userId)).
		First()
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errors.Join(err, errors.NewMsg("没有这个用户"))
		}
		return nil, errors.Join(err, errors.New("查询用户信息失败"), errors.NewMsg("系统异常"))
	}

	return userInfo, nil
}

// AddUser 添加新用户
func AddUser(ctx context.Context, userName, nickName, password, phone string, userType constants.UserType) error {
	query := db.Ctx(ctx)
	u := query.UserInfo

	_, err := query.WithContext(ctx).
		UserInfo.
		Where(u.Username.Eq(userName)).
		First()
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return errors.Join(err, errors.NewMsg("系统异常"))
	} else if err == nil {
		return errors.NewMsg("该用户名的用户已存在")
	}

	err = query.WithContext(ctx).
		UserInfo.
		Create(&model.UserInfo{
			Username: userName,
			Nickname: nickName,
			Password: password,
			UserType: int32(userType),
			Phone:    phone,
		})
	if err != nil {
		return errors.Join(err, errors.NewMsg("创建用户失败"))
	}

	return nil
}

// UpdateUserOption 更新用户选项
type UpdateUserOption struct {
	HeadImage string
	Username  string
	Nickname  string
	Password  string
	UserType  constants.UserType
	Phone     string
}

// UpdateUser 更新用户信息
func UpdateUser(ctx context.Context, userId int32, param UpdateUserOption) error {
	query := db.Ctx(ctx)
	u := query.UserInfo

	_, err := query.WithContext(ctx).
		UserInfo.
		Where(u.UserID.Eq(userId)).
		Updates(&model.UserInfo{
			Username:  param.Username,
			Nickname:  param.Nickname,
			Password:  param.Password,
			HeadImage: param.HeadImage,
			UserType:  int32(param.UserType),
			Phone:     param.Phone,
		})
	if err != nil {
		return errors.Join(err, errors.NewMsg("更新用户信息失败"))
	}

	return nil
}

// UpdatePassword 修改用户密码
func UpdatePassword(ctx context.Context, userId int32, newPassword string) error {
	query := db.Ctx(ctx)
	u := query.UserInfo

	_, err := query.WithContext(ctx).
		UserInfo.
		Where(u.UserID.Eq(userId)).
		First()
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errors.Join(err, errors.NewMsg("用户不存在"))
		}
		return errors.Join(err, errors.New("查找用户失败"), errors.NewMsg("系统异常"))
	}

	_, err = query.WithContext(ctx).
		UserInfo.
		Where(u.UserID.Eq(userId)).
		Update(u.Password, newPassword)
	if err != nil {
		return errors.Join(err, errors.New("更新密码失败"), errors.NewMsg("系统异常"))
	}

	return nil
}

// DeleteUser 删除用户（硬删除示例）
func DeleteUser(ctx context.Context, userId int32) error {
	query := db.Ctx(ctx)
	u := query.UserInfo

	_, err := query.WithContext(ctx).
		UserInfo.
		Where(u.UserID.Eq(userId)).
		First()
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errors.Join(err, errors.NewMsg("没有这个用户"))
		}
		return errors.Join(err, errors.New("查询用户失败"), errors.NewMsg("系统异常"))
	}

	_, err = query.WithContext(ctx).
		UserInfo.
		Unscoped().
		Where(u.UserID.Eq(userId)).
		Delete()
	if err != nil {
		return errors.Join(err, errors.New("删除用户失败"), errors.NewMsg("系统异常"))
	}

	return nil
}

// ComparePassword 密码比对辅助方法
func ComparePassword(userModel *model.UserInfo, password string) bool {
	if userModel == nil {
		return false
	}
	return userModel.Password == password
}

// CheckPermission 检查当前请求用户的操作权限
func CheckPermission(ctx context.Context, permissions ...constants.UserType) error {
	tc, err := jwt.GetTokenClaimsFromCtx(ctx)
	if err != nil {
		return err
	}

	for _, permission := range permissions {
		if tc.UserType == permission {
			return nil
		}
	}

	return errors.NewMsg("你没有权限进行此操作")
}
