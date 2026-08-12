package graph

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// A stand-in Microsoft Graph.
//
// These tests cover the layer that synthetic fixtures cannot reach: the token
// exchange, pagination across @odata.nextLink, throttling and Retry-After,
// token invalidation mid-run, header contracts, and the exact JSON shape sent
// to sendMail. Getting any of these wrong produces a digest that works
// perfectly against fixtures and fails against the real service.

type mockGraph struct {
	server *httptest.Server
	// Counters so tests can assert on retry and caching behaviour.
	tokenRequests atomic.Int32
	pageRequests  atomic.Int32
	// Populated by the sendMail handler for inspection.
	sentPayload atomic.Value
}

func newMockGraph(t *testing.T, handler func(m *mockGraph, w http.ResponseWriter, r *http.Request) bool) *mockGraph {
	t.Helper()
	m := &mockGraph{}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Token endpoint.
		if strings.Contains(r.URL.Path, "/oauth2/v2.0/token") {
			m.tokenRequests.Add(1)
			if err := r.ParseForm(); err != nil {
				http.Error(w, "bad form", http.StatusBadRequest)
				return
			}
			// Both flows are legitimate: client_credentials for the unattended
			// job, refresh_token for a signed-in user (the only option for
			// personal accounts).
			switch r.Form.Get("grant_type") {
			case "client_credentials":
				if r.Form.Get("scope") != "https://graph.microsoft.com/.default" {
					http.Error(w, "wrong scope", http.StatusBadRequest)
					return
				}
			case "refresh_token":
				if r.Form.Get("refresh_token") == "" {
					http.Error(w, "missing refresh token", http.StatusBadRequest)
					return
				}
			default:
				http.Error(w, "wrong grant type", http.StatusBadRequest)
				return
			}
			writeJSON(w, map[string]any{
				"access_token": fmt.Sprintf("token-%d", m.tokenRequests.Load()),
				"expires_in":   3600,
			})
			return
		}

		// Every Graph call must be bearer-authenticated.
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			http.Error(w, `{"error":{"code":"unauthenticated"}}`, http.StatusUnauthorized)
			return
		}

		if handler != nil && handler(m, w, r) {
			return
		}

		http.Error(w, `{"error":{"code":"notFound"}}`, http.StatusNotFound)
	})

	m.server = httptest.NewServer(mux)
	t.Cleanup(m.server.Close)
	return m
}

func (m *mockGraph) client() *Client {
	c := New("test-tenant", "test-client", "test-secret",
		"director@example.org", "America/Denver")
	c.SetEndpoints(m.server.URL, m.server.URL)
	return c
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// ---------------------------------------------------------------------------

func TestTokenIsFetchedOnceAndReused(t *testing.T) {
	m := newMockGraph(t, func(m *mockGraph, w http.ResponseWriter, r *http.Request) bool {
		writeJSON(w, map[string]any{"value": []Message{}})
		return true
	})
	c := m.client()
	ctx := context.Background()

	for range 3 {
		if _, err := c.InboxSince(ctx, time.Now()); err != nil {
			t.Fatalf("InboxSince: %v", err)
		}
	}

	if got := m.tokenRequests.Load(); got != 1 {
		t.Errorf("token requested %d times, want 1 (it should be cached until expiry)", got)
	}
}

func TestPaginationFollowsNextLink(t *testing.T) {
	var m *mockGraph
	m = newMockGraph(t, func(_ *mockGraph, w http.ResponseWriter, r *http.Request) bool {
		// The follow-up pages live at a different path on purpose: it proves
		// the client follows the absolute nextLink URL Graph hands back rather
		// than reconstructing the messages path itself.
		if !strings.Contains(r.URL.Path, "/messages") && !strings.Contains(r.URL.Path, "/next") {
			return false
		}
		page := m.pageRequests.Add(1)

		msg := Message{
			ID:               fmt.Sprintf("m%d", page),
			Subject:          fmt.Sprintf("Message from page %d", page),
			ReceivedDateTime: time.Now(),
			From: &Recipient{EmailAddress: EmailAddress{
				Address: "someone@example.org", Name: "Someone",
			}},
		}

		body := map[string]any{"value": []Message{msg}}
		// Two more pages, then stop.
		if page < 3 {
			body["@odata.nextLink"] = m.server.URL + "/v1.0/next?page=" + fmt.Sprint(page+1)
		}
		writeJSON(w, body)
		return true
	})

	got, err := m.client().InboxSince(context.Background(), time.Now())
	if err != nil {
		t.Fatalf("InboxSince: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("collected %d messages across pages, want 3", len(got))
	}
	if got[2].Subject != "Message from page 3" {
		t.Errorf("last message = %q, want the third page's", got[2].Subject)
	}
}

// The mock's /next path is not under /messages, so this also proves the client
// follows the absolute nextLink URL rather than rebuilding the path itself.
func TestPaginationStopsWithoutNextLink(t *testing.T) {
	m := newMockGraph(t, func(m *mockGraph, w http.ResponseWriter, r *http.Request) bool {
		m.pageRequests.Add(1)
		writeJSON(w, map[string]any{"value": []Message{{ID: "only"}}})
		return true
	})

	if _, err := m.client().InboxSince(context.Background(), time.Now()); err != nil {
		t.Fatalf("InboxSince: %v", err)
	}
	if got := m.pageRequests.Load(); got != 1 {
		t.Errorf("made %d requests, want 1 when there is no nextLink", got)
	}
}

func TestThrottlingIsRetriedAndHonoursRetryAfter(t *testing.T) {
	var attempts atomic.Int32
	m := newMockGraph(t, func(_ *mockGraph, w http.ResponseWriter, r *http.Request) bool {
		if attempts.Add(1) == 1 {
			// Graph says exactly how long to wait; 1 second keeps the test quick
			// while still proving the header is read rather than ignored.
			w.Header().Set("Retry-After", "1")
			http.Error(w, `{"error":{"code":"throttled"}}`, http.StatusTooManyRequests)
			return true
		}
		writeJSON(w, map[string]any{"value": []Message{{ID: "after-retry"}}})
		return true
	})

	start := time.Now()
	got, err := m.client().InboxSince(context.Background(), time.Now())
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("a 429 should be retried, not returned as an error: %v", err)
	}
	if len(got) != 1 || got[0].ID != "after-retry" {
		t.Errorf("did not get the post-retry result: %v", got)
	}
	if elapsed < time.Second {
		t.Errorf("waited %v, but Retry-After asked for 1s", elapsed)
	}
	if attempts.Load() != 2 {
		t.Errorf("made %d attempts, want 2", attempts.Load())
	}
}

func TestServerErrorsAreRetried(t *testing.T) {
	var attempts atomic.Int32
	m := newMockGraph(t, func(_ *mockGraph, w http.ResponseWriter, r *http.Request) bool {
		if attempts.Add(1) < 2 {
			w.Header().Set("Retry-After", "1")
			http.Error(w, `{"error":{"code":"serviceUnavailable"}}`, http.StatusServiceUnavailable)
			return true
		}
		writeJSON(w, map[string]any{"value": []Message{{ID: "recovered"}}})
		return true
	})

	got, err := m.client().InboxSince(context.Background(), time.Now())
	if err != nil {
		t.Fatalf("a 503 should be retried: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("got %d messages, want 1 after recovery", len(got))
	}
}

// A token can be revoked mid-run. One silent re-auth is correct; looping
// forever on 401 is not.
func TestUnauthorizedTriggersOneReauth(t *testing.T) {
	var calls atomic.Int32
	m := newMockGraph(t, func(_ *mockGraph, w http.ResponseWriter, r *http.Request) bool {
		if calls.Add(1) == 1 {
			http.Error(w, `{"error":{"code":"InvalidAuthenticationToken"}}`, http.StatusUnauthorized)
			return true
		}
		writeJSON(w, map[string]any{"value": []Message{{ID: "ok"}}})
		return true
	})
	c := m.client()

	if _, err := c.InboxSince(context.Background(), time.Now()); err != nil {
		t.Fatalf("expected recovery after re-auth: %v", err)
	}
	if got := m.tokenRequests.Load(); got != 2 {
		t.Errorf("token fetched %d times, want 2 (original plus one refresh)", got)
	}
}

func TestPersistentUnauthorizedFailsWithAUsefulHint(t *testing.T) {
	m := newMockGraph(t, func(_ *mockGraph, w http.ResponseWriter, r *http.Request) bool {
		http.Error(w, `{"error":{"code":"InvalidAuthenticationToken"}}`, http.StatusUnauthorized)
		return true
	})

	_, err := m.client().InboxSince(context.Background(), time.Now())
	if err == nil {
		t.Fatal("expected an error when auth never succeeds")
	}
	if !strings.Contains(err.Error(), "client secret") {
		t.Errorf("401 should hint at the expired secret, got: %v", err)
	}
}

func TestForbiddenHintsAtTheAccessPolicy(t *testing.T) {
	m := newMockGraph(t, func(_ *mockGraph, w http.ResponseWriter, r *http.Request) bool {
		http.Error(w, `{"error":{"code":"ErrorAccessDenied"}}`, http.StatusForbidden)
		return true
	})

	err := m.client().Ping(context.Background())
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "ApplicationAccessPolicy") {
		t.Errorf("403 should point at consent or the access policy, got: %v", err)
	}
}

func TestCalendarRequestsMailboxLocalTimes(t *testing.T) {
	var preferHeader atomic.Value
	m := newMockGraph(t, func(_ *mockGraph, w http.ResponseWriter, r *http.Request) bool {
		if !strings.Contains(r.URL.Path, "/calendarView") {
			return false
		}
		preferHeader.Store(r.Header.Get("Prefer"))
		writeJSON(w, map[string]any{"value": []Event{}})
		return true
	})

	start := time.Now()
	if _, err := m.client().CalendarView(context.Background(), start, start.AddDate(0, 0, 7)); err != nil {
		t.Fatalf("CalendarView: %v", err)
	}

	got, _ := preferHeader.Load().(string)
	if !strings.Contains(got, "America/Denver") {
		// Without this header Graph returns UTC, which silently shifts events
		// across week boundaries.
		t.Errorf("Prefer header = %q, want the mailbox timezone", got)
	}
}

func TestPingReadsTheInboxNotTheDirectory(t *testing.T) {
	var path atomic.Value
	m := newMockGraph(t, func(_ *mockGraph, w http.ResponseWriter, r *http.Request) bool {
		path.Store(r.URL.Path)
		writeJSON(w, map[string]any{"id": "inbox", "displayName": "Inbox"})
		return true
	})

	if err := m.client().Ping(context.Background()); err != nil {
		t.Fatalf("Ping: %v", err)
	}

	got, _ := path.Load().(string)
	if !strings.Contains(got, "mailFolders/inbox") {
		t.Errorf("Ping hit %q; it must read the inbox folder, because reading "+
			"/users/{id} would require the tenant-wide User.Read.All permission", got)
	}
}

func TestSendMailPayloadShape(t *testing.T) {
	var captured atomic.Value
	m := newMockGraph(t, func(_ *mockGraph, w http.ResponseWriter, r *http.Request) bool {
		if !strings.Contains(r.URL.Path, "/sendMail") {
			return false
		}
		var body sendMailRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return true
		}
		captured.Store(body)
		w.WriteHeader(http.StatusAccepted)
		return true
	})

	err := m.client().SendMail(context.Background(),
		"Your week: 3 emails are waiting on you",
		"<div>hello</div>",
		[]string{"director@example.org", " second@example.org ", ""})
	if err != nil {
		t.Fatalf("SendMail: %v", err)
	}

	body, ok := captured.Load().(sendMailRequest)
	if !ok {
		t.Fatal("sendMail was never called")
	}
	if body.Message.Subject != "Your week: 3 emails are waiting on you" {
		t.Errorf("subject = %q", body.Message.Subject)
	}
	if body.Message.Body.ContentType != "HTML" {
		t.Errorf("contentType = %q, want HTML", body.Message.Body.ContentType)
	}
	if !body.SaveToSentItems {
		t.Error("the digest should be saved to Sent Items as a durable record")
	}
	if len(body.Message.ToRecipients) != 2 {
		t.Fatalf("recipients = %d, want 2 (blank entries dropped, whitespace trimmed)",
			len(body.Message.ToRecipients))
	}
	if got := body.Message.ToRecipients[1].EmailAddress.Address; got != "second@example.org" {
		t.Errorf("recipient not trimmed: %q", got)
	}
}

func TestSendMailRefusesWithNoRecipients(t *testing.T) {
	m := newMockGraph(t, nil)

	err := m.client().SendMail(context.Background(), "s", "<p>b</p>", []string{"", "  "})
	if err == nil {
		t.Fatal("expected an error rather than a silently unsent digest")
	}
	if !strings.Contains(err.Error(), "recipients") {
		t.Errorf("error should name the problem, got: %v", err)
	}
}

func TestBadCredentialsSurfaceTheProviderMessage(t *testing.T) {
	m := newMockGraph(t, nil)
	// Override the token endpoint to reject.
	m.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"error":             "invalid_client",
			"error_description": "AADSTS7000215: Invalid client secret provided.",
		})
	})

	_, err := m.client().InboxSince(context.Background(), time.Now())
	if err == nil {
		t.Fatal("expected an authentication error")
	}
	if !strings.Contains(err.Error(), "AADSTS7000215") {
		t.Errorf("the provider's own diagnostic should reach the log, got: %v", err)
	}
}

func TestMessageFilterAndSelectAreEncoded(t *testing.T) {
	var rawQuery atomic.Value
	m := newMockGraph(t, func(_ *mockGraph, w http.ResponseWriter, r *http.Request) bool {
		rawQuery.Store(r.URL.RawQuery)
		writeJSON(w, map[string]any{"value": []Message{}})
		return true
	})

	since := time.Date(2026, 7, 27, 0, 0, 0, 0, time.UTC)
	if _, err := m.client().InboxSince(context.Background(), since); err != nil {
		t.Fatalf("InboxSince: %v", err)
	}

	q, _ := rawQuery.Load().(string)
	if strings.Contains(q, " ") {
		t.Errorf("unencoded space in query, Graph will reject it: %q", q)
	}
	if !strings.Contains(q, "2026-07-27") {
		t.Errorf("filter lost the since date: %q", q)
	}
	if !strings.Contains(q, "conversationId") {
		t.Errorf("select must include conversationId or reply detection breaks: %q", q)
	}
}

// ---------------------------------------------------------------------------
// Delegated (signed-in user) mode
// ---------------------------------------------------------------------------

// App-only tokens have no user, so they must name the mailbox; delegated tokens
// address the signed-in user's own mailbox as /me. Sending the wrong one yields
// a 403 that reads like a permissions problem and sends you hunting in Entra.
func TestDelegatedModeAddressesTheMailboxAsMe(t *testing.T) {
	var path atomic.Value
	m := newMockGraph(t, func(_ *mockGraph, w http.ResponseWriter, r *http.Request) bool {
		path.Store(r.URL.Path)
		writeJSON(w, map[string]any{"value": []Message{}})
		return true
	})

	c := m.client()
	c.UseDelegated("fake-refresh-token", nil)

	if _, err := c.InboxSince(context.Background(), time.Now()); err != nil {
		t.Fatalf("InboxSince: %v", err)
	}

	got, _ := path.Load().(string)
	if !strings.Contains(got, "/me/") {
		t.Errorf("delegated request hit %q, want the /me form", got)
	}
	if strings.Contains(got, "/users/") {
		t.Errorf("delegated request must not name the mailbox: %q", got)
	}
}

func TestAppOnlyModeNamesTheMailbox(t *testing.T) {
	var path atomic.Value
	m := newMockGraph(t, func(_ *mockGraph, w http.ResponseWriter, r *http.Request) bool {
		path.Store(r.URL.Path)
		writeJSON(w, map[string]any{"value": []Message{}})
		return true
	})

	if _, err := m.client().InboxSince(context.Background(), time.Now()); err != nil {
		t.Fatalf("InboxSince: %v", err)
	}

	got, _ := path.Load().(string)
	if !strings.Contains(got, "/users/") {
		t.Errorf("app-only request hit %q, want the /users/{mailbox} form", got)
	}
}

func TestDelegatedModeUsesTheRefreshTokenGrant(t *testing.T) {
	var grantType atomic.Value
	m := newMockGraph(t, nil)
	m.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/token") {
			_ = r.ParseForm()
			grantType.Store(r.Form.Get("grant_type"))
			writeJSON(w, map[string]any{
				"access_token": "delegated-token",
				"expires_in":   3600,
			})
			return
		}
		writeJSON(w, map[string]any{"value": []Message{}})
	})

	c := m.client()
	c.UseDelegated("fake-refresh-token", nil)

	if _, err := c.InboxSince(context.Background(), time.Now()); err != nil {
		t.Fatalf("InboxSince: %v", err)
	}

	if got, _ := grantType.Load().(string); got != "refresh_token" {
		t.Errorf("grant_type = %q, want refresh_token (client_credentials cannot "+
			"work for personal accounts)", got)
	}
}

// Microsoft rotates refresh tokens. A rotated token that is never persisted
// means the tool works until the old one expires and then fails permanently,
// with an error that looks nothing like "we forgot to save something".
func TestRotatedRefreshTokenIsHandedBackForPersisting(t *testing.T) {
	var saved atomic.Value
	m := newMockGraph(t, nil)
	m.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/token") {
			writeJSON(w, map[string]any{
				"access_token":  "delegated-token",
				"refresh_token": "rotated-token",
				"expires_in":    3600,
			})
			return
		}
		writeJSON(w, map[string]any{"value": []Message{}})
	})

	c := m.client()
	c.UseDelegated("original-token", func(rotated string) { saved.Store(rotated) })

	if _, err := c.InboxSince(context.Background(), time.Now()); err != nil {
		t.Fatalf("InboxSince: %v", err)
	}

	if got, _ := saved.Load().(string); got != "rotated-token" {
		t.Errorf("rotated token handed back as %q, want %q", got, "rotated-token")
	}
}

func TestExpiredSignInPointsAtTheFix(t *testing.T) {
	m := newMockGraph(t, nil)
	m.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"error":             "invalid_grant",
			"error_description": "AADSTS70008: The refresh token has expired.",
		})
	})

	c := m.client()
	c.UseDelegated("stale-token", nil)

	_, err := c.InboxSince(context.Background(), time.Now())
	if err == nil {
		t.Fatal("expected an error for an expired sign-in")
	}
	if !strings.Contains(err.Error(), "--login") {
		t.Errorf("the error should tell the user how to recover, got: %v", err)
	}
}
