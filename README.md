# listnr

listnr gives a static blog a Fediverse presence.

It runs as a single ActivityPub actor, polls the blog's RSS/Atom feed for new
posts, announces those posts to followers, receives replies/likes/boosts, and
exposes a small API that a static blog can use to render Fediverse interactions.

The blog itself stays static. listnr runs separately, usually on a VPS behind
TLS.

## Build

```sh
CGO_ENABLED=0 go build -o listnr .
```

For a Linux VPS build from macOS:

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o listnr .
```

To inject a version string:

```sh
go build -ldflags "-X github.com/vrypan/listnr/cmd.Version=$(git describe --tags --always --dirty)" -o listnr .
```

## Configure

Create `listnr.toml`:

```toml
[actor]
username = "blog"
domain   = "vrypan.net"
host     = "ap.vrypan.net"
name     = "vrypan.net blog"
summary  = "Posts from vrypan.net"
icon     = "https://blog.vrypan.net/avatar.png"
blog_url = "https://blog.vrypan.net"

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

Every command except `serve` talks to the admin API.

Create `~/.config/listnr/cli.toml`:

```toml
server = "https://ap.vrypan.net"
token = "change-me"
```

You can also pass `--server` and `--token` on the command line.

Common commands:

```sh
listnr stats
listnr poll

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

listnr version
```

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

## Blog Widget

Use `docs/widget.js` to render Fediverse interactions on static post pages:

```html
<script src="/path/to/widget.js" data-endpoint="https://ap.vrypan.net"></script>
```

The widget fetches:

```text
https://ap.vrypan.net/api/interactions?url=<current-page-url>
```

It strips query strings and fragments from the current page URL before making
the request. Replies are sanitized on the server before they are stored and
served.

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
