# Deploying listnr

Build a static Linux binary:

```sh
make build-linux
./listnr version
```

For a release, build from an annotated semantic-version tag such as `v0.1.0`.
The Makefile embeds the tag, source commit, and commit timestamp. A build from
an uncommitted checkout is marked with a `-dirty` suffix.

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

For the handle domain, add a Cloudflare redirect rule:

```text
vrypan.net/.well-known/webfinger* -> 302 https://ap.vrypan.net/.well-known/webfinger
```

Preserve the query string so `resource=acct:blog@vrypan.net` reaches listnr.
