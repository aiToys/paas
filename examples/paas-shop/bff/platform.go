package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type shopLane struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	EnvID       string `json:"envId"`
	Status      string `json:"status"`
	Description string `json:"description,omitempty"`
}

type shopPlatform struct {
	EnvID      string     `json:"envId"`
	EnvName    string     `json:"envName"`
	EnvType    string     `json:"envType"`
	AppID      string     `json:"appId"`
	ConsoleURL string     `json:"consoleUrl"`
	Discovery  bool       `json:"discovery"`
	Lanes      []shopLane `json:"lanes"`
}

// 固定只读聚合入口。作用域与平台凭证只从部署配置读取，不接受浏览器指定 URL/环境。
func platformLanes(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		platformError(w, http.StatusMethodNotAllowed, "仅支持读取泳道")
		return
	}
	base := strings.TrimRight(firstEnv("PAAS_PLATFORM_URL", "PAAS_APPCONFIG_URL", "PAAS_CORE_URL"), "/")
	token := firstEnv("PAAS_PLATFORM_TOKEN", "PAAS_API_KEY", "PAAS_DP_TOKEN")
	envID := firstEnv("PAAS_CONFIG_ENV", "PAAS_ENV_ID")
	if base == "" || token == "" || envID == "" {
		platformError(w, http.StatusServiceUnavailable, "未配置平台地址、只读凭证或运行环境；当前仅使用基线")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	var env struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Type string `json:"type"`
	}
	if err := readPlatform(ctx, base, token, "/api/environments/"+url.PathEscape(envID), &env); err != nil || env.ID != envID || (env.Type != "test" && env.Type != "prod") {
		platformError(w, http.StatusBadGateway, "无法核实运行环境，请检查平台连接与 environment:read 权限")
		return
	}
	out := shopPlatform{EnvID: envID, EnvName: env.Name, EnvType: env.Type, AppID: firstEnv("PAAS_SHOP_APP_ID"), ConsoleURL: publicConsoleURL(), Discovery: dp.Enabled(), Lanes: []shopLane{}}
	// 生产不提供联调候选；环境类型来自平台，不能由域名猜测。
	if env.Type != "prod" {
		var lanes []shopLane
		if err := readPlatform(ctx, base, token, "/api/lanes?"+url.Values{"envId": {envID}}.Encode(), &lanes); err != nil {
			platformError(w, http.StatusBadGateway, "泳道列表读取失败，请检查平台连接与 governance:read 权限")
			return
		}
		for _, lane := range lanes {
			if lane.EnvID == envID && lane.Status == "active" && lane.Name != "default" {
				out.Lanes = append(out.Lanes, lane)
			}
		}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"data": out})
}

func readPlatform(ctx context.Context, base, token, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	// 不跟随重定向，避免把平台凭证送到错误端点。
	client := &http.Client{Transport: httpClient.Transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("platform HTTP %d", resp.StatusCode)
	}
	var body struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return err
	}
	if len(body.Data) == 0 || string(body.Data) == "null" {
		return errors.New("missing platform data")
	}
	return json.Unmarshal(body.Data, out)
}

func platformError(w http.ResponseWriter, status int, msg string) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func publicConsoleURL() string {
	raw := firstEnv("PAAS_CONSOLE_URL")
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return ""
	}
	return strings.TrimRight(u.String(), "/") + "/"
}
