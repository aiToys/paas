package workload

import (
	"context"
	"testing"
)

// OnCreateRepo（部署即注册挂点）：仅 service 类型触发、回调失败/panic 不阻断创建主路径。
func TestOnCreateRepo(t *testing.T) {
	ctx := context.Background()

	// service 类型触发回调
	inner := &fakeRepo{}
	var got Workload
	repo := &OnCreateRepo{Repository: inner, OnCreate: func(_ context.Context, w Workload) {
		got = w
	}}
	w := Workload{ID: "wl-1", Type: TypeService, Name: "app-1-web"}
	if err := repo.Create(ctx, w); err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	if got.ID != "wl-1" {
		t.Fatalf("service 类型应触发回调，got %+v", got)
	}

	// job 类型不触发
	got = Workload{}
	repo2 := &OnCreateRepo{Repository: &fakeRepo{}, OnCreate: func(_ context.Context, w Workload) { got = w }}
	if err := repo2.Create(ctx, Workload{ID: "wl-2", Type: TypeJob}); err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	if got.ID != "" {
		t.Fatalf("job 类型不应触发回调，got %+v", got)
	}

	// 回调 panic 不传播（创建仍成功）
	repo3 := &OnCreateRepo{Repository: &fakeRepo{}, OnCreate: func(context.Context, Workload) { panic("boom") }}
	if err := repo3.Create(ctx, Workload{ID: "wl-3", Type: TypeService}); err != nil {
		t.Fatalf("回调 panic 不应阻断创建: %v", err)
	}

	// 回调 nil 透传；Create 失败原样返回
	repo4 := &OnCreateRepo{Repository: &fakeRepo{}}
	if err := repo4.Create(ctx, w); err != nil {
		t.Fatalf("nil 回调应透传: %v", err)
	}
	repo5 := &OnCreateRepo{Repository: &fakeRepo{createErr: context.Canceled}, OnCreate: func(context.Context, Workload) {}}
	if err := repo5.Create(ctx, w); err == nil {
		t.Fatal("inner 失败应原样返回")
	}
}
