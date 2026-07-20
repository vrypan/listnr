package cmd

import (
	"net/http"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func deliveriesListCommand(f *cliFixture, status string, limit, offset int) *cobra.Command {
	return f.command(func(c *cobra.Command) {
		c.Flags().String("status", status, "")
		c.Flags().Int("limit", limit, "")
		c.Flags().Int("offset", offset, "")
	})
}

func TestDeliveriesListEncodesFiltersAndRendersTable(t *testing.T) {
	f := newCLIFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[
			{"id":7,"inbox_url":"https://a.example/inbox","status":"failed","attempts":7,
			 "next_attempt_at":"2026-07-20T10:00:00Z","last_error":"dial tcp: connection refused",
			 "activity_type":"Create","activity_id":"https://ap.example/posts/1#create"}
		]`))
	})
	if err := runDeliveriesList(deliveriesListCommand(f, "failed", 25, 50), nil); err != nil {
		t.Fatal(err)
	}
	if got := f.requests[0].URL.RequestURI(); got != "/admin/deliveries?limit=25&offset=50&status=failed" {
		t.Fatalf("request URI = %q", got)
	}
	out := f.out.String()
	for _, want := range []string{"ID", "STATUS", "failed", "7", "https://a.example/inbox",
		"Create https://ap.example/posts/1#create", "connection refused"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}

func TestDeliveriesListTruncatesLongErrorsForDisplay(t *testing.T) {
	long := strings.Repeat("x", 300)
	f := newCLIFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"id":1,"inbox_url":"https://a.example/inbox","status":"failed",
			"attempts":7,"next_attempt_at":"2026-07-20T10:00:00Z","last_error":"` + long + `",
			"activity_type":"Create","activity_id":"x"}]`))
	})
	if err := runDeliveriesList(deliveriesListCommand(f, "", 0, 0), nil); err != nil {
		t.Fatal(err)
	}
	out := f.out.String()
	if strings.Contains(out, long) {
		t.Fatal("the full error text was printed instead of a bounded column")
	}
	if !strings.Contains(out, "…") {
		t.Fatalf("truncated error is not marked with an ellipsis:\n%s", out)
	}
}

func TestDeliveriesListOmitsUnsetFilters(t *testing.T) {
	f := newCLIFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[]`))
	})
	if err := runDeliveriesList(deliveriesListCommand(f, "", 0, 0), nil); err != nil {
		t.Fatal(err)
	}
	if got := f.requests[0].URL.RequestURI(); got != "/admin/deliveries" {
		t.Fatalf("request URI = %q, want no query string", got)
	}
	if !strings.Contains(f.out.String(), "ID") {
		t.Fatal("empty listing should still print a header")
	}
}

func TestDeliveriesListRejectsUnknownStatusLocally(t *testing.T) {
	f := newCLIFixture(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("an unknown status must not reach the server")
	})
	if err := runDeliveriesList(deliveriesListCommand(f, "bogus", 0, 0), nil); err == nil {
		t.Fatal("want an error for an unknown status")
	}
	if len(f.requests) != 0 {
		t.Fatalf("sent %d requests, want 0", len(f.requests))
	}
}

func TestDeliveryRetryAndDeletePaths(t *testing.T) {
	f := newCLIFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ok":true,"id":7}`))
	})
	if err := runDeliveryRetry(f.command(nil), []string{"7"}); err != nil {
		t.Fatal(err)
	}
	if f.requests[0].Method != http.MethodPost || f.requests[0].URL.Path != "/admin/deliveries/7/retry" {
		t.Fatalf("retry request = %s %s", f.requests[0].Method, f.requests[0].URL.Path)
	}
	if !strings.Contains(f.out.String(), "requeued") {
		t.Fatalf("retry output = %q", f.out.String())
	}

	f.out.Reset()
	if err := runDeliveryDelete(f.command(nil), []string{"7"}); err != nil {
		t.Fatal(err)
	}
	if f.requests[1].Method != http.MethodDelete || f.requests[1].URL.Path != "/admin/deliveries/7" {
		t.Fatalf("delete request = %s %s", f.requests[1].Method, f.requests[1].URL.Path)
	}
	if !strings.Contains(f.out.String(), "deleted") {
		t.Fatalf("delete output = %q", f.out.String())
	}
}

func TestDeliveryCommandsRejectNonNumericIDsLocally(t *testing.T) {
	f := newCLIFixture(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("a non-numeric id must not reach the server")
	})
	if err := runDeliveryRetry(f.command(nil), []string{"abc"}); err == nil {
		t.Fatal("want an error from retry")
	}
	if err := runDeliveryDelete(f.command(nil), []string{"abc"}); err == nil {
		t.Fatal("want an error from delete")
	}
	if len(f.requests) != 0 {
		t.Fatalf("sent %d requests, want 0", len(f.requests))
	}
}

func TestDeliveryRetrySurfacesStateConflict(t *testing.T) {
	f := newCLIFixture(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "delivery is pending; the worker may be sending it", http.StatusConflict)
	})
	err := runDeliveryRetry(f.command(nil), []string{"7"})
	if err == nil {
		t.Fatal("want an error for a 409")
	}
	if !strings.Contains(err.Error(), "409") || !strings.Contains(err.Error(), "pending") {
		t.Fatalf("err = %v, want it to explain the conflict", err)
	}
}

func TestDeliveriesRetryFailedReportsCount(t *testing.T) {
	f := newCLIFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ok":true,"retried":12}`))
	})
	if err := runDeliveriesRetryFailed(f.command(nil), nil); err != nil {
		t.Fatal(err)
	}
	if f.requests[0].URL.Path != "/admin/deliveries/retry-failed" {
		t.Fatalf("request path = %q", f.requests[0].URL.Path)
	}
	if !strings.Contains(f.out.String(), "12 failed deliveries requeued") {
		t.Fatalf("output = %q", f.out.String())
	}
}
