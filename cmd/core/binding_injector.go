package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/aitoys/paas/internal/appconfig"
	"github.com/aitoys/paas/internal/core/application"
	"github.com/aitoys/paas/internal/core/identity"
	"github.com/aitoys/paas/internal/dataservice"
)

// dsBindingInjector 把数据服务连接信息 + 模型推理凭证注入应用配置（应用×环境级 appconfig）。
// 依赖倒置：application 包只知 application.BindingInjector 接口，本实现桥接 dataservice+appconfig+identity+application，
// 破除 application -> dataservice/appconfig/identity 的直接依赖。
type dsBindingInjector struct {
	dsRepo  dataservice.Repository
	cfgRepo appconfig.Repository
	appRepo application.Repository // 查应用剩余绑定，解绑时重新注入同 Kind 连接（避免误删仍需的 key）
	idb     identity.Repository    // 模型绑定时创建应用级 API Key（用量归因到应用）
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

// 模型绑定注入的固定 appconfig key（与数据服务连接 key 同维，TypeSecret）。
const (
	cfgKeyLLMAPIKey = "PAAS_LLM_API_KEY"  // 应用级 API Key 明文（调平台 /v1 用）
	cfgKeyLLMBase   = "PAAS_LLM_BASE_URL" // 平台推理 gateway URL
)

// llmBaseURL 解析平台推理入口：env PAAS_API_BASE 优先（去尾 /v1 后补），否则集群内 core service 默认。
func llmBaseURL() string {
	if v := os.Getenv("PAAS_API_BASE"); v != "" {
		// 兼容用户填 http://x/v1 或 http://x：统一规整为以 /v1 结尾。
		if len(v) >= 3 && v[len(v)-3:] == "/v1" {
			return v
		}
		return v + "/v1"
	}
	return "http://paas-core.paas.svc.cluster.local:8080/v1"
}

// randHex 生成 n 字节随机十六进制串（API Key / ID 生成，crypto/rand 防可预测）。
func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// rand 失败极少见（仅内核熵池异常），fallback 时间戳不安全但避免 panic。
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
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
// 显式生成 id（pg store 用 caller 传入的 id 作主键，空 id 多次注入会主键冲突）。
func (b dsBindingInjector) upsert(ctx context.Context, appID, envID string, kvs []struct{ Key, Value string }) {
	for _, kv := range kvs {
		if kv.Value == "" {
			continue
		}
		if _, err := b.cfgRepo.Upsert(ctx, appconfig.ConfigItem{
			ID: "cfg-" + randHex(10), AppID: appID, EnvID: envID,
			Key: kv.Key, Value: kv.Value, Type: appconfig.TypeSecret,
		}); err != nil {
			log.Printf("dsBindingInjector: 写 appconfig key=%s 失败: %v", kv.Key, err)
		}
	}
}

// OnBind 绑定资源时落地配置注入：
//   - 数据服务（db/cache/mq/storage/vector/search）：按 Kind 写连接条目到 appconfig（TypeSecret）。
//   - 模型推理（models）：生成应用级 API Key（用量归因到应用）+ 注入 PAAS_LLM_API_KEY/BASE_URL。
//
// 模型是平台级资源（不分环境），LLM 凭证注入到 appconfig 的 "default" 桶（跨环境共享）。
func (b dsBindingInjector) OnBind(ctx context.Context, appID, btype, name string) error {
	if btype == "models" {
		return b.bindModel(ctx, appID)
	}
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

// bindModel 生成应用级 API Key 并注入 LLM 凭证到 appconfig（"default" 桶，跨环境共享）。
// 应用级 Key 只含推理权限（model:infer/read），AppID 绑定应用，gateway 据此把 token 用量归因到应用。
// 失败仅 log 不阻断绑定（绑定是主操作，凭证注入是派生副作用，与数据服务注入同款 best-effort）。
func (b dsBindingInjector) bindModel(ctx context.Context, appID string) error {
	if b.idb == nil || b.cfgRepo == nil || b.appRepo == nil {
		return nil
	}
	app, err := b.appRepo.Get(ctx, appID)
	if err != nil {
		log.Printf("dsBindingInjector bindModel: 查应用 %s 失败: %v", appID, err)
		return nil
	}
	// 已有应用级 Key 则复用（重复绑定同一模型不重复发 Key）。
	keys, _ := b.idb.ListAPIKeys(ctx, app.TenantID)
	for _, k := range keys {
		if k.AppID == appID {
			// Key 已存在，仅确保 appconfig 凭证就位（可能被用户误删过）。
			b.upsert(ctx, appID, appconfig.DefaultEnv, []struct{ Key, Value string }{
				{cfgKeyLLMAPIKey, k.Key},
				{cfgKeyLLMBase, llmBaseURL()},
			})
			return nil
		}
	}
	key := "sk-" + randHex(20)
	k := identity.APIKey{
		ID:       "k-app-" + randHex(8),
		TenantID: app.TenantID,
		UserID:   "app:" + appID, // 占位 userID（应用专用 Key，不绑具体人）
		AppID:    appID,
		Roles:    []string{"app-llm"}, // 最小角色：仅 model:infer/read（hasPermission 经 BuiltinRoles 展开）
		Key:      key,
	}
	if err := b.idb.CreateAPIKey(ctx, k); err != nil {
		log.Printf("dsBindingInjector bindModel: 创建应用级 Key 失败: %v", err)
		return nil
	}
	b.upsert(ctx, appID, appconfig.DefaultEnv, []struct{ Key, Value string }{
		{cfgKeyLLMAPIKey, key},
		{cfgKeyLLMBase, llmBaseURL()},
	})
	return nil
}

// OnUnbind 解绑时清理：
//   - 数据服务：若无同 Kind 剩余绑定 -> 删已注入 key；有 -> 重新注入最后一个（key 保留）。
//   - 模型推理：删应用级 Key + 删 appconfig 的 PAAS_LLM_*（"default" 桶）。
//
// 数据服务已删则无 key 可清，记日志跳过（best-effort）。
func (b dsBindingInjector) OnUnbind(ctx context.Context, appID, btype, name string) error {
	if btype == "models" {
		return b.unbindModel(ctx, appID)
	}
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

// unbindModel 解绑模型：删应用级 Key + 删 appconfig 的 PAAS_LLM_*（"default" 桶）。
// Key 删除按 ID，先 ListAPIKeys 过滤 AppID 找到。best-effort：失败仅 log。
func (b dsBindingInjector) unbindModel(ctx context.Context, appID string) error {
	if b.idb == nil || b.cfgRepo == nil || b.appRepo == nil {
		return nil
	}
	app, err := b.appRepo.Get(ctx, appID)
	if err != nil {
		return nil
	}
	keys, _ := b.idb.ListAPIKeys(ctx, app.TenantID)
	for _, k := range keys {
		if k.AppID == appID {
			if err := b.idb.DeleteAPIKey(ctx, k.ID); err != nil {
				log.Printf("dsBindingInjector unbindModel: 删应用级 Key %s 失败: %v", k.ID, err)
			}
		}
	}
	// 删 appconfig 的 LLM 凭证（"default" 桶）。
	items, err := b.cfgRepo.List(ctx, appID, appconfig.DefaultEnv)
	if err != nil {
		return nil
	}
	want := map[string]bool{cfgKeyLLMAPIKey: true, cfgKeyLLMBase: true}
	for _, it := range items {
		if want[it.Key] {
			if err := b.cfgRepo.Delete(ctx, it.ID); err != nil {
				log.Printf("dsBindingInjector unbindModel: 删 appconfig id=%s 失败: %v", it.ID, err)
			}
		}
	}
	return nil
}
