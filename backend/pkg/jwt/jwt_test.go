package jwt

import (
	"context"
	"testing"

	"backend/constants"
	"github.com/golang-jwt/jwt/v5"
)

func TestJWTGenerationAndParsing(t *testing.T) {
	InitJwt(Config{
		Secret:  "test_jwt_secret_key_12345",
		Seconds: 3600,
	})

	claims := TokenClaims{
		UserID:    1001,
		CompanyID: 1,
		Name:      "test_user",
		Username:  "test_user",
		UserType:  constants.User,
	}

	token, err := GenAccessToken(claims)
	if err != nil {
		t.Fatalf("GenAccessToken failed: %v", err)
	}
	if token == "" {
		t.Fatal("generated token is empty")
	}

	// 验证签出的 token 能按相同结构解析回来
	parsed, err := jwt.ParseWithClaims(token, &TokenClaims{}, func(t *jwt.Token) (interface{}, error) {
		return []byte("test_jwt_secret_key_12345"), nil
	}, jwt.WithExpirationRequired())
	if err != nil || !parsed.Valid {
		t.Fatalf("parse generated token failed: %v", err)
	}
	got := parsed.Claims.(*TokenClaims)
	if got.UserID != 1001 || got.CompanyID != 1 || got.Name != "test_user" {
		t.Fatalf("parsed claims mismatch: %+v", got)
	}

	// 验证 Context 注入与提取
	ctx := context.WithValue(context.Background(), "tokenClaims", claims)
	extracted, err := GetTokenClaimsFromCtx(ctx)
	if err != nil {
		t.Fatalf("GetTokenClaimsFromCtx failed: %v", err)
	}
	if extracted.UserID != 1001 || extracted.Name != "test_user" {
		t.Fatalf("extracted claims mismatch: %+v", extracted)
	}
}
