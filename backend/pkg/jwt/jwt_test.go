package jwt

import (
	"context"
	"testing"

	"backend/constants"
)

func TestJWTGenerationAndParsing(t *testing.T) {
	InitJwt(Config{
		Secret:  "test_jwt_secret_key_12345",
		Seconds: 3600,
	})

	identity := AuthIdentity{
		UserID:    1001,
		CompanyID: 1,
		Name:      "test_user",
		Username:  "test_user",
		UserType:  constants.User,
	}

	token, err := GenAccessToken(identity)
	if err != nil {
		t.Fatalf("GenAccessToken failed: %v", err)
	}
	if token == "" {
		t.Fatal("generated token is empty")
	}

	// Test Context injection and extraction
	ctx := context.WithValue(context.Background(), "authIdentity", identity)
	extracted, err := GetIdentityFromCtx(ctx)
	if err != nil {
		t.Fatalf("GetIdentityFromCtx failed: %v", err)
	}
	if extracted.UserID != 1001 || extracted.Name != "test_user" {
		t.Fatalf("extracted identity mismatch: %+v", extracted)
	}

	claims, err := GetTokenClaimsFromCtx(ctx)
	if err != nil {
		t.Fatalf("GetTokenClaimsFromCtx failed: %v", err)
	}
	if claims.UserId != 1001 || claims.CompanyId != 1 {
		t.Fatalf("extracted claims mismatch: %+v", claims)
	}
}
