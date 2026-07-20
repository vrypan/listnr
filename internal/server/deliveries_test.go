package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func seedDelivery(t *testing.T, e *testEnv, activityJSON, inbox, status string) int64 {
	t.Helper()
	res, err := e.st.DB.Exec(`
		INSERT INTO deliveries (activity_json, inbox_url, status, attempts, next_attempt_at, last_error)
		VALUES (?, ?, ?, 3, '2026-07-20T10:00:00Z', 'dial tcp: connection refused')`,
		activityJSON, inbox, status)
	if err != nil {
		t.Fatal(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestAdminDeliveriesRequireAuthorization(t *testing.T) {
	e := newTestEnv(t)
	e.srv.cfg.Admin.Token = "secret"
	r := httptest.NewRequest(http.MethodGet, "https://ap.vrypan.net/admin/deliveries", nil)
	w := httptest.NewRecorder()
	e.srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want 401", w.Code)
	}
}

func TestAdminDeliveriesListRedactsPayload(t *testing.T) {
	e := newTestEnv(t)
	seedDelivery(t, e, `{"id":"https://ap.vrypan.net/posts/1#create","type":"Create","object":{"content":"<p>private reply text</p>"}}`,
		"https://a.example/inbox", "failed")

	w := adminDo(t, e, http.MethodGet, "https://ap.vrypan.net/admin/deliveries")
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, body %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if strings.Contains(body, "private reply text") || strings.Contains(body, "activity_json") {
		t.Fatalf("response leaked the activity payload: %s", body)
	}
	if w.Header().Get("Cache-Control") != "no-store, private" {
		t.Fatalf("cache-control = %q", w.Header().Get("Cache-Control"))
	}
	var rows []struct {
		Status       string `json:"status"`
		ActivityType string `json:"activity_type"`
		ActivityID   string `json:"activity_id"`
		LastError    string `json:"last_error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ActivityType != "Create" ||
		rows[0].ActivityID != "https://ap.vrypan.net/posts/1#create" {
		t.Fatalf("rows = %+v", rows)
	}
	// The full error text is preserved for the JSON consumer.
	if rows[0].LastError != "dial tcp: connection refused" {
		t.Fatalf("last_error = %q, want the full text", rows[0].LastError)
	}
}

func TestAdminDeliveriesValidateQueryParameters(t *testing.T) {
	e := newTestEnv(t)
	for _, query := range []string{"?status=bogus", "?limit=0", "?limit=abc", "?offset=-1"} {
		w := adminDo(t, e, http.MethodGet, "https://ap.vrypan.net/admin/deliveries"+query)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s code = %d, want 400", query, w.Code)
		}
	}
	for _, query := range []string{"", "?status=pending", "?status=failed", "?status=done", "?limit=500"} {
		w := adminDo(t, e, http.MethodGet, "https://ap.vrypan.net/admin/deliveries"+query)
		if w.Code != http.StatusOK {
			t.Fatalf("%s code = %d, want 200", query, w.Code)
		}
	}
}

func TestAdminDeliveriesFilterByStatus(t *testing.T) {
	e := newTestEnv(t)
	seedDelivery(t, e, `{"type":"Create"}`, "https://a.example/inbox", "pending")
	seedDelivery(t, e, `{"type":"Delete"}`, "https://b.example/inbox", "failed")

	w := adminDo(t, e, http.MethodGet, "https://ap.vrypan.net/admin/deliveries?status=failed")
	var rows []struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Status != "failed" {
		t.Fatalf("rows = %+v, want only the failed delivery", rows)
	}
}

func TestAdminDeliveryRetryAndDeleteStatusMapping(t *testing.T) {
	e := newTestEnv(t)
	failed := seedDelivery(t, e, `{"type":"Create"}`, "https://a.example/inbox", "failed")
	pending := seedDelivery(t, e, `{"type":"Create"}`, "https://b.example/inbox", "pending")

	base := "https://ap.vrypan.net/admin/deliveries/"
	cases := []struct {
		name   string
		method string
		target string
		want   int
	}{
		{"retry failed", http.MethodPost, base + strconv.FormatInt(failed, 10) + "/retry", http.StatusOK},
		{"retry pending", http.MethodPost, base + strconv.FormatInt(pending, 10) + "/retry", http.StatusConflict},
		{"retry unknown", http.MethodPost, base + "9999/retry", http.StatusNotFound},
		{"retry non-numeric", http.MethodPost, base + "abc/retry", http.StatusBadRequest},
		{"delete pending", http.MethodDelete, base + strconv.FormatInt(pending, 10), http.StatusConflict},
		{"delete unknown", http.MethodDelete, base + "9999", http.StatusNotFound},
		{"delete non-numeric", http.MethodDelete, base + "abc", http.StatusBadRequest},
	}
	for _, c := range cases {
		w := adminDo(t, e, c.method, c.target)
		if w.Code != c.want {
			t.Fatalf("%s: code = %d, want %d (body %s)", c.name, w.Code, c.want, w.Body.String())
		}
	}

	// The retried row is now pending, so deleting it is refused too.
	w := adminDo(t, e, http.MethodDelete, base+strconv.FormatInt(failed, 10))
	if w.Code != http.StatusConflict {
		t.Fatalf("delete requeued row code = %d, want 409", w.Code)
	}
}

func TestAdminRetryFailedDeliveriesReportsCount(t *testing.T) {
	e := newTestEnv(t)
	seedDelivery(t, e, `{"type":"Create"}`, "https://a.example/inbox", "failed")
	seedDelivery(t, e, `{"type":"Create"}`, "https://b.example/inbox", "failed")
	seedDelivery(t, e, `{"type":"Create"}`, "https://c.example/inbox", "pending")

	w := adminDo(t, e, http.MethodPost, "https://ap.vrypan.net/admin/deliveries/retry-failed")
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, body %s", w.Code, w.Body.String())
	}
	var result struct {
		Retried int `json:"retried"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Retried != 2 {
		t.Fatalf("retried = %d, want 2", result.Retried)
	}
}
