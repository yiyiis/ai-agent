package api

import (
	"net/http"
	"strings"

	"backend/constants"
	"backend/dal/model"
	"backend/pkg/db"
	"backend/pkg/jwt"
	"github.com/gin-gonic/gin"
)

// Ping 健康检查接口
func Ping(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"status":  "ok",
		"message": "pong",
	})
}

type LoginReq struct {
	Account  string `json:"account"`
	Username string `json:"username"`
	Password string `json:"password" binding:"required"`
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

// UserLogin 处理用户登录 (POST /api/auth/login)
func UserLogin(c *gin.Context) {
	var req LoginReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误: " + err.Error()})
		return
	}

	account := strings.TrimSpace(req.Account)
	if account == "" {
		account = strings.TrimSpace(req.Username)
	}
	if account == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "账号或用户名不能为空"})
		return
	}

	// 从 user_info 查询用户（软删除由 gorm.DeletedAt 自动过滤）
	u := db.Ctx(c.Request.Context()).UserInfo
	user, err := u.WithContext(c.Request.Context()).
		Where(u.Username.Eq(account)).
		Or(u.Phone.Eq(account)).
		First()
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "账号或密码错误"})
		return
	}

	if user.Password != req.Password {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "账号或密码错误"})
		return
	}

	name := user.Nickname
	if name == "" {
		name = user.Username
	}

	claims := jwt.TokenClaims{
		UserID:    int64(user.UserID),
		CompanyID: 1, // 默认公司/租户 ID
		Name:      name,
		Username:  user.Username,
		UserType:  constants.UserType(user.UserType),
	}

	token, err := jwt.GenAccessToken(claims)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "生成登录令牌失败"})
		return
	}

	// 注入 Cookie
	c.SetCookie("agents_token", token, 7*24*3600, "/", "", false, false)

	c.JSON(http.StatusOK, LoginResp{
		Token: token,
		User: UserInfoResp{
			UserID:    int64(user.UserID),
			CompanyID: 1,
			Name:      name,
			Phone:     user.Phone,
			Username:  user.Username,
		},
	})
}

// AuthMe 获取当前登录用户信息 (GET /api/auth/me)
func AuthMe(c *gin.Context) {
	identity, err := jwt.GetTokenClaimsFromCtx(c.Request.Context())
	if err != nil || identity.UserID == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "未登录"})
		return
	}

	c.JSON(http.StatusOK, UserInfoResp{
		UserID:    identity.UserID,
		CompanyID: identity.CompanyID,
		Name:      identity.Name,
		Username:  identity.Username,
	})
}

// UserRegister 用户快速注册 (POST /api/auth/register)
func UserRegister(c *gin.Context) {
	var req struct {
		Username string `json:"username" binding:"required"`
		Password string `json:"password" binding:"required"`
		Nickname string `json:"nickname"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数无效: " + err.Error()})
		return
	}

	u := db.Ctx(c.Request.Context()).UserInfo
	count, _ := u.WithContext(c.Request.Context()).
		Where(u.Username.Eq(req.Username)).
		Count()
	if count > 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "该用户名已被注册"})
		return
	}

	nickname := req.Nickname
	if nickname == "" {
		nickname = req.Username
	}

	newUser := model.UserInfo{
		Username: req.Username,
		Nickname: nickname,
		Password: req.Password,
		UserType: int32(constants.User),
	}
	if err := db.Ctx(c.Request.Context()).UserInfo.WithContext(c.Request.Context()).Create(&newUser); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "用户创建失败"})
		return
	}

	claims := jwt.TokenClaims{
		UserID:    int64(newUser.UserID),
		CompanyID: 1,
		Name:      nickname,
		Username:  newUser.Username,
		UserType:  constants.User,
	}
	token, _ := jwt.GenAccessToken(claims)
	c.SetCookie("agents_token", token, 7*24*3600, "/", "", false, false)

	c.JSON(http.StatusOK, LoginResp{
		Token: token,
		User: UserInfoResp{
			UserID:    int64(newUser.UserID),
			CompanyID: 1,
			Name:      nickname,
			Username:  newUser.Username,
		},
	})
}

// UserLogout 用户退出登录 (POST /api/auth/logout)
func UserLogout(c *gin.Context) {
	c.SetCookie("agents_token", "", -1, "/", "", false, false)
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}
