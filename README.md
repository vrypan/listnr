# listnr

listnr gives a static blog a Fediverse presence.

It runs as a single ActivityPub actor, polls the blog's RSS/Atom feed for new
posts, announces those posts to followers, receives replies/likes/boosts, and
exposes a small API that a static blog can use to render Fediverse interactions.

The blog itself stays static. listnr runs separately, usually on a VPS behind
TLS.

## Build

```sh
make build
```

For a Linux VPS build from macOS:

```sh
make build-linux
```

These targets produce CGO-free, trimmed release binaries with symbol and
DWARF debugging data removed. For an otherwise equivalent binary that keeps
debugging information, use `make build-debug` or `make build-linux-debug`.

The Makefile derives the version from Git and embeds the commit and commit
timestamp. Tagged builds report the tag; later builds report the tag plus the
number of commits and abbreviated commit hash. A modified checkout has a
`-dirty` suffix. For example:

```text
v0.1.0
v0.1.0-3-g98e6d02
v0.1.0-3-g98e6d02-dirty
```

Inspect a binary with:

```sh
./listnr version
./listnr version --json
```

Plain `go build` also works. Such builds use the VCS metadata embedded by the
Go toolchain and report a `dev-<commit>` version.

Releases use annotated semantic-version tags. Minor versions add compatible
features, patch versions contain compatible fixes, and major versions are
reserved for incompatible configuration, CLI, or persistent-data changes.

GoReleaser builds release archives for macOS ARM64 and Linux AMD64/ARM64.
Validate the release configuration or build all release artifacts locally
with:

```sh
make release-check
make release-snapshot
```

GitHub Actions tests and snapshot-builds all three targets on pull requests
and pushes to `main`. To publish, push a `v*` tag and manually run the Release
workflow for that tag. It creates a GitHub Release containing one `.tar.gz`
archive per target plus `checksums.txt`:

```sh
git tag -a v0.2.0 -m "Release v0.2.0"
git push origin v0.2.0
gh workflow run release.yml --ref v0.2.0
```

The workflows pin the official GoReleaser action v7.2.3 and GoReleaser
v2.17.0. Release binaries retain the existing version, commit, and commit-time
metadata reported by `listnr version`.

## Configure

Create `listnr.toml`:

```toml
[actor]
username = "blog"
domain   = "vrypan.net"
host     = "ap.vrypan.net"
type     = "Service"
name     = "vrypan.net blog"
summary  = "Posts from vrypan.net"
icon     = "https://blog.vrypan.net/avatar.png"
header   = "https://blog.vrypan.net/header.jpg"
blog_url = "https://blog.vrypan.net"
also_known_as = ["https://mastodon.example/@vrypan"]

[[actor.fields]]
name  = "Website"
value = "<a href=\"https://blog.vrypan.net\" rel=\"me\">blog.vrypan.net</a>"

[[actor.fields]]
name  = "RSS"
value = "<a href=\"https://blog.vrypan.net/index.xml\">Feed</a>"

[[actor.tags]]
name = "#blogging"
href = "https://mastodon.social/tags/blogging"

[actor.extra]
discoverable = true
indexable = true
manuallyApprovesFollowers = false

[feed]
url           = "https://blog.vrypan.net/index.xml"
poll_interval = "5m"
backfill      = 20

[server]
listen       = "127.0.0.1:8420"
data_dir     = "/var/lib/listnr"
log_requests = false

[admin]
token = "change-me"
```

Important fields:

- `actor.domain` is the handle domain. With the example above, the actor is
  `@blog@vrypan.net`.
- `actor.host` is where listnr is served. With the example above, ActivityPub
  endpoints live under `https://ap.vrypan.net`.
- `actor.type` controls the ActivityPub actor type. Use `Service` to signal an
  automated account to Mastodon-compatible servers; the default is `Person`.
- `actor.icon` is the profile avatar. `actor.header` is the optional profile
  banner/header image.
- `actor.fields` are Mastodon-style profile fields rendered as
  `PropertyValue` attachments. `value` may contain HTML.
- `actor.also_known_as` and `actor.tags` are optional profile aliases and
  hashtags. Support varies by Fediverse server.
- `actor.extra` is an advanced escape hatch: keys under `[actor.extra]` are
  copied directly into the actor JSON. This lets you add Fediverse extensions
  without recompiling listnr. Use it carefully, because these values are
  emitted as-is.
- `server.data_dir` stores the SQLite database and RSA keypair. The keypair is
  generated automatically on first run.
- `server.log_requests` enables HTTP access logs when set to `true`. It logs
  method, path, status, bytes, duration, remote address, and user agent.
- If `admin.token` is empty, the admin API is disabled.

## Run

```sh
./listnr serve -c listnr.toml
```

On startup, listnr:

- creates or opens the SQLite database;
- creates or loads `actor.pem`;
- starts the delivery queue worker;
- starts the feed poller;
- serves ActivityPub, public API, and admin API routes.

The startup log includes the application version, source commit, and database
schema version. Database migrations are numbered, applied transactionally,
and recorded in the `schema_migrations` table.

Logs are written to stderr. Under systemd, they are captured by journald. HTTP
request logging is disabled by default; enable `[server] log_requests = true`
if you want access-style request logs from the daemon itself.

Public endpoints include:

- `/.well-known/webfinger`
- `/actor`
- `/inbox`
- `/outbox`
- `/followers`
- `/posts/{id}`
- `/api/interactions?url=<post-url>`

## Admin CLI

Administrative commands talk to the admin API. `listnr version` is local
unless `--remote` is supplied.

Create `~/.config/listnr/cli.toml`:

```toml
server = "https://ap.vrypan.net"
token = "change-me"
```

You can also pass `--server` and `--token` on the command line.

Common commands:

```sh
listnr stats
listnr version --remote
listnr refresh   # tell the server to fetch the RSS feed now (alias: poll)

listnr replies list
listnr replies list --post https://blog.vrypan.net/post/
listnr replies list --hidden
listnr replies hide 123
listnr replies unhide 123
listnr replies delete 123

listnr block list
listnr block add spam.example
listnr block add https://bad.example/users/spammer
listnr block rm spam.example

listnr followers list
listnr followers rm 42

listnr posts list
listnr posts list --limit 20 --offset 20
listnr posts delete 42

listnr actor publish

listnr deliveries list
listnr deliveries list --status failed
listnr deliveries retry 17
listnr deliveries retry-failed
listnr deliveries delete 17

listnr version
listnr version --json
```

`listnr version` describes the local binary. `listnr version --remote` reads
the authenticated admin API and describes the daemon currently running on the
configured server. `listnr stats` also includes the daemon build and database
schema versions.

## Backup and Restore

Create a backup from a running remote daemon:

```sh
listnr export -o listnr-backup.tar.gz
```

This uses the server and token from `~/.config/listnr/cli.toml`, sends an
authenticated `POST /admin/export` over HTTPS, and downloads a consistent
SQLite snapshot. To export directly from local files instead:

```sh
listnr export --local -c /etc/listnr/listnr.toml -o listnr-backup.tar.gz
```

Backups are portable gzip-compressed tar archives containing the database,
the actor's private key, the exact TOML configuration, and a JSON manifest
with versions, actor identity, key fingerprint, sizes, and SHA-256 checksums.
They are deliberately unencrypted. Store them with the same care as the
private key and admin token. Encryption can be added by the administrator
without changing the backup format:

```sh
listnr export -o - | age -r age1example... > listnr-backup.tar.gz.age
age -d listnr-backup.tar.gz.age | listnr import - -c /etc/listnr/listnr.toml
```

Imports are local-only. Stop the daemon first, copy the archive to the new
server, and run:

```sh
sudo systemctl stop listnr
sudo listnr import listnr-backup.tar.gz -c /etc/listnr/listnr.toml
sudo systemctl start listnr
```

The import verifies archive paths, checksums, the RSA key fingerprint, SQLite
integrity and schema compatibility, and actor identity before replacing any
runtime files. An existing destination config is retained by default, which
allows its `server.data_dir` and listen address to differ on the new server.
Use `--replace-config` to install the exact archived config. If the config is
missing, it is restored automatically.

The previous database, key, WAL files, and any replaced config are retained
under `server.data_dir/pre-import-<timestamp>-<suffix>/`. The daemon and importer use
the same nonblocking lock, so an import fails while the daemon is running.

Restoring preserves the ActivityPub actor only when `actor.host`, username,
and handle domain remain unchanged. Moving the actor to a different public
host requires an ActivityPub `Move`; changing the config during restore is
not sufficient.

## Feed Behavior

On the first run, listnr imports the newest `feed.backfill` items as
federated history. These appear in the outbox, but listnr does not fan them
out to followers.

Older feed items are stored as seen-only rows. They do not appear in the
outbox and are never announced later just because they were already present in
the feed.

After the first run:

- unknown feed items become new ActivityPub `Note`s and are announced with
  `Create`;
- changed feed items whose posts were federated are announced with `Update`;
- items missing from the feed are ignored because feeds often truncate.

## The Delivery Queue

Every outbound activity is queued and delivered by a background worker with
exponential backoff. A delivery is in one of three states:

| Status | Meaning |
|---|---|
| `pending` | waiting for its next attempt, or being sent right now |
| `done` | accepted by the remote inbox |
| `failed` | terminal — the retry budget was exhausted, or the inbox is gone |

```sh
listnr deliveries list                  # newest first
listnr deliveries list --status failed
listnr deliveries retry 17              # requeue one failed delivery
listnr deliveries retry-failed          # requeue all of them
listnr deliveries delete 17             # drop a failed or done row
```

Listings show the activity's type and id but never its JSON payload: outbound
activities can quote inbound-derived content, so the document itself stays out
of admin responses and logs.

Retrying resets the attempt counter to zero. That is deliberate — an operator
retrying after fixing DNS or a TLS certificate is asking for a fresh retry
budget, the original one having been spent against a broken remote.

Only `failed` rows can be retried, and only `failed` or `done` rows can be
deleted. Pending rows are refused with a `409`: the worker may already be
sending one, and removing the row would not stop the request in flight. Wait
for it to settle into `done` or `failed` first.

Finished rows are cleaned up automatically after 30 days; manual deletion is
for tidying specific entries, not routine maintenance. Do not edit the SQLite
database directly — use these commands, so the worker sees consistent state.

## Updating the Actor Profile

Changing the actor's name, summary, icon, header, fields, tags, aliases, or
account flags changes the document served at `/actor`, but existing followers
keep whatever profile their server cached. Most fediverse servers refresh a
profile when they receive an actor `Update`.

Publishing one is an explicit three-step operation:

1. edit `[actor]` in `listnr.toml`;
2. restart the daemon so it loads the new values;
3. run `listnr actor publish`.

```sh
listnr actor publish
```

The command sends no data of its own — the server's TOML config is the only
source of profile content, and neither profile fields nor key material can be
injected through the admin API. The daemon builds its current actor document,
fingerprints it, and queues an `Update` carrying the **full** document to every
follower inbox, recording the fingerprint in the same transaction.

It is idempotent by fingerprint: running it again without a config change
prints `profile unchanged` and queues nothing. Any change to any actor
property — including the public key — changes the fingerprint and publishes.

listnr never publishes the profile automatically, including on startup. An
automatic announcement on every restart would be noisy and would make a
mistaken config change harder to contain.

## Deleting a Post

Feeds truncate, so an item disappearing from the feed is never treated as a
deletion. Withdrawing a post is always an explicit administrative act:

```sh
listnr posts list          # find the numeric store id
listnr posts delete 42
```

`listnr posts delete` records a deletion timestamp and queues an ActivityPub
`Delete` to every follower inbox in one database transaction, so the post can
never be marked deleted without its deliveries also being queued.

The command is idempotent. Repeating it prints `already deleted` and queues
nothing, so a retrying script cannot flood followers with duplicate `Delete`
activities.

Afterwards:

- the post's ActivityPub URL answers `410 Gone` with a `Tombstone` — the id
  keeps resolving, so servers that missed the `Delete` still learn it is gone;
- browsers visiting the same URL get a plain `410 Gone` page instead of the
  instance chooser;
- the post leaves the outbox total and its pages;
- `/api/interactions` reports zero counts and a null `fediverse_url`.

Replies, likes, and boosts already stored for the post are kept in the
database for moderation and audit, and the row itself is not purged.

## Blog Widget

Use `docs/widget.js` to render Fediverse interactions on static post pages:

```html
<script src="/path/to/widget.js" data-endpoint="https://ap.vrypan.net"></script>
```

To pin the post URL explicitly:

```html
<script
  src="/path/to/widget.js"
  data-endpoint="https://ap.vrypan.net"
  data-url="https://blog.vrypan.net/2026/07/example/"></script>
```

The widget fetches:

```text
https://ap.vrypan.net/api/interactions?url=<post-url>
```

It uses `data-url` when present and falls back to the current page URL. It
strips query strings and fragments before making the request. Replies are
sanitized on the server before they are stored and served.

## HTTP Caching

Fediverse servers dereference the same actor, post, outbox, and followers
documents repeatedly. Every ActivityPub JSON response carries a strong `ETag`
computed over the exact bytes sent, so a repeat fetch of unchanged state costs
a bodyless `304` instead of a full document.

The policy is `Cache-Control: public, max-age=0, must-revalidate`: a cache may
store the document but must revalidate before reusing it. listnr deliberately
does not promise freshness for a fixed interval — a post can be updated or
withdrawn, and the profile can change, at any moment.

The ETag changes whenever the visible representation does: an edited profile
or rotated key changes the actor's, an `Update` or a `Tombstone` changes a
post's, a new post changes the outbox's, and a new follower changes the
followers collection's.

`/actor` and `/posts/{id}` answer differently to browsers and to fediverse
software, so they also send `Vary: Accept`.

This reduces bandwidth and origin work; it does not eliminate origin requests,
since every revalidation is still a request. Cloudflare configuration remains
operator-owned — see Cloudflare-Cache.md.

## Deployment

A typical deployment is:

1. Build the Linux binary.
2. Install it as `/usr/local/bin/listnr`.
3. Put `listnr.toml` in `/etc/listnr/listnr.toml`.
4. Run it with the systemd unit in `deploy/listnr.service`.
5. Reverse proxy `https://ap.vrypan.net` to `127.0.0.1:8420`.
6. Add a redirect rule for the handle domain:

```text
vrypan.net/.well-known/webfinger* -> 302 https://ap.vrypan.net/.well-known/webfinger
```

Preserve the query string in that redirect.

See `deploy/README.md` for a compact deployment checklist.

## Notes

- listnr is intentionally single-actor.
- The followers collection is count-only.
- Inbox handlers return `202 Accepted` for ignored or blocked activities.
- The project uses `modernc.org/sqlite`, so builds must keep working with
  `CGO_ENABLED=0`.

## License

MIT. See `LICENSE`.
