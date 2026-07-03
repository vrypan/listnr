# listnr — implementation plan

This plan is self-contained: read it together with `DESIGN.md` (the
architecture/feature spec). It describes what is already built, what remains,
and every decision that has already been made — **do not revisit settled
decisions**, implement them as written.

## Project context

- Purpose: give the static blog `blog.vrypan.net` a fediverse presence as the
  single actor `@blog@vrypan.net`. The blog is never modified; listnr is a
  standalone daemon on a VPS at `ap.vrypan.net`.
- Language: Go. SQLite via `modernc.org/sqlite` (pure Go). The project must
  always build with `CGO_ENABLED=0` — **never add a dependency that requires
  cgo** (e.g. do not use `mattn/go-sqlite3`).
- CLI framework: `spf13/cobra`. Config: TOML via `BurntSushi/toml`.
- Single actor only. No multi-actor support anywhere.
- Module: `github.com/vrypan/listnr`.

> **Status:** milestones 1 and 2 are DONE (see the packages below plus
> `internal/httpsig`, `internal/fedi`, `internal/delivery`,
> `internal/server/inbox.go`, and their tests). Implementation continues at
> **milestone 3**. Milestone 2's section is kept for reference — its
> behavior is covered by tests in `internal/httpsig`, `internal/delivery`,
> and `internal/server`; do not change that behavior without failing tests.

## Current state (milestone 1 — done)

```
main.go                      entrypoint
cmd/root.go                  cobra root, -c/--config flag (default listnr.toml)
cmd/serve.go                 `listnr serve`
internal/config/config.go    TOML config, validation, defaults
internal/keys/keys.go        RSA-2048 keypair, actor.pem in data_dir (PKCS#1),
                             PublicPEM() renders SPKI PEM
internal/store/store.go      sql.DB (single conn, WAL, FK on) + full schema
internal/ap/actor.go         actor JSON-LD doc; browser GETs redirect to blog
internal/ap/webfinger.go     webfinger for canonical + host-alias resources
internal/server/server.go    routes; inbox = 202 stub; outbox/followers =
                             count-only OrderedCollections
```

The SQLite schema in `store.go` already has all tables: `posts`, `followers`,
`interactions`, `blocks`, `deliveries`, `actor_cache`. See DESIGN.md for
columns. Extend with `ALTER TABLE`-style additive migrations only if a column
is genuinely missing.

Smoke-tested: webfinger (canonical/alias/404), actor JSON + browser redirect,
empty collections, inbox 202.

## Conventions (apply to all milestones)

- Timestamps: UTC, RFC 3339 / ISO 8601 `YYYY-MM-DDTHH:MM:SSZ`, stored as TEXT.
- All outbound HTTP: `User-Agent: listnr/<version> (+https://ap.vrypan.net/actor)`,
  10 s timeout, follow max 5 redirects, response bodies capped at 1 MiB.
- All inbound POST bodies capped at 1 MiB (`io.LimitReader`).
- AP content type when serving: `application/activity+json; charset=utf-8`.
  When requesting: `Accept: application/activity+json,
  application/ld+json; profile="https://www.w3.org/ns/activitystreams"`.
- Errors from handlers: log with `slog`, return bare status codes; never leak
  internals in bodies.
- Keep the single-writer `db.SetMaxOpenConns(1)`; it is intentional.
- JSON Public collection constant: `https://www.w3.org/ns/activitystreams#Public`.

---

## Milestone 2 — HTTP signatures + inbox (the core)

### 2a. Signing outbound requests (`internal/httpsig`)

Implement draft-cavage HTTP Signatures (the flavor Mastodon uses — do NOT
implement RFC 9421):

- Algorithm: RSA-SHA256 (`algorithm="rsa-sha256"` in the header).
- `keyId` is always `https://ap.vrypan.net/actor#main-key` (build from config,
  never hardcode the hostname).
- For POST (deliveries): signed headers, in this order:
  `(request-target) host date digest content-type`.
  - `Digest: SHA-256=<base64 of sha256(body)>` — compute over the exact bytes
    sent.
  - `Date` in RFC 7231 format (`Mon, 02 Jan 2006 15:04:05 GMT`).
- For GET (fetching remote actors/objects): sign
  `(request-target) host date accept`. Signed GETs are required because many
  instances run in authorized-fetch mode; sign **all** outbound AP fetches.
- Signature header form:
  `Signature: keyId="...",algorithm="rsa-sha256",headers="...",signature="<base64>"`.

### 2b. Verifying inbound requests

On every `POST /inbox`:

1. Read body (1 MiB cap). Parse minimally to get `type`, `actor`, `id`.
2. Require a `Signature` header; parse `keyId`, `headers`, `signature`.
   Unsigned → 401.
3. Require `Digest` to be among the signed headers; recompute SHA-256 over the
   received body and compare. Mismatch → 401.
4. Require `Date` among signed headers; reject if clock skew > 1 hour → 401.
5. Fetch the actor document at `keyId` (strip fragment) — via the actor cache
   (below) — extract `publicKey.publicKeyPem`, verify the signature string.
6. If verification fails with a **cached** key, re-fetch the actor once
   (bypassing cache) and retry — handles key rotation. Still failing → 401.
7. The `actor` field of the activity must match the key owner (same URL, or
   `publicKey.owner`). Mismatch → 401. Exception: `Delete` activities for
   actors that are already gone (fetch returns 410/404) → respond 202 and, if
   we have data for that actor, delete it.
8. Blocked check (see 2e) → 202 (accept and drop silently; do not reveal
   blocks).

### 2c. Actor cache (`actor_cache` table)

`FetchActor(ctx, actorID, bypassCache bool)`:

- Cache hit fresher than 24 h → return cached row.
- Otherwise signed GET, parse: `publicKey.publicKeyPem`, `preferredUsername`,
  `name`, `icon.url`, `inbox`, `endpoints.sharedInbox`.
- Store `handle` as `preferredUsername@<host of actor id>`.
- 404/410 → delete cache row, propagate a "gone" error.
- Add columns `inbox`, `shared_inbox` to `actor_cache` (migration).

### 2d. Inbox dispatch

After verification, dispatch on `type` (all responses 202 unless noted):

| Activity | Handling |
|---|---|
| `Follow` | Object must be our actor id, else 202-and-ignore. Upsert into `followers` (actor id, inbox, sharedInbox from fetched actor). Enqueue an `Accept` addressed to the follower's inbox: `{"@context": as, "id": "https://ap.vrypan.net/activities/<uuid>", "type": "Accept", "actor": <our id>, "object": <the Follow activity verbatim>}`. Re-follow when already following → send Accept again (idempotent). |
| `Undo` (object.type=`Follow`) | Delete follower row for the activity's actor. |
| `Like` | `object` must resolve to one of our posts (see resolution below). Insert into `interactions` kind=`like` (ap_id = activity id; ignore duplicate ap_id). |
| `Announce` | Same, kind=`boost`. |
| `Undo` (object.type=`Like`/`Announce`) | Delete interaction by the inner object's `id`; if the inner object is just a URL string, delete by (actor, kind, post) instead. |
| `Create` (object.type=`Note`) | If `object.inReplyTo` resolves to one of our posts: sanitize `object.content` (see below), fetch actor for name/handle/avatar, insert kind=`reply` with ap_id = the **Note's** id, content_html, published. Otherwise ignore. |
| `Update` (object.type=`Note`) | If we have an interaction with that Note id and the activity actor matches its actor: update content_html/published. |
| `Delete` | If object id (or `object.id`) matches a stored interaction of the same actor → delete it. If the object is the actor itself (actor deleted) → delete all their interactions, follower row, actor_cache row. |
| anything else | 202, log at debug. |

**Post resolution**: an incoming URL/id refers to one of our posts if it
equals `posts.ap_id` (the Note id, `https://ap.vrypan.net/posts/<hash>`) or
`posts.url` (the blog permalink). Implement one helper and use it everywhere.

**HTML sanitization**: use `github.com/microcosm-cc/bluemonday` with
`UGCPolicy()`. Sanitize on write (store clean HTML). Never store or serve
unsanitized remote HTML.

### 2e. Blocks

A `blocks.pattern` matches an actor if:
- pattern equals the full actor id, OR
- pattern (a bare domain like `spam.example`) equals the actor id's host or a
  suffix of it (`sub.spam.example` matches pattern `spam.example` on a dot
  boundary).

Check blocks before dispatch (2b step 8). When a block is added via admin
(milestone 4), also set `hidden=1` on existing interactions from matching
actors — do not delete them.

### 2f. Delivery queue (`internal/delivery`)

- `Enqueue(activityJSON, inboxURL)` inserts a `deliveries` row,
  status=`pending`, `next_attempt_at`=now.
- One background worker goroutine (started by `serve`), tick every 10 s:
  fetch due pending rows (limit 20), attempt each sequentially.
- Attempt = signed POST (2a) with `Content-Type: application/activity+json`.
- 2xx → status `done`.
- 410 → status `failed`, and if the inbox URL belongs to a follower, delete
  that follower (and any other follower rows sharing that inbox).
- Other failure → increment attempts, set `last_error`,
  `next_attempt_at = now + backoff(attempts)` with schedule
  1m, 5m, 30m, 2h, 6h, 24h, 48h; after the 7th failure → status `failed`.
- **Fan-out helper** `FanOut(activityJSON)`: gather follower inboxes, prefer
  `shared_inbox` when non-empty, dedupe URLs, `Enqueue` each once.
- Keep `done`/`failed` rows 30 days (cleanup in the same worker, hourly).

### Milestone 2 acceptance

- `go vet ./...` and `go test ./...` pass; `CGO_ENABLED=0 go build ./...` works.
- Unit tests: signature sign→verify round-trip; digest mismatch rejected;
  expired date rejected; each inbox activity type against an in-memory store
  with a fake actor-fetcher; block matching table test; backoff schedule.
- Integration-style test with `httptest`: a fake remote instance follows,
  receives a signed `Accept`, likes, replies, undoes — assert DB state.

---

## Milestone 3 — feed poller + publishing (`internal/feed`, `internal/publish`)

### Feed polling

- Library: `github.com/mmcdole/gofeed` (handles RSS and Atom).
- Poll every `feed.poll_interval` (default 5m) with `If-None-Match` /
  `If-Modified-Since` (store etag/last-modified in a new 1-row `state` table:
  `state(key TEXT PRIMARY KEY, value TEXT)`).
- Item identity = feed GUID if present, else the item link. Stored in
  `posts.guid`.

### First run (empty `posts` table)

- Take the feed's items sorted newest-first. The first `feed.backfill`
  (default 20) items: insert as posts **with** `ap_id` (they appear in the
  outbox) but **do not fan out** any activity.
- All remaining items: insert with `ap_id = NULL` — "seen, never federated".
  These never get activities and never appear in the outbox, but the poller
  will never mistake them for new posts.

### Steady state, per poll

- Unknown GUID → new post: insert, build `Create(Note)`, `FanOut`.
- Known GUID with changed `content_hash` (sha256 of title+summary+link) and
  non-NULL `ap_id` → send `Update(Note)` with `"updated"` set; bump
  `updated_at`. Posts with NULL `ap_id` are never updated/announced.
- Items missing from the feed → do nothing (feeds truncate).

### Note construction (settled format — implement exactly)

- Note id (`posts.ap_id`): `https://ap.vrypan.net/posts/<first 16 hex chars of
  sha256(guid)>` (host from config). Deterministic, stable across restarts.
- Object:

```json
{
  "id": "<ap_id>",
  "type": "Note",
  "attributedTo": "<actor id>",
  "to": ["https://www.w3.org/ns/activitystreams#Public"],
  "cc": ["<actor id>/followers... i.e. https://ap.vrypan.net/followers"],
  "published": "<item pubdate, RFC3339 UTC>",
  "url": "<blog permalink>",
  "content": "<p><strong>{title}</strong></p>{summary}<p><a href=\"{url}\">{url}</a></p>"
}
```

- `{summary}`: the feed item's summary/description, sanitized with bluemonday
  UGC, truncated to ~500 visible characters at a word boundary with `…`
  (truncate the text, then re-wrap in `<p>`; do not truncate mid-tag).
- `Create` activity: id = `<ap_id>#create` (deterministic), actor/to/cc same
  as the Note, `object` = the Note inline.
- `Update` activity: id = `<ap_id>#update-<unix ts>`, object = full new Note
  with `"updated"` timestamp.

### Endpoints to finish

- `GET /posts/{id}`: serve the stored Note JSON for non-NULL `ap_id` posts;
  404 otherwise; browsers (no AP Accept) → 302 to the post's blog URL.
- `GET /outbox`: keep `totalItems`; add `first` page
  (`/outbox?page=1`, 20 `Create` activities per page, newest first,
  `OrderedCollectionPage` with `next` when more remain).
- `GET /followers`: count-only stays (settled decision — no member listing
  publicly).
- `POST /admin/poll` → trigger an immediate poll (used by CLI and later as a
  deploy webhook).

### Milestone 3 acceptance

- Tests: first-run backfill split (backfill window federated, older marked
  seen); new-item fan-out enqueues one delivery per distinct shared inbox;
  edit detection sends Update; truncation never produces invalid HTML.
- Manual: run against the real feed URL with a temp DB; verify outbox JSON.

---

## Milestone 4 — interactions API + admin API + CLI

### Public interactions API

`GET /api/interactions?url=<post permalink>`:

- Look up post by `posts.url`; unknown → 200 with empty payload (not 404 —
  the widget runs on every page, including never-federated posts).
- Response (CORS `Access-Control-Allow-Origin: *`,
  `Cache-Control: public, max-age=60`):

```json
{
  "post": "<url>",
  "fediverse_url": "<ap_id or null>",
  "likes": 3,
  "boosts": 1,
  "replies": [
    {
      "author": {"name": "...", "handle": "user@host", "url": "<actor id>",
                 "avatar": "<icon url>"},
      "content_html": "<sanitized html>",
      "published": "2026-07-04T10:00:00Z",
      "url": "<the reply Note's id>"
    }
  ]
}
```

- Excludes `hidden=1`. Replies sorted oldest first.

### Admin API

- All under `/admin/`, require `Authorization: Bearer <admin.token>`;
  constant-time compare (`crypto/subtle`). If token is empty in config,
  every `/admin/` request → 404.
- Endpoints (JSON in/out) as listed in DESIGN.md:
  replies list/hide/unhide/delete, blocks list/add/remove (add also hides
  matching existing interactions), followers list/remove, stats
  (followers, federated posts, interactions by kind, pending deliveries),
  poll trigger.
- IDs in admin routes are the `interactions.id` / `followers.id` integers.

### CLI (client mode)

- Every subcommand except `serve` is an HTTP client for the admin API.
- Server address + token from `~/.config/listnr/cli.toml`
  (`server = "https://ap.vrypan.net"`, `token = "..."`), overridable with
  `--server` / `--token` flags.
- Commands (thin wrappers, table output via `text/tabwriter`):
  `replies list [--post URL] [--hidden]`, `replies hide|unhide|delete <id>`,
  `block list|add|rm <pattern>`, `followers list`, `followers rm <id>`,
  `stats`, `poll`.

### Milestone 4 acceptance

- Tests: auth (missing/wrong/right token; disabled when unset), interactions
  payload shape, hidden exclusion, block-add hides existing interactions.

---

## Milestone 5 — polish & deploy (do last, keep small)

- `listnr version` (ldflags-injected version string).
- Graceful shutdown (context through worker + http.Server.Shutdown).
- `docs/widget.js` + `docs/widget.md`: a dependency-free JS snippet that
  fetches `/api/interactions?url=` for `location.href` (strip query/fragment)
  and renders a comments section; escape everything except `content_html`
  (already sanitized server-side).
- `deploy/listnr.service` systemd unit (Restart=always, DynamicUser=yes,
  StateDirectory=listnr) + `deploy/README.md`: build
  (`CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build`), Caddy/nginx reverse
  proxy for `ap.vrypan.net`, and the Cloudflare redirect rule
  `vrypan.net/.well-known/webfinger*` → 302
  `https://ap.vrypan.net/.well-known/webfinger` (preserve query string).

## Gotchas the implementer must not "fix"

- Webfinger must keep answering for BOTH `acct:blog@vrypan.net` and
  `acct:blog@ap.vrypan.net`, always with subject `acct:blog@vrypan.net` —
  this asymmetry is required for the custom-domain handle; it is not a bug.
- The actor `url` field points at the blog, not at ap.vrypan.net —
  intentional.
- Inbox responses are 202 even for ignored/blocked activities — never 4xx on
  semantic grounds, only on auth/signature failures.
- Followers collection stays count-only. Outbox history beyond backfill stays
  absent. Both intentional.
- Never fan out during first-run backfill.
- `db.SetMaxOpenConns(1)` stays.
- Additive schema migrations only; never drop/rewrite existing tables.
