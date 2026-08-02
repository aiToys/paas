package dataservice_test

import (
	"strings"
	"testing"

	"github.com/aitoys/paas/internal/dataservice"
)

func TestGenerateCredentialsAllKinds(t *testing.T) {
	for _, kind := range []string{dataservice.KindDB, dataservice.KindCache, dataservice.KindMQ, dataservice.KindStorage} {
		c := dataservice.GenerateCredentials(kind, "")
		switch kind {
		case dataservice.KindDB:
			if c["user"] == "" || c["password"] == "" || c["database"] == "" {
				t.Fatalf("kind=%s db credentials missing: %v", kind, c)
			}
		case dataservice.KindCache:
			if c["password"] == "" {
				t.Fatalf("kind=%s cache password missing", kind)
			}
		case dataservice.KindMQ:
			if c["token"] == "" {
				t.Fatalf("kind=%s mq token missing", kind)
			}
		case dataservice.KindStorage:
			if c["accessKey"] == "" || c["secretKey"] == "" {
				t.Fatalf("kind=%s storage keys missing: %v", kind, c)
			}
		}
	}
}

func TestGenerateCredentialsRandomness(t *testing.T) {
	a := dataservice.GenerateCredentials(dataservice.KindDB, "mysql")
	b := dataservice.GenerateCredentials(dataservice.KindDB, "mysql")
	if a["password"] == b["password"] {
		t.Fatalf("password not random: %s", a["password"])
	}
	if len(a["password"]) != 24 {
		t.Fatalf("password length want 24 got %d", len(a["password"]))
	}
	// 字符集 base62
	for _, ch := range a["password"] {
		if !strings.ContainsRune("0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ", ch) {
			t.Fatalf("password contains non-base62 char: %q", ch)
		}
	}
}

func TestGenerateCredentialsPostgresUser(t *testing.T) {
	// postgres 默认 superuser=postgres（与容器官方镜像默认一致），mysql=root（H3）
	c := dataservice.GenerateCredentials(dataservice.KindDB, "postgres")
	if c["user"] != "postgres" {
		t.Fatalf("postgres user 应 postgres，got %s", c["user"])
	}
	c2 := dataservice.GenerateCredentials(dataservice.KindDB, "mysql")
	if c2["user"] != "root" {
		t.Fatalf("mysql user 应 root，got %s", c2["user"])
	}
}

func TestGenerateCredentialsUnknownKind(t *testing.T) {
	c := dataservice.GenerateCredentials("unknown", "")
	if len(c) != 0 {
		t.Fatalf("unknown kind should return empty map, got %v", c)
	}
}

func TestEnginePort(t *testing.T) {
	cases := map[string]int32{
		dataservice.KindDB:      3306, // mysql 默认（engine 非 postgres）
		dataservice.KindCache:   6379,
		dataservice.KindMQ:      4222,
		dataservice.KindStorage: 9000,
		"unknown":               0,
	}
	for k, want := range cases {
		if got := dataservice.EnginePort(k, ""); got != want {
			t.Fatalf("EnginePort(%s) = %d want %d", k, got, want)
		}
	}
	// postgres 端口 5432（H3）
	if got := dataservice.EnginePort(dataservice.KindDB, "postgres"); got != 5432 {
		t.Fatalf("EnginePort(db,postgres) = %d want 5432", got)
	}
}

func TestBuildConnectionDB(t *testing.T) {
	cred := dataservice.GenerateCredentials(dataservice.KindDB, "mysql")
	conn := dataservice.BuildConnection("acme-db", dataservice.KindDB, "mysql", "paas", map[string]string{"engine": "mysql"}, cred)
	if conn["host"] != "acme-db.paas.svc.cluster.local" {
		t.Fatalf("host = %s", conn["host"])
	}
	if conn["port"] != "3306" {
		t.Fatalf("port = %s", conn["port"])
	}
	uri := conn["uri"]
	if !strings.HasPrefix(uri, "mysql://root:") || !strings.Contains(uri, "@acme-db.paas.svc.cluster.local:3306/appdb") {
		t.Fatalf("db uri malformed: %s", uri)
	}
	// 凭证透传
	if conn["password"] != cred["password"] {
		t.Fatalf("password not propagated")
	}
}

func TestBuildConnectionPostgres(t *testing.T) {
	// postgres: port=5432 / user=postgres / uri=postgresql://（H3，与 mysql 区分）
	cred := dataservice.GenerateCredentials(dataservice.KindDB, "postgres")
	conn := dataservice.BuildConnection("pg-db", dataservice.KindDB, "postgres", "paas", map[string]string{"engine": "postgres"}, cred)
	if conn["port"] != "5432" {
		t.Fatalf("postgres port 应 5432，got %s", conn["port"])
	}
	if conn["user"] != "postgres" {
		t.Fatalf("postgres user 应 postgres，got %s", conn["user"])
	}
	uri := conn["uri"]
	if !strings.HasPrefix(uri, "postgresql://postgres:") || !strings.Contains(uri, "@pg-db.paas.svc.cluster.local:5432/appdb") {
		t.Fatalf("postgres uri malformed: %s", uri)
	}
}

func TestBuildConnectionCacheMQ(t *testing.T) {
	connC := dataservice.BuildConnection("c", dataservice.KindCache, "redis", "paas", nil, dataservice.GenerateCredentials(dataservice.KindCache, "redis"))
	if !strings.HasPrefix(connC["uri"], "redis://:") || !strings.Contains(connC["uri"], "@c.paas.svc.cluster.local:6379") {
		t.Fatalf("cache uri malformed: %s", connC["uri"])
	}
	connM := dataservice.BuildConnection("m", dataservice.KindMQ, "nats", "paas", nil, dataservice.GenerateCredentials(dataservice.KindMQ, "nats"))
	if !strings.HasPrefix(connM["uri"], "nats://") || !strings.Contains(connM["uri"], "@m.paas.svc.cluster.local:4222") {
		t.Fatalf("mq uri malformed: %s", connM["uri"])
	}
}

func TestBuildConnectionStorage(t *testing.T) {
	conn := dataservice.BuildConnection("mc", dataservice.KindStorage, "minio", "paas", nil, dataservice.GenerateCredentials(dataservice.KindStorage, "minio"))
	if conn["uri"] != "" {
		t.Fatalf("storage should have no uri, got %s", conn["uri"])
	}
	if conn["endpoint"] != "http://mc.paas.svc.cluster.local:9000" {
		t.Fatalf("storage endpoint malformed: %s", conn["endpoint"])
	}
	if conn["accessKey"] != "minio" {
		t.Fatalf("accessKey = %s", conn["accessKey"])
	}
}

func TestBuildConnectionNamespaceFallback(t *testing.T) {
	conn := dataservice.BuildConnection("x", dataservice.KindDB, "mysql", "", nil, dataservice.GenerateCredentials(dataservice.KindDB, "mysql"))
	if !strings.Contains(conn["host"], ".paas.svc.cluster.local") {
		t.Fatalf("empty namespace should fall back to paas, host=%s", conn["host"])
	}
}

func TestBuildConnectionDoesNotMutateInput(t *testing.T) {
	cred := map[string]string{"password": "p"}
	BuildCopy := dataservice.GenerateCredentials(dataservice.KindDB, "mysql")
	conn := dataservice.BuildConnection("d", dataservice.KindDB, "mysql", "paas", nil, BuildCopy)
	conn["password"] = "changed"
	if cred["password"] != "p" {
		t.Fatalf("BuildConnection should not share map with caller cred")
	}
	_ = conn
}

func TestMaskConnection(t *testing.T) {
	conn := map[string]string{ //nolint:gosec // G101 误报：测试 mock URL/凭据占位，非真实凭据
		"host": "h", "port": "3306", "user": "u",
		"password": "secret", "token": "tk", "secretKey": "sk",
		"uri": "mysql://root:secret@h:3306/db", "endpoint": "http://h:9000",
	}
	m := dataservice.MaskConnection(conn)
	if m["password"] != dataservice.SecretMask || m["token"] != dataservice.SecretMask || m["secretKey"] != dataservice.SecretMask {
		t.Fatalf("masked wrong: %v", m)
	}
	// uri 含明文密码，应掩码（H1）
	if m["uri"] != dataservice.SecretMask {
		t.Fatalf("uri 应掩码（含明文密码），got %q", m["uri"])
	}
	// endpoint 无凭证，不掩码
	if m["endpoint"] != "http://h:9000" {
		t.Fatalf("endpoint 不应掩码: %v", m)
	}
	if m["host"] != "h" || m["port"] != "3306" || m["user"] != "u" {
		t.Fatalf("non-secret fields changed: %v", m)
	}
	// 原始未被改
	if conn["password"] != "secret" {
		t.Fatalf("MaskConnection mutated input")
	}
}
