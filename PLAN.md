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

> **Status:** milestones 1 through 6 are DONE. The remaining work is external
> deployment/operations: configure the real VPS, Cloudflare redirect, and blog
> widget include; then run a production-feed smoke test. The milestone sections
> below are kept as implementation/reference specs — do not change their
> behavior without updating tests and this plan.

## Current state (milestones 1-5 — done)

```
main.go                      entrypoint
cmd/root.go                  cobra root, -c/--config flag (default listnr.toml)
cmd/serve.go                 `listnr serve`, delivery/feed workers,
                             graceful shutdown
cmd/client.go                admin API client commands
cmd/version.go               local/remote `listnr version` with JSON output
cmd/backup.go                remote/local export and local-only import
internal/buildinfo           Git-tag/VCS application version and User-Agent
internal/config/config.go    TOML config, validation, defaults
internal/keys/keys.go        RSA-2048 keypair, actor.pem in data_dir (PKCS#1),
                             parsing, fingerprinting, PublicPEM() SPKI output
internal/backup              versioned archive creation, validation, restore,
                             and rollback retention
internal/instance            daemon/import data-directory lock
internal/store/*.go          sql.DB (single conn, WAL, FK on) + full schema,
                             post/state/admin query helpers
internal/ap/actor.go         actor JSON-LD doc; browser GETs redirect to blog
internal/ap/webfinger.go     webfinger for canonical + host-alias resources
internal/httpsig             draft-cavage HTTP signatures
internal/fedi                signed AP fetches + actor cache
internal/delivery            persistent signed delivery queue
internal/feed                RSS/Atom polling + first-run/steady-state ingest
internal/publish             Note/Create/Update construction
internal/server/*.go         public AP routes, inbox dispatch, interactions API,
                             admin API, authenticated backup export
docs/widget.js               dependency-free blog interactions widget
docs/widget.md               widget usage notes
deploy/listnr.service        systemd unit
deploy/README.md             build/proxy/Cloudflare deployment notes
Makefile                     stripped release and debug local/Linux builds
                             with embedded metadata; GoReleaser validation and
                             snapshot targets
.goreleaser.yml              macOS ARM64 and Linux AMD64/ARM64 release archives
.github/workflows            CI snapshot builds and manually dispatched
                             GitHub Releases
```

The SQLite schema in `store.go` has all runtime tables: `posts`, `followers`,
`interactions`, `blocks`, `deliveries`, `actor_cache`, `state`, and
`schema_migrations`. See DESIGN.md for columns. Every schema change must be a
new numbered migration applied transactionally; never reuse or edit a released
migration number.

Application releases use semantic-version Git tags. `make build` and
`make build-linux` inject the tag-aware `git describe` value, commit, and
commit timestamp. Plain `go build` falls back to Go's embedded VCS metadata.
The local binary reports this through `listnr version`; the running daemon
reports it through its startup log, `/admin/stats`, `listnr stats`, and
`listnr version --remote`. Application versions must never be included in
actor, activity, or object IDs.

GoReleaser v2 produces stripped, reproducible `tar.gz` archives for
`darwin/arm64`, `linux/amd64`, and `linux/arm64`, with the same embedded build
metadata and a SHA-256 checksum manifest. GitHub Actions tests and snapshot
builds these targets for pull requests and `main`. After a `v*` tag is pushed,
the manually dispatched Release workflow publishes its GitHub Release. The
workflow pins GoReleaser v2.17.0 via the official `goreleaser-action` v7.2.3
release.

Verified locally:

- `go test ./...`
- `go vet ./...`
- `CGO_ENABLED=0 go build ./...`
- GoReleaser v2.17.0 `check` and snapshot release for all three targets

Not yet done in this repo session: manual smoke against the real feed URL with
a temporary production-like DB, and live deployment verification.

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

## Milestone 2 — HTTP signatures + inbox (done)

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
| `Create` (object.type=`Note`) | If `object.inReplyTo` resolves to one of our posts or a stored reply on one of our posts: sanitize `object.content` (see below), fetch actor for name/handle/avatar, insert kind=`reply` with ap_id = the **Note's** id, content_html, published. Otherwise ignore. |
| `Update` (object.type=`Note`) | If we have an interaction with that Note id and the activity actor matches its actor: update content_html/published. |
| `Delete` | If object id (or `object.id`) matches a stored interaction of the same actor → delete it. If the object is the actor itself (actor deleted) → delete all their interactions, follower row, actor_cache row. |
| anything else | 202, log at debug. |

**Post resolution**: an incoming URL/id refers to one of our posts if it
equals `posts.ap_id` (the Note id, `https://ap.vrypan.net/posts/<hash>`),
`posts.url` (the blog permalink), or a stored reply's `interactions.ap_id`.
The stored-reply case lets replies-to-replies attach to the original post.
Implement one helper and use it everywhere.

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

## Milestone 3 — feed poller + publishing (done)

Implemented in `internal/feed` and `internal/publish`; wired from
`cmd/serve.go`.

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

### Endpoints

- `GET /posts/{id}`: serve the stored Note JSON for non-NULL `ap_id` posts;
  404 otherwise; browsers (no AP Accept) get an HTML interstitial that
  sends them to the post on their own instance via
  `https://<instance>/authorize_interaction?uri=<note id>` (instance
  remembered in localStorage, auto-redirect once per tab session), with a
  fallback link to the blog post.
- `GET /outbox`: keep `totalItems`; add `first` page
  (`/outbox?page=1`, 20 `Create` activities per page, newest first,
  `OrderedCollectionPage` with `next` when more remain).
- `GET /followers`: count-only stays (settled decision — no member listing
  publicly).
- `POST /admin/poll` → trigger an immediate poll (used by CLI and later as a
  deploy webhook).

### Milestone 3 acceptance

- Tests cover first-run backfill split, edit detection sending `Update`, and
  truncation/sanitization producing wrapped HTML.
- Delivery inbox de-duping is covered by `internal/delivery`'s `FanOut`
  behavior through the store helper.
- Manual real-feed temp-DB smoke remains to be run before production deploy.

---

## Milestone 4 — interactions API + admin API + CLI (done)

Implemented in `internal/server/public.go`, `internal/server/admin.go`, and
`cmd/client.go`.

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
      "url": "<the reply Note's id>",
      "in_reply_to": "<the Note's raw inReplyTo>"
    }
  ]
}
```

- Excludes `hidden=1`. Replies sorted oldest first.
- `in_reply_to` lets the widget thread nested replies: when it equals
  another reply's `url`, the reply is a child of that reply; otherwise
  it is a top-level reply to the post. Replies stored before the column
  existed have `""`.

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
  payload shape, and hidden exclusion. Block matching is covered by existing
  block tests; block-add hide behavior should be kept under test when block
  admin code changes.

---

## Milestone 5 — polish & deploy (done)

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

---

## Milestone 6 — portable backup and restore (done)

- `listnr export` downloads a backup from the authenticated
  `POST /admin/export` endpoint; `--local` snapshots the local instance.
  `--output -` supports administrator-selected encryption pipelines.
- The unencrypted, versioned `.tar.gz` contains a consistent SQLite online
  backup, `actor.pem`, the exact `listnr.toml`, and `manifest.json`. The
  manifest records build/schema versions, actor identity, key fingerprint,
  creation time, and payload SHA-256 checksums and sizes.
- Every admin response, including exports, sends
  `Cache-Control: no-store, private` and `Pragma: no-cache`.
- `listnr import` is local-only. It rejects unsafe or unexpected archive
  entries, checksum/key/config/actor mismatches, corrupt SQLite databases,
  and schemas newer than the importing binary.
- The daemon and importer acquire the same exclusive nonblocking lock in
  `server.data_dir`; imports therefore require the daemon to be stopped.
- Existing destination config is preserved by default. A missing config is
  restored automatically; `--replace-config` explicitly installs the
  archived config. Existing database, WAL, key, and replaced config files are
  retained in `pre-import-<timestamp>-<suffix>` for rollback.
- Restore requires an unchanged actor id and handle. Moving to another public
  actor host remains a separate ActivityPub `Move` operation.
- Tests cover consistent snapshot/restore, actor mismatch, lock exclusion,
  unsafe archive paths, rollback retention, and authenticated no-cache export.

## Milestone 7 — explicit post deletion (done)

- Schema migration 2 adds a nullable `posts.deleted_at`. Existing rows migrate
  in place with a NULL timestamp; nothing is dropped or rewritten.
- `store.WithFanOut` is the shared primitive: it runs a mutation and inserts
  one delivery row per deduplicated follower inbox in a single transaction, so
  a state change and its fan-out commit together or not at all.
- `DELETE /admin/posts/{id}` and `listnr posts delete <id>` withdraw a post.
  The operation is idempotent: a repeat neither moves the timestamp nor
  enqueues a second `Delete`.
- A withdrawn post serves `410 Gone` with a `Tombstone` to ActivityPub clients
  and a plain `410` to browsers, leaves the outbox total and pages, and reports
  empty `/api/interactions` results. Its stored interactions are kept.
- `PostCount` is now explicitly the active federated count; `TotalPostCount`
  remains the all-time ingestion count the poller uses to detect a first run.

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
- A feed omitting an item is NOT a deletion — feeds truncate. Deletion is only
  ever initiated by an authenticated administrator.
- `db.SetMaxOpenConns(1)` stays.
- Additive schema migrations only; never drop/rewrite existing tables.
- The gone-key Delete path (inbox.go) only accepts an actor deleting *itself*
  (object == actor, key host == actor host). Do not loosen it — keyId is
  attacker-controlled, so a broader rule lets anyone purge any actor.
- Outbound federation fetches/deliveries use `safehttp.Client`, which refuses
  to dial private/loopback/link-local/metadata IPs (SSRF guard). Inbox URLs
  are attacker-supplied; do not swap in a plain `http.Client`. Tests pass an
  explicit plain client because they talk to loopback httptest servers.
- Inbox activities are de-duplicated by `id` (seen_activities table) to reject
  replays; ids are marked seen only after a successful dispatch, and pruned
  after ~2h (just past the 1h signature clock-skew window).
