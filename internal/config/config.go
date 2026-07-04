package config

import (
	"fmt"
	"os"
	"time"

	"github.com/BurntSushi/toml"
)

type Config struct {
	Actor  Actor  `toml:"actor"`
	Feed   Feed   `toml:"feed"`
	Server Server `toml:"server"`
	Admin  Admin  `toml:"admin"`
}

type Actor struct {
	Username    string       `toml:"username"`
	Domain      string       `toml:"domain"` // handle domain, e.g. vrypan.net
	Host        string       `toml:"host"`   // where listnr is served, e.g. ap.vrypan.net
	Name        string       `toml:"name"`
	Summary     string       `toml:"summary"`
	Icon        string       `toml:"icon"`
	Header      string       `toml:"header"`
	BlogURL     string       `toml:"blog_url"`
	AlsoKnownAs []string     `toml:"also_known_as"`
	Fields      []ActorField `toml:"fields"`
	Tags        []ActorTag   `toml:"tags"`
}

type ActorField struct {
	Name  string `toml:"name"`
	Value string `toml:"value"`
}

type ActorTag struct {
	Name string `toml:"name"`
	Href string `toml:"href"`
}

type Feed struct {
	URL          string   `toml:"url"`
	PollInterval duration `toml:"poll_interval"`
	Backfill     int      `toml:"backfill"`
}

type Server struct {
	Listen      string `toml:"listen"`
	DataDir     string `toml:"data_dir"`
	LogRequests bool   `toml:"log_requests"`
}

type Admin struct {
	Token string `toml:"token"`
}

// duration wraps time.Duration so TOML values like "5m" parse.
type duration time.Duration

func (d *duration) UnmarshalText(b []byte) error {
	v, err := time.ParseDuration(string(b))
	if err != nil {
		return err
	}
	*d = duration(v)
	return nil
}

func (d duration) Value() time.Duration { return time.Duration(d) }

// Handle returns the actor's fediverse handle, e.g. "blog@vrypan.net".
func (a Actor) Handle() string { return a.Username + "@" + a.Domain }

// ID returns the actor's ActivityPub id.
func (a Actor) ID() string { return "https://" + a.Host + "/actor" }

func Load(path string) (*Config, error) {
	cfg := &Config{
		Feed:   Feed{PollInterval: duration(5 * time.Minute), Backfill: 20},
		Server: Server{Listen: "127.0.0.1:8420", DataDir: "."},
	}
	meta, err := toml.DecodeFile(path, cfg)
	if err != nil {
		return nil, err
	}
	if undecoded := meta.Undecoded(); len(undecoded) > 0 {
		return nil, fmt.Errorf("unknown config keys: %v", undecoded)
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) validate() error {
	for _, f := range []struct{ name, val string }{
		{"actor.username", c.Actor.Username},
		{"actor.domain", c.Actor.Domain},
		{"actor.host", c.Actor.Host},
		{"actor.blog_url", c.Actor.BlogURL},
		{"feed.url", c.Feed.URL},
	} {
		if f.val == "" {
			return fmt.Errorf("config: %s is required", f.name)
		}
	}
	if c.Admin.Token == "" {
		fmt.Fprintln(os.Stderr, "warning: admin.token not set; /admin API disabled")
	}
	return nil
}
