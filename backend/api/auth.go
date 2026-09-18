package api

import (
	"context"
	"strings"

	"backend/constants"
	"backend/dal/model"
	"backend/dao"
	"backend/pkg/apiwarp"
	"backend/pkg/db"
	"backend/pkg/errors"
	"backend/pkg/jwt"
	"github.com/gin-gonic/gin"
)

// Ping 健康检查：不走 Controller 信封，供负载均衡/探活直读
func Ping(c *gin.Context) {
	c.JSON(200, gin.H{
		"status":  "ok",
		"message": "pong",
	})
}

type LoginReq struct {
	Account     string `json:"account"`
	Username    string `json:"username"`
	Password    string `json:"password" binding:"required"`
	CompanyCode string `json:"company_code"` // 可选企业唯一代号 (如: default, corp_a)
}

type UserInfoResp struct {
	UserID    int64  `json:"user_id"`
	CompanyID int64  `json:"company_id"`
	Name      string `json:"name"`
	Phone     string `json:"phone,omitempty"`
	Username  string `json:"username,omitempty"`
}

type LoginResp struct {
	Token string       `json:"token"`
	User  UserInfoResp `json:"user"`
}

func tokenCookie(token string) []apiwarp.SetCookie {
	return []apiwarp.SetCookie{{Name: "agents_token", Value: token, MaxAge: 7 * 24 * 3600, Path: "/"}}
}

// UserLogin 处理用户登录 (POST /api/auth/login)
func UserLogin(ctx context.Context, req *LoginReq) (*apiwarp.CookieResp, error) {
	account := strings.TrimSpace(req.Account)
	if account == "" {
		account = strings.TrimSpace(req.Username)
	}
	if account == "" {
		return nil, errors.NewMsg("账号或用户名不能为空")
	}

	// 1. 若指定了企业代号，先解析企业 ID
	var targetCompanyID int64
	companyCode := strings.TrimSpace(req.CompanyCode)
	if companyCode != "" {
		comp, compErr := dao.GetCompanyByCode(ctx, companyCode)
		if compErr != nil || comp == nil {
			return nil, errors.NewMsg("指定的企业不存在")
		}
		targetCompanyID = comp.ID
	}

	// 2. 从 user_info 查询用户
	u := db.Ctx(ctx).UserInfo
	do := u.WithContext(ctx)
	if targetCompanyID > 0 {
		do = do.Where(u.CompanyID.Eq(targetCompanyID))
	}
	user, err := do.Where(do.Where(u.Username.Eq(account)).Or(u.Phone.Eq(account))).First()
	if err != nil || user.Password != req.Password {
		return nil, errors.NewMsg("账号或密码错误")
	}

	name := user.Nickname
	if name == "" {
		name = user.Username
	}

	claims := jwt.TokenClaims{
		UserID:    int64(user.UserID),
		CompanyID: user.CompanyID, // 真实绑定的企业租户 ID
		Name:      name,
		Username:  user.Username,
		UserType:  constants.UserType(user.UserType),
	}
	token, err := jwt.GenAccessToken(claims)
	if err != nil {
		return nil, errors.Join(err, errors.New("gen token"), errors.NewMsg("生成登录令牌失败"))
	}

	return &apiwarp.CookieResp{
		Cookies: tokenCookie(token),
		Body: LoginResp{
			Token: token,
			User: UserInfoResp{
				UserID:    int64(user.UserID),
				CompanyID: user.CompanyID,
				Name:      name,
				Phone:     user.Phone,
				Username:  user.Username,
			},
		},
	}, nil
}

type AuthMeReq struct{}

// AuthMe 获取当前登录用户信息 (GET /api/auth/me)
func AuthMe(ctx context.Context, _ *AuthMeReq) (*UserInfoResp, error) {
	identity, err := jwt.GetTokenClaimsFromCtx(ctx)
	if err != nil || identity.UserID == 0 {
		return nil, errors.NewMsg("登录态已失效")
	}
	return &UserInfoResp{
		UserID:    identity.UserID,
		CompanyID: identity.CompanyID,
		Name:      identity.Name,
		Username:  identity.Username,
	}, nil
}

type UserRegisterReq struct {
	Username    string `json:"username" binding:"required"`
	Password    string `json:"password" binding:"required"`
	Nickname    string `json:"nickname"`
	CompanyCode string `json:"company_code"` // 可选企业代号，默认为 default
}

// UserRegister 用户快速注册 (POST /api/auth/register)
func UserRegister(ctx context.Context, req *UserRegisterReq) (*apiwarp.CookieResp, error) {
	companyCode := strings.TrimSpace(req.CompanyCode)
	if companyCode == "" {
		companyCode = "default"
	}

	comp, err := dao.GetCompanyByCode(ctx, companyCode)
	if err != nil || comp == nil {
		// 若为 default 企业且尚未初始化，主动确保存在
		if companyCode == "default" {
			comp, _ = dao.EnsureDefaultCompany(ctx)
		}
		if comp == nil {
			return nil, errors.NewMsg("指定的归属企业不存在")
		}
	}
	targetCompanyID := comp.ID

	u := db.Ctx(ctx).UserInfo
	count, _ := u.WithContext(ctx).Where(u.Username.Eq(req.Username), u.CompanyID.Eq(targetCompanyID)).Count()
	if count > 0 {
		return nil, errors.NewMsg("该企业下该用户名已被注册")
	}

	nickname := req.Nickname
	if nickname == "" {
		nickname = req.Username
	}

	newUser := model.UserInfo{
		CompanyID: targetCompanyID,
		Username:  req.Username,
		Nickname:  nickname,
		Password:  req.Password,
		UserType:  int32(constants.User),
	}
	if err := db.Ctx(ctx).UserInfo.WithContext(ctx).Create(&newUser); err != nil {
		return nil, errors.Join(err, errors.New("create user"), errors.NewMsg("用户创建失败"))
	}

	claims := jwt.TokenClaims{
		UserID:    int64(newUser.UserID),
		CompanyID: targetCompanyID,
		Name:      nickname,
		Username:  newUser.Username,
		UserType:  constants.User,
	}
	token, err := jwt.GenAccessToken(claims)
	if err != nil {
		return nil, errors.Join(err, errors.New("gen token"), errors.NewMsg("生成登录令牌失败"))
	}

	return &apiwarp.CookieResp{
		Cookies: tokenCookie(token),
		Body: LoginResp{
			Token: token,
			User: UserInfoResp{
				UserID:    int64(newUser.UserID),
				CompanyID: targetCompanyID,
				Name:      nickname,
				Username:  newUser.Username,
			},
		},
	}, nil
}

type UserLogoutReq struct{}

// UserLogout 用户退出登录 (POST /api/auth/logout)
func UserLogout(ctx context.Context, _ *UserLogoutReq) (*apiwarp.CookieResp, error) {
	return &apiwarp.CookieResp{
		Cookies: []apiwarp.SetCookie{{Name: "agents_token", Value: "", MaxAge: -1, Path: "/"}},
		Body:    gin.H{"status": "ok"},
	}, nil
}
