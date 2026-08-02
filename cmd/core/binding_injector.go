package main

import (
	"context"
	"fmt"
	"log"

	"github.com/aitoys/paas/internal/appconfig"
	"github.com/aitoys/paas/internal/core/application"
	"github.com/aitoys/paas/internal/dataservice"
)

// dsBindingInjector 把数据服务连接信息注入应用配置（应用×环境级 appconfig）。
// 依赖倒置：application 包只知 application.BindingInjector 接口，本实现桥接 dataservice+appconfig+application，
// 破除 application -> dataservice/appconfig 的直接依赖。
type dsBindingInjector struct {
	dsRepo  dataservice.Repository
	cfgRepo appconfig.Repository
	appRepo application.Repository // 查应用剩余绑定，解绑时重新注入同 Kind 连接（避免误删仍需的 key）
}

// isDataserviceKind 判断绑定类型是否为数据服务 kind（db/cache/mq/storage/vector/search）。
// 绑定 type 用具体 kind（与前端 TypeKey、application.Binding.Type 对齐），非 "dataservice" 大类。
func isDataserviceKind(t string) bool {
	switch t {
	case dataservice.KindDB, dataservice.KindCache, dataservice.KindMQ,
		dataservice.KindStorage, dataservice.KindVector, dataservice.KindSearch:
		return true
	}
	return false
}

// injectKeys 按 Kind 返回要注入的 appconfig (key, value) 对（value 取自 ds.Connection）。
// 固定 key 名（DATABASE_URL/REDIS_URL/NATS_URL/MINIO_*），应用代码引用方便；
// 同应用×环境同 Kind 多绑定覆盖取最后（起步约定，YAGNI）；解绑时重新注入剩余最后一个。
func injectKeys(ds dataservice.DataService) []struct{ Key, Value string } {
	switch ds.Kind {
	case dataservice.KindDB:
		return []struct{ Key, Value string }{{"DATABASE_URL", ds.Connection["uri"]}}
	case dataservice.KindCache:
		return []struct{ Key, Value string }{{"REDIS_URL", ds.Connection["uri"]}}
	case dataservice.KindMQ:
		return []struct{ Key, Value string }{{"NATS_URL", ds.Connection["uri"]}}
	case dataservice.KindStorage:
		return []struct{ Key, Value string }{
			{"MINIO_ENDPOINT", ds.Connection["endpoint"]},
			{"MINIO_ACCESS_KEY", ds.Connection["accessKey"]},
			{"MINIO_SECRET_KEY", ds.Connection["secretKey"]},
		}
	}
	return nil
}

// resolveDS 按 nameOrID 解析数据服务：先按 ID 直查（常见路径），未命中则按 name 遍历。
// 前端绑定浮层允许用户填名称（人类可读）或 ID，两者皆可解析（M2 容错）。
func (b dsBindingInjector) resolveDS(ctx context.Context, nameOrID string) (dataservice.DataService, error) {
	if ds, err := b.dsRepo.Get(ctx, nameOrID); err == nil {
		return ds, nil
	}
	list, err := b.dsRepo.List(ctx, "")
	if err != nil {
		return dataservice.DataService{}, err
	}
	for _, ds := range list {
		if ds.Name == nameOrID {
			return ds, nil
		}
	}
	return dataservice.DataService{}, fmt.Errorf("数据服务不存在: %s", nameOrID)
}

// upsert 向 appconfig 注入一组连接条目（TypeSecret，含密码）。空值跳过。
func (b dsBindingInjector) upsert(ctx context.Context, appID, envID string, kvs []struct{ Key, Value string }) {
	for _, kv := range kvs {
		if kv.Value == "" {
			continue
		}
		if _, err := b.cfgRepo.Upsert(ctx, appconfig.ConfigItem{
			AppID: appID, EnvID: envID, Key: kv.Key, Value: kv.Value, Type: appconfig.TypeSecret,
		}); err != nil {
			log.Printf("dsBindingInjector: 写 appconfig key=%s 失败: %v", kv.Key, err)
		}
	}
}

// OnBind 绑定数据服务时：查 ds -> 按 Kind 写连接条目到 appconfig（TypeSecret，含密码）。
func (b dsBindingInjector) OnBind(ctx context.Context, appID, btype, name string) error {
	if !isDataserviceKind(btype) || b.dsRepo == nil || b.cfgRepo == nil {
		return nil
	}
	ds, err := b.resolveDS(ctx, name)
	if err != nil {
		return err
	}
	b.upsert(ctx, appID, ds.EnvID, injectKeys(ds))
	return nil
}

// OnUnbind 解绑时：若无同 Kind 剩余绑定 -> 删已注入 key；有同 Kind 剩余 -> 重新注入最后一个（覆盖被解绑值，key 保留）。
// 数据服务已删则无 key 可清，记日志跳过（best-effort）。
func (b dsBindingInjector) OnUnbind(ctx context.Context, appID, btype, name string) error {
	if !isDataserviceKind(btype) || b.dsRepo == nil || b.cfgRepo == nil {
		return nil
	}
	unbound, err := b.resolveDS(ctx, name)
	if err != nil {
		log.Printf("dsBindingInjector OnUnbind: 数据服务 %s 已不存在，跳过清理（best-effort）: %v", name, err)
		return nil
	}
	// 查应用剩余同 Kind 绑定：有则重新注入最后一个（key 保留，值覆盖为剩余 ds 的连接）。
	if b.appRepo != nil {
		app, err := b.appRepo.Get(ctx, appID)
		if err == nil {
			var remaining []dataservice.DataService
			for _, bd := range app.Bindings {
				if bd.Type != btype || bd.Name == name {
					continue
				}
				if ds, err := b.resolveDS(ctx, bd.Name); err == nil && ds.Kind == unbound.Kind {
					remaining = append(remaining, ds)
				}
			}
			if len(remaining) > 0 {
				last := remaining[len(remaining)-1]
				b.upsert(ctx, appID, last.EnvID, injectKeys(last))
				return nil
			}
		}
	}
	// 无同 Kind 剩余：删被解绑 ds 的注入 key。appconfig.Delete 按 id，故先 List 找 key->id。
	items, err := b.cfgRepo.List(ctx, appID, unbound.EnvID)
	if err != nil {
		return err
	}
	want := map[string]bool{}
	for _, kv := range injectKeys(unbound) {
		want[kv.Key] = true
	}
	for _, it := range items {
		if want[it.Key] {
			if err := b.cfgRepo.Delete(ctx, it.ID); err != nil {
				log.Printf("dsBindingInjector OnUnbind: 删 appconfig id=%s 失败: %v", it.ID, err)
			}
		}
	}
	return nil
}
