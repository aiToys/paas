// delivery_matrix.go 服务×环境版本矩阵（spec 2026-10-17 §5）。
//
// 跨运行/跨环境的聚合只读视图：基线 Workload × Environment × Release/Image，
// 回答「哪个环境运行哪个 digest、各环境是否同一候选版本、能否晋升」。
// 放 cmd/core 装配层而非 pipeline 包：数据源横跨 workload/environment/devops 三域，
// 在此做纯读聚合不引入反向依赖（与 deployDomain 桥接同款取舍）。
package main

import (
	"net/http"
	"strings"

	"github.com/aitoys/paas/internal/devops"
	"github.com/aitoys/paas/internal/environment"
	"github.com/aitoys/paas/internal/httputil"
	"github.com/aitoys/paas/internal/workload"
)

// MatrixCell 矩阵单元格：某服务在某基线环境当前运行的版本。
type MatrixCell struct {
	EnvID       string `json:"envId"`
	EnvName     string `json:"envName"`
	EnvType     string `json:"envType"`
	ServiceName string `json:"serviceName"` // 空=单服务
	WorkloadID  string `json:"workloadId"`
	ImageTag    string `json:"imageTag,omitempty"`
	Digest      string `json:"imageDigest,omitempty"` // ImageRef 不可变引用（有则展示）
	ReleaseID   string `json:"releaseId,omitempty"`   // 最近发布记录（按服务+环境匹配）
	Ready       int    `json:"ready"`
	Replicas    int    `json:"replicas"`
	Status      string `json:"status"`
	UpdatedAt   string `json:"updatedAt,omitempty"`
}

// deliveryMatrixHandler GET /api/applications/{id}/delivery-matrix。
type deliveryMatrixHandler struct {
	envs      environment.Repository
	workloads workload.Repository
	images    devops.ImageRepository
	releases  devops.ReleaseRepository
	authorize func(r *http.Request, perm string) bool
}

func (h *deliveryMatrixHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.authorize != nil && !h.authorize(r, "pipeline:read") {
		httputil.WriteError(w, http.StatusForbidden, "无权访问交付矩阵")
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/api/applications/")
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) != 2 || parts[1] != "delivery-matrix" || parts[0] == "" {
		httputil.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	appID := parts[0]
	cells, err := h.build(r, appID)
	if err != nil {
		httputil.WriteServiceError(w, http.StatusInternalServerError, err)
		return
	}
	httputil.WriteData(w, cells)
}

// build 聚合矩阵行：环境（按 PromoteOrder 阶序）× 基线 service 型 Workload。
// digest 展示优先 ImageRef（Release 编排写入的不可变引用）；发布记录按
// （服务, 环境）取最近一条（同格多 workload 属异常态，仍全部展示）。
func (h *deliveryMatrixHandler) build(r *http.Request, appID string) ([]MatrixCell, error) {
	ctx := r.Context()
	envs, err := h.envs.List(ctx)
	if err != nil {
		return nil, err
	}
	// laneID=default 基线 + service 型（矩阵只看对外服务版本，job/cronjob 不入矩阵）。
	wls, err := h.workloads.List(ctx, "", appID, workload.LaneDefault, "service", "")
	if err != nil {
		return nil, err
	}
	imgIdx := map[string]devops.Image{}
	if h.images != nil {
		if imgs, ierr := h.images.ListImages(ctx, appID); ierr == nil {
			for _, im := range imgs {
				imgIdx[im.ID] = im
			}
		}
	}
	type relKey struct{ envID, workloadID string }
	relIdx := map[relKey]string{}
	if h.releases != nil {
		if rels, rerr := h.releases.ListReleases(ctx, appID); rerr == nil {
			for _, rel := range rels {
				if rel.LaneID != "" && rel.LaneID != "default" {
					continue // 只关联基线发布（泳道发布不进矩阵）
				}
				k := relKey{envID: rel.EnvID, workloadID: rel.WorkloadID}
				if _, seen := relIdx[k]; !seen { // ListReleases 倒序，首条即最近
					relIdx[k] = rel.ID
				}
			}
		}
	}
	envIdx := map[string]environment.Environment{}
	for _, e := range envs {
		envIdx[e.ID] = e
	}
	cells := make([]MatrixCell, 0, len(wls))
	for _, wl := range wls {
		env := envIdx[wl.EnvID]
		cell := MatrixCell{
			EnvID: wl.EnvID, EnvName: env.Name, EnvType: env.Type,
			ServiceName: wl.Service, WorkloadID: wl.ID,
			ImageTag: wl.Image, Digest: wl.ImageRef,
			Ready: wl.Ready, Replicas: wl.Replicas, Status: wl.Status,
			UpdatedAt: wl.CreatedAt.Format("2006-01-02 15:04"),
			ReleaseID: relIdx[relKey{envID: wl.EnvID, workloadID: wl.ID}],
		}
		if cell.Digest == "" {
			// 无 ImageRef 时尽力从镜像记录取 digest（按 tag 引用的存量负载）。
			for _, im := range imgIdx {
				if im.Registry != "" && strings.HasSuffix(wl.Image, im.Tag) && im.AppID == appID {
					cell.Digest = im.Digest
					break
				}
			}
		}
		cells = append(cells, cell)
	}
	return cells, nil
}
