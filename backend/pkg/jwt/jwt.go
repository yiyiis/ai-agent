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

// InitJwt 初始化 JWT 配置
func InitJwt(conf Config) {
	config = &conf
}

// TokenClaims JWT 载荷：业务身份字段 + 标准注册声明
type TokenClaims struct {
	UserID    int64              `json:"user_id"`
	CompanyID int64              `json:"company_id"`
	Name      string             `json:"name"`
	Username  string             `json:"username"`
	UserType  constants.UserType `json:"user_type"`
	jwt.RegisteredClaims
}

// GenAccessToken 签发 JWT
func GenAccessToken(tc TokenClaims) (string, error) {
	if config == nil || config.Secret == "" {
		return "", errors.New("jwt secret must not be empty")
	}
	seconds := config.Seconds
	if seconds <= 0 {
		seconds = 7 * 24 * 3600
	}

	now := time.Now()
	tc.ExpiresAt = jwt.NewNumericDate(now.Add(time.Duration(seconds) * time.Second))
	tc.IssuedAt = jwt.NewNumericDate(now)
	tc.NotBefore = jwt.NewNumericDate(now.Add(-5 * time.Second))

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, tc)
	return token.SignedString([]byte(config.Secret))
}

// GetTokenClaimsFromCtx 从 Context 中提取登录身份
func GetTokenClaimsFromCtx(ctx context.Context) (TokenClaims, error) {
	if ctx == nil {
		return TokenClaims{}, errors.NewMsg("用户未登录")
	}

	value := ctx.Value("tokenClaims")
	if value == nil {
		return TokenClaims{}, errors.NewMsg("用户未登录或登录态已失效")
	}

	tc, ok := value.(TokenClaims)
	if !ok || tc.UserID == 0 {
		return TokenClaims{}, errors.NewMsg("无效的登录身份")
	}

	return tc, nil
}
