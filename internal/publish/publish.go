package publish

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"html"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/microcosm-cc/bluemonday"
	"github.com/vrypan/listnr/internal/config"
	"github.com/vrypan/listnr/internal/store"
)

const Public = "https://www.w3.org/ns/activitystreams#Public"

var tagRE = regexp.MustCompile(`<[^>]*>`)

func NoteID(host, guid string) string {
	sum := sha256.Sum256([]byte(guid))
	return "https://" + host + "/posts/" + hex.EncodeToString(sum[:])[:16]
}

func ContentHash(title, summary, link string) string {
	sum := sha256.Sum256([]byte(title + "\x00" + summary + "\x00" + link))
	return hex.EncodeToString(sum[:])
}

func SummaryHTML(raw string) string {
	clean := bluemonday.UGCPolicy().Sanitize(raw)
	text := strings.TrimSpace(html.UnescapeString(tagRE.ReplaceAllString(clean, " ")))
	text = strings.Join(strings.Fields(text), " ")
	text = truncateVisible(text, 500)
	if text == "" {
		return ""
	}
	return "<p>" + html.EscapeString(text) + "</p>"
}

func truncateVisible(s string, limit int) string {
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && !unicode.IsSpace(r[cut-1]) {
		cut--
	}
	if cut < limit/2 {
		cut = limit
	}
	return strings.TrimSpace(string(r[:cut])) + "…"
}

func Note(cfg config.Actor, p *store.Post) map[string]any {
	apID := p.APID.String
	content := "<p><strong>" + html.EscapeString(p.Title) + "</strong></p>" +
		p.SummaryHTML +
		`<p><a href="` + html.EscapeString(p.URL) + `">` + html.EscapeString(p.URL) + `</a></p>`
	note := map[string]any{
		"id":           apID,
		"type":         "Note",
		"attributedTo": cfg.ID(),
		"to":           []string{Public},
		"cc":           []string{"https://" + cfg.Host + "/followers"},
		"published":    p.PublishedAt,
		"url":          p.URL,
		"content":      content,
	}
	if p.UpdatedAt.Valid && p.UpdatedAt.String != "" {
		note["updated"] = p.UpdatedAt.String
	}
	return note
}

func Create(cfg config.Actor, p *store.Post) map[string]any {
	apID := p.APID.String
	return map[string]any{
		"@context": "https://www.w3.org/ns/activitystreams",
		"id":       apID + "#create",
		"type":     "Create",
		"actor":    cfg.ID(),
		"to":       []string{Public},
		"cc":       []string{"https://" + cfg.Host + "/followers"},
		"object":   Note(cfg, p),
	}
}

func Update(cfg config.Actor, p *store.Post, updated time.Time) map[string]any {
	ts := updated.UTC().Format(time.RFC3339)
	p.UpdatedAt = store.NullString(ts)
	return map[string]any{
		"@context": "https://www.w3.org/ns/activitystreams",
		"id":       p.APID.String + "#update-" + strconv.FormatInt(updated.UTC().Unix(), 10),
		"type":     "Update",
		"actor":    cfg.ID(),
		"to":       []string{Public},
		"cc":       []string{"https://" + cfg.Host + "/followers"},
		"object":   Note(cfg, p),
	}
}

// Delete withdraws a post. Its object is the Note's AP id rather than an
// embedded Note: the object is gone, so there is nothing left to describe.
// The id is derived from that AP id, so re-sending a Delete for the same post
// is recognisably the same activity.
func Delete(cfg config.Actor, p *store.Post) map[string]any {
	apID := p.APID.String
	return map[string]any{
		"@context":  "https://www.w3.org/ns/activitystreams",
		"id":        apID + "#delete",
		"type":      "Delete",
		"actor":     cfg.ID(),
		"to":        []string{Public},
		"cc":        []string{"https://" + cfg.Host + "/followers"},
		"object":    apID,
		"published": p.DeletedAt.String,
	}
}

// Tombstone is what a withdrawn post's AP id resolves to, so a server that
// missed the Delete still learns the Note existed and is gone.
func Tombstone(cfg config.Actor, p *store.Post) map[string]any {
	return map[string]any{
		"@context":   "https://www.w3.org/ns/activitystreams",
		"id":         p.APID.String,
		"type":       "Tombstone",
		"formerType": "Note",
		"deleted":    p.DeletedAt.String,
	}
}

func Marshal(v any) ([]byte, error) {
	return json.Marshal(v)
}
