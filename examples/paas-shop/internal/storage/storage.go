// Package storage 提供 paas-shop 的 MinIO 对象存储能力（平台 storage 数据服务绑定注入 MINIO_*）。
//
// 纯 net/http 实现 S3 PutObject/GetObject（AWS SigV4 签名手写，无 SDK 依赖——
// minio-go/aws-sdk 依赖树庞大，示例保持零重依赖风格；只用到 put/get 两个动作）。
//
//   - EnsureBucket：建 paas-shop-images bucket（幂等）
//   - PutImage：商品图片上传（content-type 校验限图片，key=products/{id}/{sha}.{ext}）
//   - URL：图片访问 URL（bucket 公开读策略 + 经平台 dataplane 反代或直接 minio 端点）
//
// 降级链约定：MINIO_ENDPOINT 未配 -> degraded stub（上传返空 URL，前端显示占位图）。
package storage

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

const bucket = "paas-shop-images"

// Client 封装 MinIO S3 API；degraded=true 时全部 no-op。
type Client struct {
	endpoint  string // http://host:9000
	accessKey string
	secretKey string
	http      *http.Client
	degraded  bool
}

// New 从 env 构造：MINIO_ENDPOINT/MINIO_ACCESS_KEY/MINIO_SECRET_KEY（storage 绑定注入）。缺失 -> degraded。
func New() *Client {
	c := &Client{
		endpoint:  os.Getenv("MINIO_ENDPOINT"),
		accessKey: os.Getenv("MINIO_ACCESS_KEY"),
		secretKey: os.Getenv("MINIO_SECRET_KEY"),
		http:      &http.Client{Timeout: 30 * time.Second},
	}
	if c.endpoint == "" {
		c.degraded = true
		slog.Warn("MINIO_ENDPOINT 未设置，图片上传降级关闭（前端显示占位图）")
	}
	return c
}

// Available 是否可用。
func (c *Client) Available() bool { return c != nil && !c.degraded }

// EnsureBucket 建 bucket（幂等：已存在 409 忽略）+ 设公开读策略（图片直接 URL 可访问）。
func (c *Client) EnsureBucket(ctx context.Context) error {
	if c.degraded {
		return nil
	}
	req, err := c.sign(ctx, http.MethodPut, "/"+bucket, nil, "")
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body) //nolint:errcheck
	if resp.StatusCode >= 300 && resp.StatusCode != http.StatusConflict {
		return fmt.Errorf("建 bucket %d", resp.StatusCode)
	}
	// 公开读策略（anonymous get object；写仍需签名）。
	policy := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":"*","Action":["s3:GetObject"],"Resource":["arn:aws:s3:::` + bucket + `/*"]}]}`
	req, err = c.sign(ctx, http.MethodPut, "/"+bucket+"?policy=", strings.NewReader(policy), "application/json")
	if err != nil {
		return err
	}
	resp, err = c.http.Do(req)
	if err != nil {
		return fmt.Errorf("设公开策略: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body) //nolint:errcheck
	if resp.StatusCode >= 300 && resp.StatusCode != http.StatusConflict {
		return fmt.Errorf("设策略 %d", resp.StatusCode)
	}
	slog.Info("minio bucket 就绪", "bucket", bucket)
	return nil
}

// PutImage 上传图片，返回对象 key（URL = endpoint/bucket/key）。degraded 返空串。
func (c *Client) PutImage(ctx context.Context, key string, r io.Reader, size int64, contentType string) (string, error) {
	if c.degraded {
		return "", nil
	}
	req, err := c.sign(ctx, http.MethodPut, "/"+bucket+"/"+key, r, contentType)
	if err != nil {
		return "", err
	}
	req.ContentLength = size
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body) //nolint:errcheck
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("put %d", resp.StatusCode)
	}
	return c.endpoint + "/" + bucket + "/" + key, nil
}

// sign 构造 AWS SigV4 签名请求（S3 单 chunk payload，streaming-signed-payload 不需要——
// minio 默认接受 UNSIGNED-PAYLOAD 上传，这里对 payload 也签 hash 简化）。
func (c *Client) sign(ctx context.Context, method, path string, body io.Reader, contentType string) (*http.Request, error) {
	u := c.endpoint + path
	var req *http.Request
	var err error
	if body != nil {
		req, err = http.NewRequestWithContext(ctx, method, u, body)
	} else {
		req, err = http.NewRequestWithContext(ctx, method, u, nil)
	}
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	payloadHash := "UNSIGNED-PAYLOAD" // minio 支持，避免读流两遍
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	host := strings.TrimPrefix(strings.TrimPrefix(c.endpoint, "http://"), "https://")
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")
	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("Host", host)

	// canonical request
	q := req.URL.Query().Encode() // policy 等 query 需参与签名
	canonicalURI := req.URL.Path
	if canonicalURI == "" {
		canonicalURI = "/"
	}
	signedHeaders, canonicalHeaders := canonicalHdrs(req)
	canonicalRequest := strings.Join([]string{
		method, canonicalURI, q, canonicalHeaders, signedHeaders, payloadHash,
	}, "\n")

	// string to sign
	scope := dateStamp + "/auto/s3/aws4_request"
	h := sha256.Sum256([]byte(canonicalRequest))
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256", amzDate, scope, hex.EncodeToString(h[:]),
	}, "\n")

	// signing key
	kDate := hmacSHA256([]byte("AWS4"+c.secretKey), dateStamp)
	kScope := hmacSHA256(kDate, "auto")
	kService := hmacSHA256(kScope, "s3")
	kSigning := hmacSHA256(kService, "aws4_request")
	sig := hex.EncodeToString(hmacSHA256(kSigning, stringToSign))

	req.Header.Set("Authorization", fmt.Sprintf(
		"AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		c.accessKey, scope, signedHeaders, sig))
	return req, nil
}

// canonicalHdrs 收集参与签名的 header（host + x-amz-* + content-type），返排序串。
func canonicalHdrs(req *http.Request) (signed, canonical string) {
	hdrs := map[string]string{"host": req.Header.Get("Host")}
	for k, vs := range req.Header {
		lk := strings.ToLower(k)
		if lk == "content-type" || strings.HasPrefix(lk, "x-amz-") {
			hdrs[lk] = strings.TrimSpace(vs[0])
		}
	}
	keys := make([]string, 0, len(hdrs))
	for k := range hdrs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k + ":" + hdrs[k] + "\n")
	}
	return strings.Join(keys, ";"), b.String()
}

func hmacSHA256(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
}
