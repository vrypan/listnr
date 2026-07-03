# Deploying listnr

Build a static Linux binary:

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "-X github.com/vrypan/listnr/cmd.Version=$(git describe --tags --always --dirty)" -o listnr .
```

Install it as `/usr/local/bin/listnr`, put `listnr.toml` in `/etc/listnr/`,
and use `deploy/listnr.service` as the systemd unit. With `DynamicUser=yes`
and `StateDirectory=listnr`, set:

```toml
[server]
data_dir = "/var/lib/listnr"
```

Reverse proxy `https://ap.vrypan.net` to the configured listen address, for
example `127.0.0.1:8420`.

For the handle domain, add a Cloudflare redirect rule:

```text
vrypan.net/.well-known/webfinger* -> 302 https://ap.vrypan.net/.well-known/webfinger
```

Preserve the query string so `resource=acct:blog@vrypan.net` reaches listnr.

