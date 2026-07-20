package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vrypan/listnr/internal/store"
)

func nodeInfoGet(t *testing.T, e *testEnv, target, ifNoneMatch string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, target, nil)
	if ifNoneMatch != "" {
		r.Header.Set("If-None-Match", ifNoneMatch)
	}
	w := httptest.NewRecorder()
	e.srv.Routes().ServeHTTP(w, r)
	return w
}

func TestNodeInfoDiscoveryLinksExactlyOneDocument(t *testing.T) {
	e := newTestEnv(t)
	w := nodeInfoGet(t, e, "https://ap.vrypan.net/.well-known/nodeinfo", "")
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", w.Code)
	}
	if got := w.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Fatalf("content-type = %q", got)
	}
	var doc struct {
		Links []struct {
			Rel  string `json:"rel"`
			Href string `json:"href"`
		} `json:"links"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Links) != 1 {
		t.Fatalf("links = %d, want exactly 1", len(doc.Links))
	}
	if doc.Links[0].Rel != "http://nodeinfo.diaspora.software/ns/schema/2.1" {
		t.Fatalf("rel = %q", doc.Links[0].Rel)
	}
	// The href must be absolute and on the configured host.
	if doc.Links[0].Href != "https://ap.vrypan.net/nodeinfo/2.1" {
		t.Fatalf("href = %q", doc.Links[0].Href)
	}
}

func TestNodeInfoPayloadMatchesSchema(t *testing.T) {
	e := newTestEnv(t)
	w := nodeInfoGet(t, e, "https://ap.vrypan.net/nodeinfo/2.1", "")
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", w.Code)
	}
	if got := w.Header().Get("Content-Type"); !strings.Contains(got, "schema/2.1#") {
		t.Fatalf("content-type = %q, want the 2.1 profile", got)
	}

	var doc struct {
		Version  string `json:"version"`
		Software struct {
			Name       string `json:"name"`
			Version    string `json:"version"`
			Repository string `json:"repository"`
			Homepage   string `json:"homepage"`
		} `json:"software"`
		Protocols []string `json:"protocols"`
		Services  struct {
			Inbound  []string `json:"inbound"`
			Outbound []string `json:"outbound"`
		} `json:"services"`
		OpenRegistrations bool `json:"openRegistrations"`
		Usage             struct {
			Users struct {
				Total          int `json:"total"`
				ActiveMonth    int `json:"activeMonth"`
				ActiveHalfyear int `json:"activeHalfyear"`
			} `json:"users"`
			LocalPosts    int `json:"localPosts"`
			LocalComments int `json:"localComments"`
		} `json:"usage"`
		Metadata map[string]any `json:"metadata"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Version != "2.1" || doc.Software.Name != "listnr" || doc.Software.Version == "" {
		t.Fatalf("software = %+v", doc.Software)
	}
	if strings.HasPrefix(doc.Software.Version, "v") {
		t.Fatalf("version = %q, want the leading v trimmed", doc.Software.Version)
	}
	const repo = "https://github.com/vrypan/listnr"
	if doc.Software.Repository != repo || doc.Software.Homepage != repo {
		t.Fatalf("repository/homepage = %q / %q", doc.Software.Repository, doc.Software.Homepage)
	}
	if len(doc.Protocols) != 1 || doc.Protocols[0] != "activitypub" {
		t.Fatalf("protocols = %v", doc.Protocols)
	}
	if doc.OpenRegistrations {
		t.Fatal("openRegistrations = true, want false")
	}
	if doc.Usage.Users.Total != 1 || doc.Usage.Users.ActiveMonth != 1 || doc.Usage.Users.ActiveHalfyear != 1 {
		t.Fatalf("users = %+v, want one active local actor", doc.Usage.Users)
	}
	if doc.Usage.LocalComments != 0 {
		t.Fatalf("localComments = %d, want 0 (remote replies are not local)", doc.Usage.LocalComments)
	}

	// Empty collections must serialize as [] and {}, never null.
	body := w.Body.String()
	for _, want := range []string{`"inbound":[]`, `"outbound":[]`, `"metadata":{}`} {
		if !strings.Contains(strings.ReplaceAll(body, " ", ""), want) {
			t.Fatalf("body missing %s:\n%s", want, body)
		}
	}
	if doc.Services.Inbound == nil || doc.Services.Outbound == nil || doc.Metadata == nil {
		t.Fatal("a collection deserialized as null")
	}
}

func TestNodeInfoUsageCountsOnlyActiveFederatedPosts(t *testing.T) {
	e := newTestEnv(t)

	localPosts := func() int {
		t.Helper()
		w := nodeInfoGet(t, e, "https://ap.vrypan.net/nodeinfo/2.1", "")
		var doc struct {
			Usage struct {
				LocalPosts int `json:"localPosts"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
			t.Fatal(err)
		}
		return doc.Usage.LocalPosts
	}

	if got := localPosts(); got != 1 {
		t.Fatalf("localPosts = %d, want 1", got)
	}

	// A seen-but-never-federated row is not published.
	if _, err := e.st.InsertPost(&store.Post{
		GUID: "guid-unfederated", URL: "https://blog.vrypan.net/old/",
		PublishedAt: "2026-01-01T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	if got := localPosts(); got != 1 {
		t.Fatalf("localPosts = %d after an unfederated row, want 1", got)
	}

	// A second federated post counts.
	if _, err := e.st.InsertPost(&store.Post{
		GUID: "guid-2", URL: "https://blog.vrypan.net/second/",
		PublishedAt: "2026-07-02T00:00:00Z",
		APID:        store.NullString("https://ap.vrypan.net/posts/1111111111111111"),
	}); err != nil {
		t.Fatal(err)
	}
	if got := localPosts(); got != 2 {
		t.Fatalf("localPosts = %d, want 2", got)
	}

	// A withdrawn post does not.
	adminDo(t, e, http.MethodDelete, "https://ap.vrypan.net/admin/posts/1")
	if got := localPosts(); got != 1 {
		t.Fatalf("localPosts = %d after a deletion, want 1", got)
	}
}

// NodeInfo is coarse on purpose: it must not disclose who follows the actor or
// who replied to it.
func TestNodeInfoDisclosesNoFollowersOrInteractions(t *testing.T) {
	e := newTestEnv(t)
	if err := e.st.UpsertFollower("https://remote.example/users/alice",
		"https://remote.example/users/alice/inbox", ""); err != nil {
		t.Fatal(err)
	}
	postID, _, _ := e.st.ResolvePost(postURL)
	if _, err := e.st.InsertInteraction(testInteraction(postID, "reply", "https://remote.example/notes/1")); err != nil {
		t.Fatal(err)
	}

	body := nodeInfoGet(t, e, "https://ap.vrypan.net/nodeinfo/2.1", "").Body.String()
	for _, leak := range []string{"alice", "remote.example", "followers", "hello"} {
		if strings.Contains(body, leak) {
			t.Fatalf("nodeinfo leaked %q:\n%s", leak, body)
		}
	}
	var doc struct {
		Usage struct {
			LocalComments int `json:"localComments"`
		} `json:"usage"`
	}
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Usage.LocalComments != 0 {
		t.Fatalf("a remote reply was counted as a local comment: %d", doc.Usage.LocalComments)
	}
}

func TestNodeInfoEndpointsArePublicAndRevalidate(t *testing.T) {
	for _, target := range []string{
		"https://ap.vrypan.net/.well-known/nodeinfo",
		"https://ap.vrypan.net/nodeinfo/2.1",
	} {
		e := newTestEnv(t)
		// No token: these endpoints are unauthenticated.
		first := nodeInfoGet(t, e, target, "")
		if first.Code != http.StatusOK {
			t.Fatalf("%s: code = %d, want 200", target, first.Code)
		}
		etag := first.Header().Get("ETag")
		if etag == "" {
			t.Fatalf("%s: no ETag", target)
		}
		if got := first.Header().Get("Cache-Control"); got != "public, max-age=0, must-revalidate" {
			t.Fatalf("%s: cache-control = %q", target, got)
		}
		second := nodeInfoGet(t, e, target, etag)
		if second.Code != http.StatusNotModified {
			t.Fatalf("%s: revalidation code = %d, want 304", target, second.Code)
		}
		if second.Body.Len() != 0 {
			t.Fatalf("%s: 304 body = %q, want empty", target, second.Body.String())
		}
	}
}

func TestNodeInfoRejectsNonGetMethods(t *testing.T) {
	e := newTestEnv(t)
	for _, target := range []string{
		"https://ap.vrypan.net/.well-known/nodeinfo",
		"https://ap.vrypan.net/nodeinfo/2.1",
	} {
		r := httptest.NewRequest(http.MethodPost, target, nil)
		w := httptest.NewRecorder()
		e.srv.Routes().ServeHTTP(w, r)
		if w.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s: POST code = %d, want 405", target, w.Code)
		}
	}
}

// Discovery must not depend on the database, so a broken statistics query
// cannot hide where NodeInfo lives.
func TestNodeInfoReportsDatabaseFailureGenerically(t *testing.T) {
	e := newTestEnv(t)
	if _, err := e.st.DB.Exec(`DROP TABLE posts`); err != nil {
		t.Fatal(err)
	}

	w := nodeInfoGet(t, e, "https://ap.vrypan.net/nodeinfo/2.1", "")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("code = %d, want 500", w.Code)
	}
	if strings.Contains(w.Body.String(), "posts") {
		t.Fatalf("error body leaked internals: %q", w.Body.String())
	}

	if got := nodeInfoGet(t, e, "https://ap.vrypan.net/.well-known/nodeinfo", "").Code; got != http.StatusOK {
		t.Fatalf("discovery code = %d, want 200 despite the statistics failure", got)
	}
}
