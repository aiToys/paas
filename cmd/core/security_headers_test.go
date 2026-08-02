package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSecurityHeaders_BasicHeaders(t *testing.T) {
	h := securityHeadersMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	hdr := rec.Header()
	if hdr.Get("X-Frame-Options") != "DENY" {
		t.Error("缺 X-Frame-Options:DENY")
	}
	if hdr.Get("X-Content-Type-Options") != "nosniff" {
		t.Error("缺 X-Content-Type-Options:nosniff")
	}
	if hdr.Get("Referrer-Policy") == "" {
		t.Error("缺 Referrer-Policy")
	}
	if hdr.Get("Content-Security-Policy") == "" {
		t.Error("缺 Content-Security-Policy")
	}
}

func TestSecurityHeaders_HSTSOnlyOnHTTPS(t *testing.T) {
	h := securityHeadersMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))

	// HTTP：不发 HSTS
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if v := rec.Header().Get("Strict-Transport-Security"); v != "" {
		t.Errorf("HTTP 不应发 HSTS，got %q", v)
	}

	// HTTPS（ingress X-Forwarded-Proto=https）：发 HSTS
	rec2 := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	h.ServeHTTP(rec2, req)
	if v := rec2.Header().Get("Strict-Transport-Security"); v == "" {
		t.Error("HTTPS 应发 HSTS")
	}
}

func TestSecurityHeaders_PassesThrough(t *testing.T) {
	called := false
	h := securityHeadersMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if !called {
		t.Error("下游 handler 未被调用")
	}
}
