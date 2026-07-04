// Package fedi is the outbound side of federation: signed HTTP fetches and
// the remote-actor cache.
package fedi

import (
	"context"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/vrypan/listnr/internal/httpsig"
	"github.com/vrypan/listnr/internal/safehttp"
	"github.com/vrypan/listnr/internal/store"
)

const (
	acceptAP     = `application/activity+json, application/ld+json; profile="https://www.w3.org/ns/activitystreams"`
	maxBody      = 1 << 20
	cacheMaxAge  = 24 * time.Hour
	UserAgent    = "listnr/0.1 (+https://github.com/vrypan/listnr)"
	fetchTimeout = 10 * time.Second
)

// ErrGone means the remote object no longer exists (HTTP 404/410).
var ErrGone = errors.New("remote object gone")

// Actor is a remote actor's profile, as much of it as listnr needs.
type Actor struct {
	ID           string
	PublicKeyPEM string
	Name         string
	Handle       string
	IconURL      string
	Inbox        string
	SharedInbox  string
}

type Client struct {
	http  *http.Client
	st    *store.Store
	key   *rsa.PrivateKey
	keyID string
}

// NewClient builds an outbound federation client. Pass httpClient to override
// the transport (tests use a loopback-capable one); nil selects the default
// SSRF-guarded client.
func NewClient(st *store.Store, key *rsa.PrivateKey, keyID string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = safehttp.Client(fetchTimeout)
	}
	return &Client{
		http:  httpClient,
		st:    st,
		key:   key,
		keyID: keyID,
	}
}

// Get performs a signed ActivityPub GET. All outbound fetches are signed so
// that authorized-fetch ("secure mode") instances answer us.
func (c *Client) Get(ctx context.Context, rawURL string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Accept", acceptAP)
	req.Header.Set("User-Agent", UserAgent)
	if err := httpsig.Sign(req, nil, c.key, c.keyID); err != nil {
		return nil, 0, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return body, resp.StatusCode, nil
}

// FetchActor returns a remote actor, from cache when fresh. bypassCache
// forces a re-fetch (used once on signature failure, to pick up rotated
// keys). Returns ErrGone when the actor no longer exists; the cache row is
// dropped in that case.
func (c *Client) FetchActor(ctx context.Context, actorID string, bypassCache bool) (*Actor, error) {
	actorID = stripFragment(actorID)

	if !bypassCache {
		cached, err := c.st.GetCachedActor(actorID)
		if err != nil {
			return nil, err
		}
		if cached != nil && time.Since(cached.FetchedAt) < cacheMaxAge {
			return fromCached(cached), nil
		}
	}

	body, status, err := c.Get(ctx, actorID)
	if err != nil {
		return nil, fmt.Errorf("fetch actor %s: %w", actorID, err)
	}
	if status == http.StatusNotFound || status == http.StatusGone {
		c.st.DeleteCachedActor(actorID)
		return nil, fmt.Errorf("actor %s: %w", actorID, ErrGone)
	}
	if status < 200 || status > 299 {
		return nil, fmt.Errorf("fetch actor %s: HTTP %d", actorID, status)
	}

	actor, err := parseActor(body)
	if err != nil {
		return nil, fmt.Errorf("parse actor %s: %w", actorID, err)
	}
	if stripFragment(actor.ID) != actorID {
		return nil, fmt.Errorf("actor document id %q does not match fetched URL %q", actor.ID, actorID)
	}
	if err := c.st.UpsertCachedActor(&store.CachedActor{
		ID: actor.ID, PublicKeyPEM: actor.PublicKeyPEM, Name: actor.Name,
		Handle: actor.Handle, IconURL: actor.IconURL,
		Inbox: actor.Inbox, SharedInbox: actor.SharedInbox,
	}); err != nil {
		return nil, err
	}
	return actor, nil
}

func fromCached(a *store.CachedActor) *Actor {
	return &Actor{
		ID: a.ID, PublicKeyPEM: a.PublicKeyPEM, Name: a.Name, Handle: a.Handle,
		IconURL: a.IconURL, Inbox: a.Inbox, SharedInbox: a.SharedInbox,
	}
}

func parseActor(body []byte) (*Actor, error) {
	var doc struct {
		ID                string          `json:"id"`
		PreferredUsername string          `json:"preferredUsername"`
		Name              string          `json:"name"`
		Inbox             string          `json:"inbox"`
		Icon              json.RawMessage `json:"icon"`
		PublicKey         struct {
			PublicKeyPem string `json:"publicKeyPem"`
		} `json:"publicKey"`
		Endpoints struct {
			SharedInbox string `json:"sharedInbox"`
		} `json:"endpoints"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, err
	}
	if doc.ID == "" || doc.Inbox == "" {
		return nil, errors.New("actor document missing id or inbox")
	}
	handle := ""
	if u, err := url.Parse(doc.ID); err == nil && doc.PreferredUsername != "" {
		handle = doc.PreferredUsername + "@" + u.Host
	}
	return &Actor{
		ID:           doc.ID,
		PublicKeyPEM: doc.PublicKey.PublicKeyPem,
		Name:         doc.Name,
		Handle:       handle,
		IconURL:      parseIcon(doc.Icon),
		Inbox:        doc.Inbox,
		SharedInbox:  doc.Endpoints.SharedInbox,
	}, nil
}

// parseIcon tolerates the shapes icons appear in: an Image object, a bare
// URL string, or an array of either.
func parseIcon(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var obj struct {
		URL string `json:"url"`
	}
	if json.Unmarshal(raw, &obj) == nil && obj.URL != "" {
		return obj.URL
	}
	var arr []json.RawMessage
	if json.Unmarshal(raw, &arr) == nil && len(arr) > 0 {
		return parseIcon(arr[0])
	}
	return ""
}

func stripFragment(u string) string {
	if i := strings.IndexByte(u, '#'); i >= 0 {
		return u[:i]
	}
	return u
}
