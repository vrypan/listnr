package cmd

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// cliFixture points the admin client at a stub server and captures a
// command's output. It isolates HOME so a developer's real cli.toml cannot
// influence the test.
type cliFixture struct {
	out      *bytes.Buffer
	requests []*http.Request
}

func newCLIFixture(t *testing.T, handler http.HandlerFunc) *cliFixture {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	f := &cliFixture{out: &bytes.Buffer{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.requests = append(f.requests, r)
		handler(w, r)
	}))
	t.Cleanup(srv.Close)

	previousServer, previousToken := cliServer, cliToken
	cliServer, cliToken = srv.URL, "secret"
	t.Cleanup(func() { cliServer, cliToken = previousServer, previousToken })
	return f
}

// command builds a throwaway cobra command carrying the flags a run function
// reads, plus a context for the outbound request.
func (f *cliFixture) command(flags func(*cobra.Command)) *cobra.Command {
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	cmd.SetOut(f.out)
	if flags != nil {
		flags(cmd)
	}
	return cmd
}

func TestPostsListRendersDeletionStatus(t *testing.T) {
	f := newCLIFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[
			{"id":2,"url":"https://blog.example/two","title":"Two",
			 "ap_id":"https://ap.example/posts/two","published_at":"2026-07-02T00:00:00Z"},
			{"id":1,"url":"https://blog.example/one","title":"One",
			 "ap_id":"https://ap.example/posts/one","published_at":"2026-07-01T00:00:00Z",
			 "deleted_at":"2026-07-20T10:00:00Z"}
		]`))
	})
	cmd := f.command(func(c *cobra.Command) {
		c.Flags().Int("limit", 5, "")
		c.Flags().Int("offset", 10, "")
	})
	if err := runPostsList(cmd, nil); err != nil {
		t.Fatal(err)
	}
	if got := f.requests[0].URL.RequestURI(); got != "/admin/posts?limit=5&offset=10" {
		t.Fatalf("request URI = %q", got)
	}
	out := f.out.String()
	for _, want := range []string{"ID", "STATUS", "live", "deleted 2026-07-20T10:00:00Z", "Two", "One"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}

func TestPostsListOmitsUnsetPagination(t *testing.T) {
	f := newCLIFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[]`))
	})
	cmd := f.command(func(c *cobra.Command) {
		c.Flags().Int("limit", 0, "")
		c.Flags().Int("offset", 0, "")
	})
	if err := runPostsList(cmd, nil); err != nil {
		t.Fatal(err)
	}
	if got := f.requests[0].URL.RequestURI(); got != "/admin/posts" {
		t.Fatalf("request URI = %q, want no query string", got)
	}
	if !strings.Contains(f.out.String(), "ID") {
		t.Fatal("empty listing should still print a header")
	}
}

func TestPostDeleteReportsFirstAndRepeatOutcomes(t *testing.T) {
	body := `{"ok":true,"id":1,"ap_id":"https://ap.example/posts/one",
	          "deleted_at":"2026-07-20T10:00:00Z","already_deleted":false,"queued":3}`
	f := newCLIFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(body))
	})
	cmd := f.command(nil)
	if err := runPostDelete(cmd, []string{"1"}); err != nil {
		t.Fatal(err)
	}
	if f.requests[0].Method != http.MethodDelete || f.requests[0].URL.Path != "/admin/posts/1" {
		t.Fatalf("request = %s %s", f.requests[0].Method, f.requests[0].URL.Path)
	}
	if out := f.out.String(); !strings.Contains(out, "deleted at 2026-07-20T10:00:00Z") ||
		!strings.Contains(out, "3 deliveries queued") {
		t.Fatalf("output = %q", out)
	}

	body = `{"ok":true,"id":1,"ap_id":"https://ap.example/posts/one",
	         "deleted_at":"2026-07-20T10:00:00Z","already_deleted":true,"queued":0}`
	f.out.Reset()
	if err := runPostDelete(cmd, []string{"1"}); err != nil {
		t.Fatal(err)
	}
	if out := f.out.String(); !strings.Contains(out, "already deleted at 2026-07-20T10:00:00Z") {
		t.Fatalf("repeat output = %q", out)
	}
}

func TestPostDeleteRejectsNonNumericIDLocally(t *testing.T) {
	f := newCLIFixture(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("a non-numeric id must not reach the server")
	})
	if err := runPostDelete(f.command(nil), []string{"https://blog.example/one"}); err == nil {
		t.Fatal("want an error for a non-numeric post id")
	}
	if len(f.requests) != 0 {
		t.Fatalf("sent %d requests, want 0", len(f.requests))
	}
}

func TestPostDeleteSurfacesServerErrors(t *testing.T) {
	f := newCLIFixture(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "404 page not found", http.StatusNotFound)
	})
	err := runPostDelete(f.command(nil), []string{"9999"})
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("err = %v, want it to mention HTTP 404", err)
	}
}
