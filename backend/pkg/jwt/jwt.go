package jwt

import (
	"context"
	"backend/constants"
	"backend/pkg/errors"
	"github.com/golang-jwt/jwt/v5"
	"time"
)

var config *Config

type Config struct {
	Secret     string   // 密钥
	Seconds    int64    // 签发有效期（秒）
	IgnorePath []string // 白名单忽略路径
}

// InitJwt 初始化 JWT 配置
func InitJwt(conf Config) {
	config = &conf
}

type TokenClaims struct {
	UserId   int32              `json:"user_id"`
	UserType constants.UserType `json:"user_type"`
}

// GenAccessToken 生成 AccessToken
func GenAccessToken(tc TokenClaims) (string, error) {
	if config.Secret == "" {
		return "", errors.New("jwt secret must not be empty")
	}
	if config.Seconds <= 0 {
		return "", errors.New("jwt seconds must be a positive number")
	}

	iat := time.Now().Unix()

	claims := make(jwt.MapClaims)
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	claims["exp"] = iat + config.Seconds
	claims["user_id"] = tc.UserId
	claims["user_type"] = tc.UserType

	return token.SignedString([]byte(config.Secret))
}

// GetTokenClaimsFromCtx 从 Context 中提取强类型的登录凭证
func GetTokenClaimsFromCtx(ctx context.Context) (TokenClaims, error) {
	value := ctx.Value("tokenClaims")
	if value == nil {
		return TokenClaims{}, errors.NewMsg("用户未登录或token不存在")
	}

	tc, ok := value.(TokenClaims)
	if !ok {
		return TokenClaims{}, errors.New("tokenClaims 类型断言失败")
	}

	return tc, nil
}
