package server

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRequestLoggingOptional(t *testing.T) {
	e := newTestEnv(t)
	var buf bytes.Buffer
	e.srv.log = slog.New(slog.NewTextHandler(&buf, nil))

	r := httptest.NewRequest(http.MethodGet, "https://ap.vrypan.net/healthz", nil)
	w := httptest.NewRecorder()
	e.srv.Routes().ServeHTTP(w, r)
	if strings.Contains(buf.String(), "http request") {
		t.Fatalf("request logged with log_requests disabled: %s", buf.String())
	}

	e.srv.cfg.Server.LogRequests = true
	r = httptest.NewRequest(http.MethodGet, "https://ap.vrypan.net/healthz", nil)
	w = httptest.NewRecorder()
	e.srv.Routes().ServeHTTP(w, r)
	logged := buf.String()
	if !strings.Contains(logged, "http request") || !strings.Contains(logged, "status=200") {
		t.Fatalf("request log missing expected fields: %s", logged)
	}
}
