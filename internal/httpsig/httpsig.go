// Package httpsig implements draft-cavage HTTP Signatures with rsa-sha256,
// the flavor spoken by Mastodon and most of the fediverse.
package httpsig

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// MaxClockSkew is how far an inbound request's Date header may deviate
// from local time.
const MaxClockSkew = time.Hour

// Sign adds Date, Digest (for requests with a body) and Signature headers to
// r. Pass the exact body bytes that will be sent, or nil for GET-style
// requests. keyID is the URL of the signing actor's public key.
func Sign(r *http.Request, body []byte, key *rsa.PrivateKey, keyID string) error {
	if r.Header.Get("Date") == "" {
		r.Header.Set("Date", time.Now().UTC().Format(http.TimeFormat))
	}
	var headers []string
	if body != nil {
		sum := sha256.Sum256(body)
		r.Header.Set("Digest", "SHA-256="+base64.StdEncoding.EncodeToString(sum[:]))
		headers = []string{"(request-target)", "host", "date", "digest", "content-type"}
	} else {
		headers = []string{"(request-target)", "host", "date", "accept"}
	}

	msg, err := signingString(r, headers)
	if err != nil {
		return err
	}
	digest := sha256.Sum256([]byte(msg))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return err
	}
	r.Header.Set("Signature", fmt.Sprintf(
		`keyId="%s",algorithm="rsa-sha256",headers="%s",signature="%s"`,
		keyID, strings.Join(headers, " "), base64.StdEncoding.EncodeToString(sig)))
	return nil
}

// Signature is a parsed Signature header.
type Signature struct {
	KeyID     string
	Algorithm string
	Headers   []string
	Signature []byte
}

var sigParamRe = regexp.MustCompile(`(\w+)="([^"]*)"`)

// ParseSignature parses the value of a Signature header.
func ParseSignature(header string) (*Signature, error) {
	if header == "" {
		return nil, fmt.Errorf("missing Signature header")
	}
	params := map[string]string{}
	for _, m := range sigParamRe.FindAllStringSubmatch(header, -1) {
		params[m[1]] = m[2]
	}
	if params["keyId"] == "" || params["signature"] == "" {
		return nil, fmt.Errorf("signature header lacks keyId or signature")
	}
	raw, err := base64.StdEncoding.DecodeString(params["signature"])
	if err != nil {
		return nil, fmt.Errorf("signature not valid base64: %w", err)
	}
	headers := params["headers"]
	if headers == "" {
		headers = "date" // draft-cavage default
	}
	return &Signature{
		KeyID:     params["keyId"],
		Algorithm: params["algorithm"],
		Headers:   strings.Fields(strings.ToLower(headers)),
		Signature: raw,
	}, nil
}

// Verify checks sig against the received request r and its body. It enforces
// that the security-relevant headers were signed, that the Digest matches the
// body (for requests with one), and that the Date is within MaxClockSkew.
func Verify(r *http.Request, body []byte, pub *rsa.PublicKey, sig *Signature) error {
	signed := map[string]bool{}
	for _, h := range sig.Headers {
		signed[h] = true
	}
	required := []string{"(request-target)", "date"}
	if r.Method == http.MethodPost {
		required = append(required, "digest")
	}
	for _, h := range required {
		if !signed[h] {
			return fmt.Errorf("header %q not covered by signature", h)
		}
	}

	if signed["digest"] {
		sum := sha256.Sum256(body)
		want := "SHA-256=" + base64.StdEncoding.EncodeToString(sum[:])
		got := r.Header.Get("Digest")
		if !strings.EqualFold(got, want) {
			return fmt.Errorf("digest mismatch")
		}
	}

	date, err := http.ParseTime(r.Header.Get("Date"))
	if err != nil {
		return fmt.Errorf("bad Date header: %w", err)
	}
	if skew := time.Since(date); skew > MaxClockSkew || skew < -MaxClockSkew {
		return fmt.Errorf("date outside allowed clock skew: %s", date)
	}

	msg, err := signingString(r, sig.Headers)
	if err != nil {
		return err
	}
	digest := sha256.Sum256([]byte(msg))
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, digest[:], sig.Signature); err != nil {
		return fmt.Errorf("signature verification failed")
	}
	return nil
}

func signingString(r *http.Request, headers []string) (string, error) {
	var lines []string
	for _, h := range headers {
		switch h {
		case "(request-target)":
			lines = append(lines, "(request-target): "+
				strings.ToLower(r.Method)+" "+r.URL.RequestURI())
		case "host":
			host := r.Host
			if host == "" {
				host = r.URL.Host
			}
			lines = append(lines, "host: "+host)
		default:
			v := r.Header.Get(h)
			if v == "" {
				return "", fmt.Errorf("signed header %q not present in request", h)
			}
			lines = append(lines, h+": "+v)
		}
	}
	return strings.Join(lines, "\n"), nil
}
