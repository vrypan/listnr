package server

import (
	"html/template"
	"net/http"

	"github.com/vrypan/listnr/internal/store"
)

// interstitialTmpl is served to browsers hitting /posts/{id}. Fediverse
// software fetches the same URL with an ActivityPub Accept header and gets
// the Note instead. The page sends the visitor to the post on their own
// instance via Mastodon's /authorize_interaction endpoint, remembering the
// instance in localStorage and auto-redirecting on later visits (once per
// tab session, so a mistyped instance doesn't trap them).
var interstitialTmpl = template.Must(template.New("interstitial").Parse(`<!doctype html>
<html lang="en">
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}} — on the fediverse</title>
<style>
  body { font-family: system-ui, sans-serif; max-width: 32rem; margin: 4rem auto; padding: 0 1rem; line-height: 1.5; }
  input { font-size: 1rem; padding: .5rem; width: 14rem; }
  button { font-size: 1rem; padding: .5rem 1rem; }
  .muted { color: #666; font-size: .9rem; }
  @media (prefers-color-scheme: dark) { body { background: #111; color: #ddd; } .muted { color: #999; } }
</style>
<h1>{{.Title}}</h1>
<p>This post lives on the fediverse. To reply, like, or boost it from your
own account, enter your instance:</p>
<form id="f">
  <input id="instance" placeholder="mastodon.social" autocapitalize="off" autocorrect="off">
  <button type="submit">Open</button>
</form>
<p class="muted">Or read the post on the blog:
<a href="{{.BlogURL}}">{{.BlogURL}}</a><br>
You can also paste <code>{{.NoteID}}</code> into your instance&rsquo;s search box.</p>
<script>
(function () {
  var key = "listnr-instance";
  var note = {{.NoteID}};
  var saved = "";
  try { saved = localStorage.getItem(key) || ""; } catch (e) {}
  var input = document.getElementById("instance");
  if (saved) input.value = saved;
  function go(domain) {
    domain = domain.trim().replace(/^@/, "").replace(/^https?:\/\//, "").replace(/\/.*$/, "");
    if (!domain) domain = "mastodon.social";
    try { localStorage.setItem(key, domain); } catch (e) {}
    location.href = "https://" + domain + "/authorize_interaction?uri=" + encodeURIComponent(note);
  }
  document.getElementById("f").addEventListener("submit", function (ev) {
    ev.preventDefault();
    go(input.value);
  });
  if (saved && !sessionStorage.getItem("listnr-redirected")) {
    try { sessionStorage.setItem("listnr-redirected", "1"); } catch (e) {}
    go(saved);
  }
})();
</script>
</html>
`))

func (s *Server) serveInterstitial(w http.ResponseWriter, post *store.Post) {
	title := post.Title
	if title == "" {
		title = "A post"
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	interstitialTmpl.Execute(w, map[string]string{
		"Title":   title,
		"BlogURL": post.URL,
		"NoteID":  post.APID.String,
	})
}
