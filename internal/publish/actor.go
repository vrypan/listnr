package publish

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/vrypan/listnr/internal/config"
)

// ActorUpdate wraps a complete actor document in an Update activity and
// returns the fingerprint that identifies that exact document.
//
// ActivityPub requires the object to be the full new representation, not a
// patch, so the fingerprint covers every actor property including the public
// key. The activity id is derived from the fingerprint, which makes the same
// profile always produce the same activity and lets receivers recognise a
// re-sent Update instead of treating it as a fresh change.
func ActorUpdate(cfg config.Actor, doc map[string]any) (map[string]any, string, error) {
	fingerprint, err := ActorFingerprint(doc)
	if err != nil {
		return nil, "", err
	}
	return map[string]any{
		"@context": []any{
			"https://www.w3.org/ns/activitystreams",
			"https://w3id.org/security/v1",
		},
		"id":     cfg.ID() + "#update-" + fingerprint,
		"type":   "Update",
		"actor":  cfg.ID(),
		"to":     []string{Public},
		"cc":     []string{"https://" + cfg.Host + "/followers"},
		"object": doc,
	}, fingerprint, nil
}

// ActorFingerprint digests the canonical serialization of an actor document.
// encoding/json sorts map keys, so equal documents always digest equally.
func ActorFingerprint(doc map[string]any) (string, error) {
	b, err := json.Marshal(doc)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}
