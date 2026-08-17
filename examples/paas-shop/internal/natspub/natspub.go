// Package natspub 提供 paas-shop 共享的 NATS 发布能力。
//
// 平台创建 dataservice shop-mq(nats) -> 绑定应用 -> NATS_URL 注入 env（nats://<token>@<host>:4222）。
// Connect 在 NATS_URL 空或连接失败时降级为 stub（Connected=false，Publish 静默丢弃），
// 保证未绑 MQ 的最小部署（单测/无 MQ 集群）不崩——向后兼容。
package natspub

import (
	"context"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"

	"github.com/nats-io/nats.go"
)

// Publisher 封装 NATS 连接；degraded=true 时为降级 stub。
type Publisher struct {
	nc       *nats.Conn
	degraded bool
}

// Connect 连 NATS；url 空或失败均降级（不返 error，调用方无感）。
func Connect(url string) *Publisher {
	if url == "" {
		slog.Warn("NATS_URL 未设置，product/recommend 降级运行（MQ 链路不可用）")
		return &Publisher{degraded: true}
	}
	nc, err := nats.Connect(url,
		nats.ReconnectWait(2*time.Second),
		nats.MaxReconnects(-1), // 永久重连
		nats.Timeout(5*time.Second),
	)
	if err != nil {
		slog.Warn("NATS 连接失败，降级运行", "url", maskURL(url), "err", err)
		return &Publisher{degraded: true}
	}
	slog.Info("NATS 已连接", "url", maskURL(url))
	return &Publisher{nc: nc}
}

// Publish 发布消息；降级 stub 静默丢弃返 nil。
// 带 OTel span（messaging 语义属性）使瀑布图可见 MQ 发布耗时与目标 subject。
func (p *Publisher) Publish(subject string, payload []byte) error {
	if p.degraded || p.nc == nil {
		return nil
	}
	_, span := otel.Tracer("paas-shop/nats").Start(context.Background(), "nats.publish "+subject)
	span.SetAttributes(
		attribute.String("messaging.system", "nats"),
		attribute.String("messaging.destination", subject),
		attribute.Int("messaging.message.payload_size_bytes", len(payload)),
	)
	defer span.End()
	return p.nc.Publish(subject, payload)
}

// Connected 报告是否真连 NATS（降级返 false）。
func (p *Publisher) Connected() bool {
	return !p.degraded && p.nc != nil && !p.nc.IsClosed()
}

// Close 优雅关闭（drain 等在途消息发完）；降级 stub no-op。
func (p *Publisher) Close() {
	if p.degraded || p.nc == nil {
		return
	}
	_ = p.nc.Drain() // Drain 阻塞等缓冲发完再关
}

// maskURL 隐藏 token 段，保留 host:port 便于日志排查。
func maskURL(url string) string {
	for i := 0; i < len(url)-1; i++ {
		if url[i] == ':' && url[i+1] == '/' {
			if at := indexByte(url, '@'); at > i {
				return url[:i+3] + "***" + url[at:]
			}
		}
	}
	return url
}

func indexByte(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}
