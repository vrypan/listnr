# Deploying listnr

Build a static Linux binary:

```sh
make build-linux
./listnr version
```

`build-linux` produces a CGO-free, stripped release binary. Use
`make build-linux-debug` when symbols and DWARF debugging information are
needed for diagnosis.

For a release, build from an annotated semantic-version tag such as `v0.1.0`.
The Makefile embeds the tag, source commit, and commit timestamp. A build from
an uncommitted checkout is marked with a `-dirty` suffix.

After pushing a `v*` tag, manually run the GitHub Actions Release workflow for
that tag. GoReleaser creates macOS ARM64 and Linux AMD64/ARM64 archives,
generates SHA-256 checksums, and publishes them to the matching GitHub
Release. Run `make release-snapshot` before tagging to reproduce the packaging
locally without publishing.

Install it as `/usr/local/bin/listnr`, put `listnr.toml` in `/etc/listnr/`,
and use `deploy/listnr.service` as the systemd unit. With `DynamicUser=yes`
and `StateDirectory=listnr`, set:

```toml
[server]
data_dir = "/var/lib/listnr"
```

Reverse proxy `https://ap.vrypan.net` to the configured listen address, for
example `127.0.0.1:8420`.

After restarting the service, verify the deployed binary from the laptop:

```sh
listnr version --remote
```

The daemon also writes its version, commit, and database schema version to the
startup journal.

Create regular backups from another machine over the TLS endpoint:

```sh
listnr export -o listnr-backup-$(date -u +%Y%m%d).tar.gz
```

The archive is not encrypted and contains `actor.pem` and the admin token.
Protect it or pipe `listnr export -o -` into an encryption tool. To restore on
a replacement server, install the binary and destination config, stop the
service, then run:

```sh
sudo systemctl stop listnr
sudo listnr import listnr-backup.tar.gz -c /etc/listnr/listnr.toml
sudo systemctl start listnr
```

Keep the same public `actor.host`, username, and handle domain. The destination
config is preserved unless `--replace-config` is supplied, so deployment-only
settings can differ. The import reports the rollback directory containing the
previous instance files.

For the handle domain, add a Cloudflare redirect rule:

```text
vrypan.net/.well-known/webfinger* -> 302 https://ap.vrypan.net/.well-known/webfinger
```

Preserve the query string so `resource=acct:blog@vrypan.net` reaches listnr.
