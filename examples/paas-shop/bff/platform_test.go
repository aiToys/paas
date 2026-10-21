package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPlatformLanesScopeAndCredentials(t *testing.T) {
	prod := false
	broken := false
	platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer private-token" {
			t.Error("server credential not used")
		}
		switch r.URL.Path {
		case "/api/environments/test-env":
			kind := "test"
			if prod {
				kind = "prod"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]string{"id": "test-env", "name": "测试", "type": kind}})
		case "/api/lanes":
			if prod {
				t.Error("production must not query lane candidates")
			}
			if r.URL.Query().Get("envId") != "test-env" {
				t.Error("browser changed configured scope")
			}
			if broken {
				w.WriteHeader(403)
				return
			}
			_, _ = w.Write([]byte(`{"data":[{"id":"a","name":"feature-x","envId":"test-env","status":"active"},{"id":"b","name":"closed","envId":"test-env","status":"closed"},{"id":"c","name":"foreign","envId":"other-env","status":"active"}]}`))
		default:
			t.Errorf("unexpected platform request %s", r.URL)
			http.NotFound(w, r)
		}
	}))
	defer platform.Close()
	t.Setenv("PAAS_PLATFORM_URL", platform.URL)
	t.Setenv("PAAS_PLATFORM_TOKEN", "private-token")
	t.Setenv("PAAS_CONFIG_ENV", "test-env")
	t.Setenv("PAAS_CONSOLE_URL", "https://console.example.com/next/")
	request := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/api/platform/lanes?envId=other-env&url=https://evil.example", nil)
		r.Header.Set("Authorization", "Bearer browser-token")
		w := httptest.NewRecorder()
		platformLanes(w, r)
		return w
	}
	w := request()
	var out struct {
		Data shopPlatform `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || len(out.Data.Lanes) != 1 || out.Data.Lanes[0].ID != "a" || strings.Contains(w.Body.String(), "private-token") || strings.Contains(w.Body.String(), platform.URL) {
		t.Fatalf("unexpected response %d %s", w.Code, w.Body)
	}
	broken = true
	if w := request(); w.Code != 502 {
		t.Fatalf("lane read failure hidden: %d %s", w.Code, w.Body)
	}
	prod = true
	w = request()
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 200 || len(out.Data.Lanes) != 0 || out.Data.EnvType != "prod" {
		t.Fatalf("prod response %d %s", w.Code, w.Body)
	}
}

func TestPlatformLanesMissingConfigAndMethod(t *testing.T) {
	t.Setenv("PAAS_PLATFORM_URL", "")
	t.Setenv("PAAS_APPCONFIG_URL", "")
	t.Setenv("PAAS_CORE_URL", "")
	w := httptest.NewRecorder()
	platformLanes(w, httptest.NewRequest(http.MethodGet, "/api/platform/lanes", nil))
	if w.Code != 503 {
		t.Fatalf("got %d", w.Code)
	}
	w = httptest.NewRecorder()
	platformLanes(w, httptest.NewRequest(http.MethodPost, "/api/platform/lanes", nil))
	if w.Code != 405 || w.Header().Get("Allow") != "GET" {
		t.Fatalf("got %d", w.Code)
	}
}
