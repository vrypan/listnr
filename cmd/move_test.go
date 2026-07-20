package cmd

import (
	"net/http"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func moveCommand(f *cliFixture, to string, yes bool) *cobra.Command {
	return f.command(func(c *cobra.Command) {
		c.Flags().String("to", to, "")
		c.Flags().Bool("yes", yes, "")
	})
}

// Migration is irreversible, so it must not happen without explicit consent.
func TestActorMoveRequiresConfirmation(t *testing.T) {
	f := newCLIFixture(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("an unconfirmed move must not reach the server")
	})
	err := runActorMove(moveCommand(f, "https://mastodon.example/users/me", false), nil)
	if err == nil {
		t.Fatal("want an error without --yes")
	}
	if !strings.Contains(err.Error(), "irreversible") || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("err = %v, want it to explain the risk and how to confirm", err)
	}
	// The irreversible target is named before anything is submitted.
	if !strings.Contains(err.Error(), "https://mastodon.example/users/me") {
		t.Fatalf("err = %v, want it to show the target", err)
	}
	if len(f.requests) != 0 {
		t.Fatalf("sent %d requests, want 0", len(f.requests))
	}
}

func TestActorMoveValidatesTargetLocally(t *testing.T) {
	f := newCLIFixture(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("an invalid target must not reach the server")
	})
	cases := map[string]string{
		"missing":     "",
		"bare handle": "@me@mastodon.example",
		"http":        "http://mastodon.example/users/me",
		"host only":   "mastodon.example",
	}
	for name, target := range cases {
		if err := runActorMove(moveCommand(f, target, true), nil); err == nil {
			t.Errorf("%s (%q): want an error", name, target)
		}
	}
	if len(f.requests) != 0 {
		t.Fatalf("sent %d requests, want 0", len(f.requests))
	}
}

func TestActorMoveReportsFirstAndRepeatOutcomes(t *testing.T) {
	body := `{"ok":true,"target":"https://mastodon.example/users/me",
	          "activity_id":"https://ap.vrypan.net/actor#move-abc",
	          "moved_at":"2026-07-20T10:00:00Z","already_moved":false,"queued":5}`
	f := newCLIFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(body))
	})
	cmd := moveCommand(f, "https://mastodon.example/users/me", true)
	if err := runActorMove(cmd, nil); err != nil {
		t.Fatal(err)
	}
	if f.requests[0].Method != http.MethodPost || f.requests[0].URL.Path != "/admin/actor/move" {
		t.Fatalf("request = %s %s", f.requests[0].Method, f.requests[0].URL.Path)
	}
	out := f.out.String()
	if !strings.Contains(out, "moved to https://mastodon.example/users/me") ||
		!strings.Contains(out, "5 deliveries queued") {
		t.Fatalf("output = %q", out)
	}
	// The operator is told the old instance is still part of the migration.
	if !strings.Contains(out, "online") {
		t.Fatalf("output = %q, want a note about keeping this instance online", out)
	}

	body = `{"ok":true,"target":"https://mastodon.example/users/me",
	         "moved_at":"2026-07-20T10:00:00Z","already_moved":true,"queued":0}`
	f.out.Reset()
	if err := runActorMove(cmd, nil); err != nil {
		t.Fatal(err)
	}
	if out := f.out.String(); !strings.Contains(out, "already moved") ||
		!strings.Contains(out, "nothing queued") {
		t.Fatalf("repeat output = %q", out)
	}
}

func TestActorMoveSurfacesValidationFailureClearly(t *testing.T) {
	f := newCLIFixture(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "move target https://mastodon.example/users/me does not list "+
			"https://ap.vrypan.net/actor in its alsoKnownAs; add the alias on the target account first",
			http.StatusBadRequest)
	})
	err := runActorMove(moveCommand(f, "https://mastodon.example/users/me", true), nil)
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "alsoKnownAs") {
		t.Fatalf("err = %v, want the reciprocal alias failure surfaced verbatim", err)
	}
}

func TestActorMoveSurfacesDifferentTargetConflict(t *testing.T) {
	f := newCLIFixture(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "actor has already moved to https://other.example/users/me", http.StatusConflict)
	})
	err := runActorMove(moveCommand(f, "https://mastodon.example/users/me", true), nil)
	if err == nil || !strings.Contains(err.Error(), "409") ||
		!strings.Contains(err.Error(), "already moved") {
		t.Fatalf("err = %v, want a clear conflict", err)
	}
}

func TestActorMoveStatusOutput(t *testing.T) {
	f := newCLIFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"moved":false}`))
	})
	if err := runActorMoveStatus(f.command(nil), nil); err != nil {
		t.Fatal(err)
	}
	if f.requests[0].Method != http.MethodGet || f.requests[0].URL.Path != "/admin/actor/move" {
		t.Fatalf("request = %s %s", f.requests[0].Method, f.requests[0].URL.Path)
	}
	if out := f.out.String(); !strings.Contains(out, "not moved") {
		t.Fatalf("output = %q", out)
	}

	f2 := newCLIFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"moved":true,"target":"https://mastodon.example/users/me",
			"activity_id":"https://ap.vrypan.net/actor#move-abc",
			"target_fingerprint":"deadbeef","moved_at":"2026-07-20T10:00:00Z"}`))
	})
	if err := runActorMoveStatus(f2.command(nil), nil); err != nil {
		t.Fatal(err)
	}
	out := f2.out.String()
	for _, want := range []string{
		"moved to https://mastodon.example/users/me",
		"2026-07-20T10:00:00Z", "move-abc", "deadbeef",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}
