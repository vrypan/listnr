package fedi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// supportedActorTypes are the ActivityStreams actor types listnr will federate
// with. A migration target has to be one of them.
var supportedActorTypes = map[string]bool{
	"Person": true, "Service": true, "Application": true,
	"Organization": true, "Group": true,
}

// TargetActor is a migration target that passed every validation.
type TargetActor struct {
	ID          string
	Type        string
	AlsoKnownAs []string
	// Fingerprint digests the exact document that was validated, so the
	// migration record can name what was checked.
	Fingerprint string
}

// FetchMoveTarget dereferences a proposed migration target and proves it is
// willing to receive the local actor's followers.
//
// The proof is the target's own `alsoKnownAs` naming the local actor. A local
// config field would prove nothing: anyone could then redirect followers to an
// account they do not control. The check therefore requires actually fetching
// the target and reading what it says about itself.
//
// The fetch goes through the same signed, SSRF-guarded, size-limited client as
// every other outbound request.
func (c *Client) FetchMoveTarget(ctx context.Context, targetURL, localActorID string) (*TargetActor, error) {
	if err := validateTargetURL(targetURL, localActorID); err != nil {
		return nil, err
	}

	body, status, err := c.Get(ctx, targetURL)
	if err != nil {
		return nil, fmt.Errorf("fetch move target %s: %w", targetURL, err)
	}
	if status < 200 || status > 299 {
		return nil, fmt.Errorf("fetch move target %s: HTTP %d", targetURL, status)
	}

	return parseAndValidateTarget(body, targetURL, localActorID)
}

// parseAndValidateTarget checks a fetched target document against the actor
// asking to migrate to it.
func parseAndValidateTarget(body []byte, targetURL, localActorID string) (*TargetActor, error) {
	var doc struct {
		ID          string          `json:"id"`
		Type        string          `json:"type"`
		AlsoKnownAs json.RawMessage `json:"alsoKnownAs"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("parse move target %s: %w", targetURL, err)
	}

	// The document must claim exactly the id we asked for; a redirect that
	// lands somewhere else is not the target the operator confirmed.
	if doc.ID != targetURL {
		return nil, fmt.Errorf("move target document id %q does not match the requested target %q",
			doc.ID, targetURL)
	}
	if !supportedActorTypes[doc.Type] {
		return nil, fmt.Errorf("move target %s has unsupported actor type %q", targetURL, doc.Type)
	}

	aliases := stringOrStringArray(doc.AlsoKnownAs)
	if !containsExact(aliases, localActorID) {
		return nil, fmt.Errorf(
			"move target %s does not list %s in its alsoKnownAs; add the alias on the target account first",
			targetURL, localActorID)
	}

	sum := sha256.Sum256(body)
	return &TargetActor{
		ID:          doc.ID,
		Type:        doc.Type,
		AlsoKnownAs: aliases,
		Fingerprint: hex.EncodeToString(sum[:]),
	}, nil
}

func validateTargetURL(targetURL, localActorID string) error {
	if targetURL == "" {
		return errors.New("move target is required")
	}
	if targetURL == localActorID {
		return errors.New("move target must differ from the local actor")
	}
	u, err := url.Parse(targetURL)
	if err != nil {
		return fmt.Errorf("move target is not a valid URL: %w", err)
	}
	if u.Scheme != "https" {
		return fmt.Errorf("move target must be an https URL, got %q", u.Scheme)
	}
	if u.Host == "" {
		return errors.New("move target must be an absolute URL")
	}
	if u.Fragment != "" {
		return errors.New("move target must not contain a fragment")
	}
	return nil
}

// stringOrStringArray reads the alsoKnownAs shapes actors use in practice: a
// single string, or an array of strings.
func stringOrStringArray(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var one string
	if json.Unmarshal(raw, &one) == nil {
		return []string{one}
	}
	var many []string
	if json.Unmarshal(raw, &many) == nil {
		return many
	}
	return nil
}

func containsExact(values []string, want string) bool {
	for _, v := range values {
		if strings.TrimSpace(v) == want {
			return true
		}
	}
	return false
}
