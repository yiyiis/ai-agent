package jwt

import (
	"context"
	"time"

	"backend/constants"
	"backend/pkg/errors"
	"github.com/golang-jwt/jwt/v5"
)

var config *Config

type Config struct {
	Secret     string   `mapstructure:"Secret"`
	Seconds    int64    `mapstructure:"Seconds"`
	IgnorePath []string `mapstructure:"IgnorePath"`
}

func InitJwt(conf Config) {
	config = &conf
}

// AuthIdentity 从 token 解析出的用户身份
type AuthIdentity struct {
	UserID    int64              `json:"user_id"`
	CompanyID int64              `json:"company_id"`
	Name      string             `json:"name"`
	Username  string             `json:"username"`
	UserType  constants.UserType `json:"user_type"`
}

// TokenClaims JWT 自定义 Payload（兼容 Uid/UserId、CompanyId 等命名规范）
type TokenClaims struct {
	Uid       int64              `json:"Uid"`
	UserId    int64              `json:"user_id"`
	CompanyId int64              `json:"CompanyId"`
	CompanyID int64              `json:"company_id"`
	Name      string             `json:"Name"`
	Username  string             `json:"username"`
	UserType  constants.UserType `json:"user_type"`
	jwt.RegisteredClaims
}

// GenAccessToken 签发 JWT
func GenAccessToken(identity AuthIdentity) (string, error) {
	if config == nil || config.Secret == "" {
		return "", errors.New("jwt secret must not be empty")
	}
	seconds := config.Seconds
	if seconds <= 0 {
		seconds = 7 * 24 * 3600
	}

	now := time.Now()
	uid := identity.UserID
	cid := identity.CompanyID
	if cid == 0 {
		cid = 1
	}

	claims := TokenClaims{
		Uid:       uid,
		UserId:    uid,
		CompanyId: cid,
		CompanyID: cid,
		Name:      identity.Name,
		Username:  identity.Username,
		UserType:  identity.UserType,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Duration(seconds) * time.Second)),
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now.Add(-5 * time.Second)),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(config.Secret))
}

// GetIdentityFromCtx 从 Context 获取当前登录用户身份
func GetIdentityFromCtx(ctx context.Context) (AuthIdentity, error) {
	value := ctx.Value("authIdentity")
	if value == nil {
		return AuthIdentity{}, errors.NewMsg("用户未登录")
	}
	identity, ok := value.(AuthIdentity)
	if !ok || identity.UserID == 0 {
		return AuthIdentity{}, errors.NewMsg("无效的登录身份")
	}
	return identity, nil
}

// GetTokenClaimsFromCtx 兼容旧接口获取 TokenClaims
func GetTokenClaimsFromCtx(ctx context.Context) (*TokenClaims, error) {
	identity, err := GetIdentityFromCtx(ctx)
	if err != nil {
		return nil, err
	}
	return &TokenClaims{
		Uid:       identity.UserID,
		UserId:    identity.UserID,
		CompanyId: identity.CompanyID,
		CompanyID: identity.CompanyID,
		Name:      identity.Name,
		Username:  identity.Username,
		UserType:  identity.UserType,
	}, nil
}

