package dataservice

import (
	"crypto/rand"
	"strconv"
)

// SecretMask 是连接信息中敏感字段（password/secretKey/token/uri）的固定掩码。
// list/detail 返回用，不泄漏长度/内容（与 appconfig.SecretMask 同语义，本包独立常量避免跨包耦合）。
const SecretMask = "••••••" //nolint:gosec // G101 误报：固定掩码占位符，非凭据

// DefaultNamespace 是未注入 NamespaceResolver 时的兜底 K8s namespace（BuildConnection/FillConnection 用）。
const DefaultNamespace = "paas"

// maskKeys 是连接信息中需要掩码的 key。
// password/secretKey/token 是凭证；uri 含 user:password@ 整串掩码（list/detail 不泄漏连接串明文）。
// endpoint（http://host:port，无凭证）/host/port/user/database/accessKey 不掩码。
var maskKeys = map[string]bool{
	"password":  true,
	"secretKey": true,
	"token":     true,
	"uri":       true,
}

const randLen = 24
const randChars = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"

// randString 用 crypto/rand 生成 base62 随机串（拒绝越界字节避免模偏置）。
// 失败 panic（熵耗尽极罕见，fail-fast 优于持久化空密码 -> Secret 写入空凭证）。
func randString(n int) string {
	out := make([]byte, n)
	// max 使 len(randChars) 整除 [0,max)，丢弃 >= max 的字节避免取模偏置。
	max := byte(256 - 256%len(randChars))
	for i := 0; i < n; {
		var b [1]byte
		if _, err := rand.Read(b[:]); err != nil {
			panic("dataservice: crypto/rand 读取失败: " + err.Error())
		}
		if b[0] >= max {
			continue
		}
		out[i] = randChars[int(b[0])%len(randChars)]
		i++
	}
	return string(out)
}

// GenerateCredentials 按 Kind+Engine 生成强随机凭证（user/password/database 或 accessKey/secretKey 或 token）。
// postgres 默认 superuser=postgres（容器官方镜像默认），mysql=root；纯函数 + crypto/rand。
// 每次调用生成新随机，调用方负责持久化（Create 时一次性，重启不变）。
func GenerateCredentials(kind, engine string) map[string]string {
	switch kind {
	case KindDB:
		user := "root"
		if engine == "postgres" {
			user = "postgres"
		}
		return map[string]string{
			"user":     user,
			"password": randString(randLen),
			"database": "appdb",
		}
	case KindCache:
		return map[string]string{"password": randString(randLen)}
	case KindMQ:
		return map[string]string{"token": randString(randLen)}
	case KindStorage:
		return map[string]string{
			"accessKey": "minio",
			"secretKey": randString(randLen),
		}
	}
	return map[string]string{}
}

// EnginePort 返回 Kind+Engine 对应默认端口（未知返 0）。
// mysql=3306 / postgres=5432 / redis=6379 / nats=4222 / minio=9000（S3 API；console 9001 仅 Pod 内不暴露）。
func EnginePort(kind, engine string) int32 {
	switch kind {
	case KindDB:
		if engine == "postgres" {
			return 5432
		}
		return 3306 // mysql
	case KindCache:
		return 6379
	case KindMQ:
		return 4222 // nats
	case KindStorage:
		return 9000
	}
	return 0
}

// BuildConnection 算完整连接信息：host(FQDN)/port/credentials 合并/uri。
// 纯函数，dev 纯内存/PG/K8s 三模式一致；namespace 空兜底 "paas"。
// 按 engine 区分 uri scheme（mysql:// vs postgresql://）与 user 默认值；
// credentials 入参深拷（不共享 map）；host/port/uri 由 name+ns+engine+credentials 派生。
func BuildConnection(name, kind, engine, namespace string, spec, credentials map[string]string) map[string]string {
	if namespace == "" {
		namespace = DefaultNamespace
	}
	host := name + "." + namespace + ".svc.cluster.local"
	port := EnginePort(kind, engine)
	portStr := strconv.Itoa(int(port))
	conn := map[string]string{
		"host": host,
		"port": portStr,
	}
	for k, v := range credentials { // 深拷凭证，不共享入参 map
		conn[k] = v
	}
	switch kind {
	case KindDB:
		user := conn["user"]
		if user == "" {
			user = "root"
			if engine == "postgres" {
				user = "postgres"
			}
		}
		db := conn["database"]
		if db == "" {
			db = "appdb"
		}
		scheme := "mysql"
		if engine == "postgres" {
			scheme = "postgresql"
		}
		conn["uri"] = scheme + "://" + user + ":" + conn["password"] + "@" + host + ":" + portStr + "/" + db
	case KindCache:
		conn["uri"] = "redis://:" + conn["password"] + "@" + host + ":" + portStr
	case KindMQ:
		conn["uri"] = "nats://" + conn["token"] + "@" + host + ":" + portStr
	case KindStorage:
		conn["endpoint"] = "http://" + host + ":" + portStr
	}
	return conn
}

// MaskConnection 返回掩码副本（password/secretKey/token/uri -> SecretMask）。list/detail 返回用。
// 不修改入参 map（深拷）。
func MaskConnection(conn map[string]string) map[string]string {
	out := make(map[string]string, len(conn))
	for k, v := range conn {
		if maskKeys[k] {
			out[k] = SecretMask
		} else {
			out[k] = v
		}
	}
	return out
}
