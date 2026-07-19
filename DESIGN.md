# listnr — design

A small ActivityPub bridge that gives a static blog a fediverse presence.
Single actor: `@blog@vrypan.net`. The blog itself (blog.vrypan.net) stays
fully static and is never touched.

## Topology

- **listnr** runs on a VPS as a single static Go binary, listening on
  `ap.vrypan.net` (behind Caddy/nginx for TLS, or Cloudflare).
- **vrypan.net** (static S3 site behind Cloudflare) gets one Cloudflare
  redirect rule: `vrypan.net/.well-known/webfinger*` → 302 →
  `https://ap.vrypan.net/.well-known/webfinger` (query string preserved).
- listnr **polls the blog's RSS/Atom feed** to detect new/updated posts.
- Blog pages embed a small JS snippet that fetches fediverse
  replies/likes/boosts from listnr's public API and renders them as comments.

## Stack

- Go, single binary, `CGO_ENABLED=0` (cross-compiles from macOS).
- SQLite via `modernc.org/sqlite` (pure Go, no cgo).
- RSA keypair generated on first run, stored in the data directory.
- Config: one TOML file.
- Release identity: semantic-version Git tags with commit metadata embedded by
  the build; plain Go builds fall back to Go's VCS build information.
- Database changes: numbered transactional migrations recorded in
  `schema_migrations`, independently of the application release version.

## HTTP endpoints

### Public (ActivityPub)

| Method | Path | Purpose |
|---|---|---|
| GET | `/.well-known/webfinger?resource=acct:blog@vrypan.net` | JRD doc; `subject: acct:blog@vrypan.net`, `rel=self` link to the actor. Also answers for `acct:blog@ap.vrypan.net` (Mastodon's reverse check). Anything else → 404. |
| GET | `/actor` | Actor document (`Person`, `preferredUsername: blog`, public key, `url: https://blog.vrypan.net`, icon, summary). Content negotiation: browsers get a redirect to the blog. |
| POST | `/inbox` | The only write endpoint. Verifies HTTP Signature, dispatches by activity type (see below). |
| GET | `/outbox` | `OrderedCollection` of past `Create(Note)` activities, paged. |
| GET | `/followers` | `OrderedCollection`; publishes `totalItems`, items pages optional. |
| GET | `/posts/{id}` | Dereferenceable `Note` object for each announced post. Browsers get an interstitial that opens the post on the visitor's own instance (`/authorize_interaction`, instance remembered in localStorage). |

### Public (blog integration)

| Method | Path | Purpose |
|---|---|---|
| GET | `/api/interactions?url={post-url}` | JSON for the JS widget: array of replies (author handle/name/avatar, HTML content, timestamp, link to original) + like count + boost count. CORS `*`. Sends an `ETag` fingerprint of the response plus `Cache-Control: public, max-age=0, s-maxage=30, stale-while-revalidate=300`, so Cloudflare's edge absorbs bursts (see Cloudflare-Cache.md) while browsers revalidate to a `304` and the ETag changes the moment reactions change. Hidden/blocked replies excluded. |

### Admin (bearer-token auth, used by the CLI)

| Method | Path | Purpose |
|---|---|---|
| GET | `/admin/replies` | List replies (filters: post, hidden, since). |
| POST | `/admin/replies/{id}/hide` · `/unhide` | Toggle a reply's visibility. |
| DELETE | `/admin/replies/{id}` | Delete a stored reply. |
| GET/POST/DELETE | `/admin/blocks` | Manage blocklist entries (full actor URL or bare domain). Adding a block hides existing matching interactions. |
| GET | `/admin/followers` | List followers. |
| DELETE | `/admin/followers/{id}` | Force-remove a follower. |
| GET | `/admin/stats` | Application build, database schema version, and counts for followers, posts, interactions, and queue depth. |
| POST | `/admin/poll` | Trigger an immediate feed poll (also the deploy-webhook hook point later). |

## Inbox handling

| Activity | Action |
|---|---|
| `Follow` | Store follower (actor id, inbox, sharedInbox), send `Accept`. |
| `Undo(Follow)` | Remove follower. |
| `Like` / `Announce` on a known post | Store as interaction. |
| `Undo(Like/Announce)` | Remove the stored interaction. |
| `Create(Note)` with `inReplyTo` = a known post URL, Note id, or stored reply Note id | Store as reply (fetch actor profile for name/avatar; sanitize HTML). Replies to stored replies are attached to the original post. |
| `Delete` | Remove matching stored interactions; if actor-level, drop everything from that actor. |
| `Update(Note)` | Update stored reply content. |
| Anything else | 202, ignore. |

Signature verification: HTTP Signatures (draft-cavage), fetch + cache remote
actor public keys, reject unsigned/invalid. Blocked actors/domains rejected
before processing.

## Feed poller & delivery

- Every `poll_interval` (default 5 min): fetch feed with ETag/Last-Modified.
- New item (unknown GUID) → create `Note` (title + summary + permalink;
  configurable template), store, fan out `Create` to follower inboxes.
- Changed item (content hash differs) → `Update(Note)` fan-out.
- Item removed from feed → nothing (feeds truncate); explicit delete via CLI.
- Fan-out uses shared inboxes (one delivery per instance), a persistent queue,
  exponential backoff (up to ~48 h), and drops followers whose inbox returns
  410 Gone repeatedly.
- First run: the `backfill` most recent feed items are imported as history
  (outbox) **without** fan-out; older items are ignored entirely. Items
  already present in the feed but beyond the backfill window are also
  remembered as "seen" so a later poll never mistakes them for new posts.

## SQLite schema (sketch)

```sql
posts        (id, guid UNIQUE, url, title, summary_html, published_at,
              content_hash, ap_id UNIQUE, announced_at, updated_at)
followers    (id, actor_id UNIQUE, inbox, shared_inbox, followed_at)
interactions (id, ap_id UNIQUE, kind CHECK(kind IN ('reply','like','boost')),
              post_id REFERENCES posts, actor_id, actor_handle, actor_name,
              actor_icon_url, content_html, published_at, received_at,
              hidden INTEGER DEFAULT 0)
blocks       (id, pattern, created_at)        -- actor URL or domain
deliveries   (id, activity_json, inbox_url, attempts, next_attempt_at,
              last_error, status)
actor_cache  (actor_id, public_key_pem, name, handle, icon_url, fetched_at)
```

## Config (`listnr.toml`)

```toml
[actor]
username   = "blog"
domain     = "vrypan.net"       # handle domain
host       = "ap.vrypan.net"    # where listnr is served
type       = "Service"          # optional; Service/Application signal automation
name       = "vrypan.net blog"
summary    = "..."
icon       = "https://blog.vrypan.net/avatar.png"
header     = "https://blog.vrypan.net/header.jpg"  # optional profile header
blog_url   = "https://blog.vrypan.net"
also_known_as = ["https://mastodon.example/@vrypan"]

[[actor.fields]]
name  = "Website"
value = "<a href=\"https://blog.vrypan.net\" rel=\"me\">blog.vrypan.net</a>"

[[actor.tags]]
name = "#blogging"
href = "https://mastodon.social/tags/blogging"

[actor.extra]                  # optional raw actor JSON properties
discoverable = true
indexable = true
manuallyApprovesFollowers = false

[feed]
url           = "https://blog.vrypan.net/index.xml"
poll_interval = "5m"
backfill      = 20    # max posts imported as history on first run

[server]
listen    = "127.0.0.1:8420"
data_dir  = "/var/lib/listnr"   # sqlite db + keypair

[admin]
token = "..."                    # bearer token for /admin/* and the CLI
```

## CLI

Same binary; `listnr serve` runs the daemon, everything else is an admin
client (reads server URL + token from `~/.config/listnr/cli.toml` or flags),
so it runs from the laptop against the VPS:

```
listnr serve
listnr replies list [--post URL] [--hidden]
listnr replies hide|unhide|delete <id>
listnr block add|rm|list <actor-or-domain>
listnr followers list [--rm <id>]
listnr stats
listnr refresh       # tell the server to fetch the RSS feed now (alias: poll)
listnr keygen        # (first-run helper, normally automatic)
```

## Out of scope (deliberately)

Posting from the fediverse, client-to-server API, media hosting,
multi-actor support, approval-before-publish moderation queue.
