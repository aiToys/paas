package auth

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testSecret = "s3cret-key-for-test"

func TestSignParseRoundTrip(t *testing.T) {
	c := Claims{Sub: "u1", Tenant: "t-acme", Roles: []string{"tenant-admin"}, Typ: TokenAccess,
		Exp: time.Now().Add(AccessTTL).Unix()}
	tok, err := Sign(c, testSecret)
	require.NoError(t, err)
	// JWT 形如 header.payload.signature，含两个点
	require.Equal(t, 2, func() int { n := 0; for _, ch := range tok { if ch == '.' { n++ } }; return n }())

	got, err := Parse(tok, testSecret)
	require.NoError(t, err)
	assert.Equal(t, "u1", got.Sub)
	assert.Equal(t, "t-acme", got.Tenant)
	assert.Equal(t, []string{"tenant-admin"}, got.Roles)
	assert.Equal(t, TokenAccess, got.Typ)
}

func TestParseTamperedSignature(t *testing.T) {
	c := Claims{Sub: "u1", Typ: TokenAccess, Exp: time.Now().Add(time.Minute).Unix()}
	tok, err := Sign(c, testSecret)
	require.NoError(t, err)
	// 篡改最后一段签名
	tampered := tok[:len(tok)-3] + "XXX"
	_, err = Parse(tampered, testSecret)
	assert.ErrorIs(t, err, ErrTokenSignature)
}

func TestParseWrongSecret(t *testing.T) {
	c := Claims{Sub: "u1", Typ: TokenAccess, Exp: time.Now().Add(time.Minute).Unix()}
	tok, _ := Sign(c, testSecret)
	_, err := Parse(tok, "wrong-secret")
	assert.ErrorIs(t, err, ErrTokenSignature)
}

func TestParseExpired(t *testing.T) {
	c := Claims{Sub: "u1", Typ: TokenAccess, Exp: time.Now().Add(-time.Minute).Unix()}
	tok, _ := Sign(c, testSecret)
	_, err := Parse(tok, testSecret)
	assert.ErrorIs(t, err, ErrTokenExpired)
}

func TestParseTypeRejectsWrongType(t *testing.T) {
	c := Claims{Sub: "u1", Typ: TokenRefresh, Exp: time.Now().Add(time.Minute).Unix()}
	tok, _ := Sign(c, testSecret)
	_, err := ParseType(tok, testSecret, TokenAccess)
	assert.ErrorIs(t, err, ErrTokenType)
}

func TestParseMalformed(t *testing.T) {
	_, err := Parse("not-a-jwt", testSecret)
	assert.ErrorIs(t, err, ErrTokenMalformed)
}

func TestHashAndCheckPassword(t *testing.T) {
	h, err := HashPassword("123456")
	require.NoError(t, err)
	assert.True(t, CheckPassword(h, "123456"))
	assert.False(t, CheckPassword(h, "wrong"))
	// 不同哈希
	h2, _ := HashPassword("123456")
	assert.NotEqual(t, h, h2)
}
