package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadAllowsActorExtraButRejectsOtherUnknownKeys(t *testing.T) {
	path := writeConfig(t, `
[actor]
username = "blog"
domain = "vrypan.net"
host = "ap.vrypan.net"
type = "Service"
blog_url = "https://blog.vrypan.net"

[actor.extra]
discoverable = true
manuallyApprovesFollowers = false

[feed]
url = "https://blog.vrypan.net/index.xml"

[admin]
token = "secret"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Actor.Type != "Service" {
		t.Fatalf("actor type = %q, want Service", cfg.Actor.Type)
	}
	if cfg.Actor.Extra["discoverable"] != true {
		t.Fatalf("actor extra not decoded: %#v", cfg.Actor.Extra)
	}

	bad := writeConfig(t, `
[actor]
username = "blog"
domain = "vrypan.net"
host = "ap.vrypan.net"
blog_url = "https://blog.vrypan.net"
typo = true

[feed]
url = "https://blog.vrypan.net/index.xml"

[admin]
token = "secret"
`)
	if _, err := Load(bad); err == nil || !strings.Contains(err.Error(), "unknown config keys") {
		t.Fatalf("Load accepted unknown key, err=%v", err)
	}
}

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "listnr.toml")
	if err := os.WriteFile(path, []byte(strings.TrimSpace(content)), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
