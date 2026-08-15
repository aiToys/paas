package auth

import "golang.org/x/crypto/bcrypt"

// HashPassword 用 bcrypt（cost=10）哈希明文密码。
func HashPassword(plain string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(plain), 10)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// CheckPassword 校验明文与哈希是否匹配。
func CheckPassword(hash, plain string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain)) == nil
}

// dummyHash 是启动时生成的一次性 bcrypt 哈希，专用于登录时序侧信道防护：
// 用户不存在时也执行一次 dummy 比对，使响应时间与「密码错」路径一致（cost=10 ~50-100ms），
// 防攻击者通过响应延迟差异枚举用户名是否存在。
var dummyHash = func() string {
	b, _ := bcrypt.GenerateFromPassword([]byte("paas-timing-attack-dummy"), 10)
	return string(b)
}()
