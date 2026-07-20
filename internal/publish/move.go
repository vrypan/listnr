package publish

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/vrypan/listnr/internal/config"
)

// MoveID derives a stable activity id from the old and target actor ids, so
// the same migration always produces the same activity and a receiver can
// recognise a redelivery rather than treating it as a second migration.
func MoveID(cfg config.Actor, target string) string {
	sum := sha256.Sum256([]byte(cfg.ID() + "\x00" + target))
	return cfg.ID() + "#move-" + hex.EncodeToString(sum[:])[:16]
}

// Move announces that this actor's followers should follow target instead.
//
// The activity is addressed to the followers collection rather than to Public:
// migration is a message to the people who follow this account, not a public
// broadcast, and that is the addressing Mastodon acts on.
//
// Both actor and object are the old actor: the thing that moved is this
// account, and it is the one saying so.
func Move(cfg config.Actor, target, movedAt string) map[string]any {
	return map[string]any{
		"@context":  "https://www.w3.org/ns/activitystreams",
		"id":        MoveID(cfg, target),
		"type":      "Move",
		"actor":     cfg.ID(),
		"object":    cfg.ID(),
		"target":    target,
		"to":        []string{"https://" + cfg.Host + "/followers"},
		"published": movedAt,
	}
}
