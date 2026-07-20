package cmd

import (
	"net/http"
	"strings"
	"testing"
)

func TestActorPublishReportsQueuedUpdate(t *testing.T) {
	f := newCLIFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"published":true,"fingerprint":"abc123","queued":4}`))
	})
	if err := runActorPublish(f.command(nil), nil); err != nil {
		t.Fatal(err)
	}
	if f.requests[0].Method != http.MethodPost || f.requests[0].URL.Path != "/admin/actor/publish" {
		t.Fatalf("request = %s %s", f.requests[0].Method, f.requests[0].URL.Path)
	}
	out := f.out.String()
	if !strings.Contains(out, "published") || !strings.Contains(out, "abc123") ||
		!strings.Contains(out, "4 deliveries queued") {
		t.Fatalf("output = %q", out)
	}
}

func TestActorPublishReportsUnchangedProfile(t *testing.T) {
	f := newCLIFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"published":false,"fingerprint":"abc123","queued":0}`))
	})
	if err := runActorPublish(f.command(nil), nil); err != nil {
		t.Fatal(err)
	}
	if out := f.out.String(); !strings.Contains(out, "unchanged") || !strings.Contains(out, "nothing queued") {
		t.Fatalf("output = %q", out)
	}
}

// The command must never carry profile data: the server's TOML config is the
// only source of the actor document.
func TestActorPublishSendsNoBody(t *testing.T) {
	f := newCLIFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength > 0 {
			t.Errorf("request body length = %d, want 0", r.ContentLength)
		}
		w.Write([]byte(`{"published":true,"fingerprint":"abc123","queued":0}`))
	})
	if err := runActorPublish(f.command(nil), nil); err != nil {
		t.Fatal(err)
	}
}

func TestActorPublishSurfacesServerErrors(t *testing.T) {
	f := newCLIFixture(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "server error", http.StatusInternalServerError)
	})
	err := runActorPublish(f.command(nil), nil)
	if err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("err = %v, want it to mention HTTP 500", err)
	}
}
