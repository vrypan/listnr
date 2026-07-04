// Package delivery is the persistent outbound activity queue: signed POSTs
// to remote inboxes with retry and backoff.
package delivery

import (
	"bytes"
	"context"
	"crypto/rsa"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/vrypan/listnr/internal/fedi"
	"github.com/vrypan/listnr/internal/httpsig"
	"github.com/vrypan/listnr/internal/safehttp"
	"github.com/vrypan/listnr/internal/store"
)

const (
	tickInterval    = 10 * time.Second
	cleanupInterval = time.Hour
	cleanupMaxAge   = 30 * 24 * time.Hour
	// seenMaxAge outlives the signature clock-skew window (httpsig.MaxClockSkew
	// is 1h), after which a captured request can no longer be replayed.
	seenMaxAge  = 2 * time.Hour
	batchSize   = 20
	postTimeout = 10 * time.Second
	contentType = "application/activity+json"
)

// backoffSchedule[n] is the delay after the (n+1)-th failed attempt; a
// delivery that exhausts the schedule is marked failed.
var backoffSchedule = []time.Duration{
	time.Minute, 5 * time.Minute, 30 * time.Minute,
	2 * time.Hour, 6 * time.Hour, 24 * time.Hour, 48 * time.Hour,
}

type Queue struct {
	st    *store.Store
	key   *rsa.PrivateKey
	keyID string
	http  *http.Client
	log   *slog.Logger
}

// NewQueue builds the delivery queue. Pass httpClient to override the
// transport (tests use a loopback-capable one); nil selects the default
// SSRF-guarded client.
func NewQueue(st *store.Store, key *rsa.PrivateKey, keyID string, log *slog.Logger, httpClient *http.Client) *Queue {
	if httpClient == nil {
		httpClient = safehttp.Client(postTimeout)
	}
	return &Queue{
		st:    st,
		key:   key,
		keyID: keyID,
		http:  httpClient,
		log:   log,
	}
}

// Enqueue schedules one activity for delivery to one inbox.
func (q *Queue) Enqueue(activityJSON []byte, inboxURL string) error {
	return q.st.EnqueueDelivery(string(activityJSON), inboxURL)
}

// FanOut schedules an activity for delivery to every follower, using shared
// inboxes where available (one delivery per instance).
func (q *Queue) FanOut(activityJSON []byte) error {
	inboxes, err := q.st.DeliveryInboxes()
	if err != nil {
		return err
	}
	for _, inbox := range inboxes {
		if err := q.st.EnqueueDelivery(string(activityJSON), inbox); err != nil {
			return err
		}
	}
	return nil
}

// Run processes the queue until ctx is cancelled.
func (q *Queue) Run(ctx context.Context) {
	tick := time.NewTicker(tickInterval)
	cleanup := time.NewTicker(cleanupInterval)
	defer tick.Stop()
	defer cleanup.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			q.ProcessDue(ctx)
		case <-cleanup.C:
			if err := q.st.CleanupDeliveries(cleanupMaxAge); err != nil {
				q.log.Error("delivery cleanup failed", "err", err)
			}
			if err := q.st.CleanupSeenActivities(seenMaxAge); err != nil {
				q.log.Error("seen-activity cleanup failed", "err", err)
			}
		}
	}
}

// ProcessDue attempts every due delivery once. Exported for tests and for
// triggering an immediate drain after a fan-out.
func (q *Queue) ProcessDue(ctx context.Context) {
	due, err := q.st.DueDeliveries(batchSize)
	if err != nil {
		q.log.Error("fetching due deliveries failed", "err", err)
		return
	}
	for _, d := range due {
		if ctx.Err() != nil {
			return
		}
		q.attempt(ctx, d)
	}
}

func (q *Queue) attempt(ctx context.Context, d store.Delivery) {
	status, err := q.post(ctx, d.InboxURL, []byte(d.ActivityJSON))
	switch {
	case err == nil && status >= 200 && status <= 299:
		q.st.MarkDeliveryDone(d.ID)
		return
	case err == nil && status == http.StatusGone:
		// The whole inbox is gone: drop the delivery and any followers
		// reachable through it.
		q.log.Info("inbox gone, dropping followers", "inbox", d.InboxURL)
		q.st.MarkDeliveryFailed(d.ID, "410 Gone")
		q.st.DeleteFollowersWithInbox(d.InboxURL)
		return
	}

	reason := fmt.Sprintf("HTTP %d", status)
	if err != nil {
		reason = err.Error()
	}
	attempts := d.Attempts + 1
	if attempts > len(backoffSchedule) {
		q.log.Warn("delivery failed permanently", "inbox", d.InboxURL, "reason", reason)
		q.st.MarkDeliveryFailed(d.ID, reason)
		return
	}
	next := time.Now().Add(Backoff(attempts))
	q.log.Info("delivery failed, will retry", "inbox", d.InboxURL,
		"attempt", attempts, "next", next, "reason", reason)
	q.st.RescheduleDelivery(d.ID, attempts, next, reason)
}

// Backoff returns the retry delay after the given (1-based) attempt count.
func Backoff(attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	if attempts > len(backoffSchedule) {
		attempts = len(backoffSchedule)
	}
	return backoffSchedule[attempts-1]
}

func (q *Queue) post(ctx context.Context, inboxURL string, body []byte) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, inboxURL, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("User-Agent", fedi.UserAgent)
	if err := httpsig.Sign(req, body, q.key, q.keyID); err != nil {
		return 0, err
	}
	resp, err := q.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return resp.StatusCode, nil
}
