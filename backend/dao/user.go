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

// GetUserByUsername 根据用户名查询用户（若存在多个跨企同名用户，默认返回首个匹配项）
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

// GetUserByUsernameAndCompany 根据企业租户与用户名精确查询用户
func GetUserByUsernameAndCompany(ctx context.Context, username string, companyID int64) (*model.UserInfo, error) {
	query := db.Ctx(ctx)
	u := query.UserInfo

	do := query.WithContext(ctx).UserInfo.Where(u.Username.Eq(username))
	if companyID > 0 {
		do = do.Where(u.CompanyID.Eq(companyID))
	}

	userModel, err := do.First()
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

// AddUser 添加新用户（默认绑定至默认企业 company_id = 1）
func AddUser(ctx context.Context, userName, nickName, password, phone string, userType constants.UserType) error {
	_, err := AddUserWithCompany(ctx, userName, nickName, password, phone, userType, 1)
	return err
}

// AddUserWithCompany 带有租户绑定的新用户添加（在同一 company_id 下用户名唯一）
func AddUserWithCompany(ctx context.Context, userName, nickName, password, phone string, userType constants.UserType, companyID int64) (*model.UserInfo, error) {
	if companyID <= 0 {
		companyID = 1
	}

	query := db.Ctx(ctx)
	u := query.UserInfo

	_, err := query.WithContext(ctx).
		UserInfo.
		Where(u.Username.Eq(userName), u.CompanyID.Eq(companyID)).
		First()
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errors.Join(err, errors.NewMsg("系统异常"))
	} else if err == nil {
		return nil, errors.NewMsg("该企业下该用户名的用户已存在")
	}

	newUser := &model.UserInfo{
		CompanyID: companyID,
		Username:  userName,
		Nickname:  nickName,
		Password:  password,
		UserType:  int32(userType),
		Phone:     phone,
	}

	err = query.WithContext(ctx).UserInfo.Create(newUser)
	if err != nil {
		return nil, errors.Join(err, errors.NewMsg("创建用户失败"))
	}

	return newUser, nil
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

// CheckSessionAccess 检查用户对会话的只读查看权限（三级 RBAC）
// 超管(1)：放行全平台会话；企管(2)：放行本企业会话；普通员工(3)：仅放行本人创建的会话。
func CheckSessionAccess(tc jwt.TokenClaims, session *model.Session) error {
	if session == nil {
		return errors.NewMsg("会话不存在")
	}
	if tc.UserType == constants.SuperManager {
		return nil
	}
	if tc.UserType == constants.Manager && session.CompanyID == tc.CompanyID {
		return nil
	}
	if session.UserID == tc.UserID {
		return nil
	}
	return errors.NewMsg("会话不存在")
}

// CheckSessionWriteAccess 检查用户对会话的写入/发送消息权限
// 超管(1)：具备最高排查权限；创建者本人：具备写权限；
// 企管(2)：对本企业无头/自动化会话 (user_id = 0) 具备交互权限；其余情况拒绝。
func CheckSessionWriteAccess(tc jwt.TokenClaims, session *model.Session) error {
	if session == nil {
		return errors.NewMsg("会话不存在")
	}
	if tc.UserType == constants.SuperManager {
		return nil
	}
	if session.UserID == tc.UserID {
		return nil
	}
	if session.UserID == 0 && session.CompanyID == tc.CompanyID && tc.UserType == constants.Manager {
		return nil
	}
	return errors.NewMsg("会话不存在或无权向此会话发送消息")
}

// GetUserNamesByIDs 批量查询用户昵称或用户名（用于会话列表等场景展示创建人）
func GetUserNamesByIDs(ctx context.Context, userIDs []int64) (map[int64]string, error) {
	if len(userIDs) == 0 {
		return map[int64]string{}, nil
	}
	int32IDs := make([]int32, 0, len(userIDs))
	for _, id := range userIDs {
		int32IDs = append(int32IDs, int32(id))
	}
	query := db.Ctx(ctx)
	u := query.UserInfo
	users, err := query.WithContext(ctx).UserInfo.
		Where(u.UserID.In(int32IDs...)).
		Find()
	if err != nil {
		return nil, err
	}
	res := make(map[int64]string, len(users))
	for _, user := range users {
		name := user.Nickname
		if name == "" {
			name = user.Username
		}
		res[int64(user.UserID)] = name
	}
	return res, nil
}
