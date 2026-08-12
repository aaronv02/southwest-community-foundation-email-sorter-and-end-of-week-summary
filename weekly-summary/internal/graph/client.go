// Package graph is a small Microsoft Graph client for unattended, app-only use.
//
// It authenticates with the client-credentials flow, which is the only OAuth
// flow suited to a scheduled job: there is no user present to complete an
// interactive sign-in, and refresh tokens on an unattended machine expire in
// ways that fail silently weeks later.
//
// The tradeoff is that application permissions grant tenant-wide mailbox
// access by default. That is unacceptable for a foundation, so the setup
// documentation requires an Exchange ApplicationAccessPolicy scoping this app
// to a single mailbox. See README "Lock it to one mailbox".
package graph

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	defaultGraphBase = "https://graph.microsoft.com/v1.0"
	defaultLoginBase = "https://login.microsoftonline.com"
	maxPages         = 40
	httpTimeout      = 60 * time.Second
)

// Client talks to Graph for one specific mailbox.
type Client struct {
	tenantID string
	clientID string
	secret   string
	mailbox  string
	tz       string

	// Service endpoints. Fields rather than constants so the tests can point
	// the client at a local stand-in Graph and exercise paging, throttling,
	// and token handling for real.
	graphBase string
	loginBase string

	// Delegated (signed-in-user) mode. When refreshToken is set the client
	// authenticates as the user instead of as the application, which is the
	// only option for personal Microsoft accounts.
	refreshToken string
	// Called when Microsoft rotates the refresh token, so it can be persisted.
	// Without this the tool works until the current token expires, then fails
	// permanently for no visible reason.
	onRefreshToken func(string)

	http  *http.Client
	token string
	// Slightly before the real expiry, so a long run never trips over it.
	tokenExpiry time.Time
}

// New builds a client. tz is an IANA name used for calendar localization.
func New(tenantID, clientID, secret, mailbox, tz string) *Client {
	return &Client{
		tenantID:  tenantID,
		clientID:  clientID,
		secret:    secret,
		mailbox:   mailbox,
		tz:        tz,
		graphBase: defaultGraphBase,
		loginBase: defaultLoginBase,
		http:      &http.Client{Timeout: httpTimeout},
	}
}

// SetEndpoints redirects the client at alternative service URLs. Test-only.
func (c *Client) SetEndpoints(graph, login string) {
	c.graphBase = graph
	c.loginBase = login
}

// UseDelegated switches the client to signed-in-user authentication.
//
// onRotate is called whenever Microsoft issues a replacement refresh token and
// must persist it.
func (c *Client) UseDelegated(refreshToken string, onRotate func(string)) {
	c.refreshToken = refreshToken
	c.onRefreshToken = onRotate
}

// Delegated reports whether the client authenticates as a user.
func (c *Client) Delegated() bool { return c.refreshToken != "" }

// mailboxPath is the URL prefix for mailbox-scoped requests.
//
// Delegated tokens address the signed-in user's own mailbox as /me. App-only
// tokens have no user, so they must name the mailbox explicitly. Getting this
// wrong yields a 403 that reads like a permissions problem.
func (c *Client) mailboxPath() string {
	if c.Delegated() {
		return "/me"
	}
	return "/users/" + url.PathEscape(c.mailbox)
}

// Mailbox returns the mailbox this client reads.
func (c *Client) Mailbox() string { return c.mailbox }

type tokenResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
	Error       string `json:"error"`
	ErrorDesc   string `json:"error_description"`
}

// accessToken returns a cached or freshly minted app-only token.
func (c *Client) accessToken(ctx context.Context) (string, error) {
	if c.token != "" && time.Now().Before(c.tokenExpiry) {
		return c.token, nil
	}

	if c.Delegated() {
		return c.redeemRefreshToken(ctx)
	}

	form := url.Values{
		"client_id":     {c.clientID},
		"client_secret": {c.secret},
		// .default requests every application permission already consented to
		// for this app, which is the correct shape for client credentials.
		"scope":      {"https://graph.microsoft.com/.default"},
		"grant_type": {"client_credentials"},
	}

	endpoint := fmt.Sprintf("%s/%s/oauth2/v2.0/token", c.loginBase, c.tenantID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint,
		strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("requesting token: %w", err)
	}
	defer resp.Body.Close()

	var tr tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return "", fmt.Errorf("decoding token response: %w", err)
	}
	if tr.Error != "" {
		return "", fmt.Errorf("authentication failed: %s: %s", tr.Error, tr.ErrorDesc)
	}
	if tr.AccessToken == "" {
		return "", fmt.Errorf("authentication returned no token (HTTP %d)", resp.StatusCode)
	}

	c.token = tr.AccessToken
	c.tokenExpiry = time.Now().Add(time.Duration(tr.ExpiresIn-120) * time.Second)
	return c.token, nil
}

// APIError carries a Graph failure with enough context to act on.
type APIError struct {
	Status int
	Body   string
	URL    string
}

func (e *APIError) Error() string {
	hint := ""
	switch e.Status {
	case http.StatusUnauthorized:
		hint = "\nHint: check the client secret has not expired in Entra."
	case http.StatusForbidden:
		hint = "\nHint: the app may lack admin consent, or the ApplicationAccessPolicy " +
			"may not include this mailbox."
	case http.StatusNotFound:
		hint = "\nHint: check the mailbox address is exact."
	}
	return fmt.Sprintf("Graph request failed (HTTP %d) for %s: %s%s",
		e.Status, e.URL, truncate(e.Body, 400), hint)
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// do performs a Graph request, retrying on throttling and transient failures.
func (c *Client) do(ctx context.Context, method, rawURL string, body []byte) ([]byte, error) {
	const maxAttempts = 5

	for attempt := 0; ; attempt++ {
		token, err := c.accessToken(ctx)
		if err != nil {
			return nil, err
		}

		var reader io.Reader
		if body != nil {
			reader = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, rawURL, reader)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/json")
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if c.tz != "" {
			// Ask Graph to render calendar times in the mailbox's timezone
			// rather than UTC, so week boundaries line up with her actual week.
			req.Header.Set("Prefer", fmt.Sprintf(`outlook.timezone="%s"`, c.tz))
		}

		resp, err := c.http.Do(req)
		if err != nil {
			if attempt < maxAttempts-1 {
				sleep(ctx, backoff(attempt))
				continue
			}
			return nil, fmt.Errorf("network error calling Graph: %w", err)
		}

		payload, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			return nil, readErr
		}

		switch {
		case resp.StatusCode == http.StatusTooManyRequests,
			resp.StatusCode >= 500:
			if attempt < maxAttempts-1 {
				wait := backoff(attempt)
				// Graph states exactly how long to wait; guessing is both
				// ruder and slower.
				if ra := resp.Header.Get("Retry-After"); ra != "" {
					if secs, err := strconv.Atoi(ra); err == nil && secs > 0 {
						wait = time.Duration(secs) * time.Second
					}
				}
				sleep(ctx, wait)
				continue
			}
			return nil, &APIError{Status: resp.StatusCode, Body: string(payload), URL: rawURL}

		case resp.StatusCode == http.StatusUnauthorized && attempt == 0:
			// Token may have been revoked mid-run; drop it and retry once.
			c.token = ""
			continue

		case resp.StatusCode >= 400:
			return nil, &APIError{Status: resp.StatusCode, Body: string(payload), URL: rawURL}
		}

		return payload, nil
	}
}

func backoff(attempt int) time.Duration {
	return time.Duration(1<<attempt) * time.Second
}

func sleep(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

// listPaged walks @odata.nextLink and accumulates typed values.
func listPaged[T any](ctx context.Context, c *Client, path string) ([]T, error) {
	var out []T
	next := c.graphBase + path

	for page := 0; page < maxPages && next != ""; page++ {
		payload, err := c.do(ctx, http.MethodGet, next, nil)
		if err != nil {
			return nil, err
		}
		var envelope struct {
			Value    []T    `json:"value"`
			NextLink string `json:"@odata.nextLink"`
		}
		if err := json.Unmarshal(payload, &envelope); err != nil {
			return nil, fmt.Errorf("decoding Graph response: %w", err)
		}
		out = append(out, envelope.Value...)
		next = envelope.NextLink
	}
	return out, nil
}

const messageSelect = "id,conversationId,subject,bodyPreview,receivedDateTime,sentDateTime," +
	"isRead,isDraft,hasAttachments,importance,categories,from,sender,toRecipients," +
	"ccRecipients,flag,webLink"

// InboxSince returns inbox messages received at or after the given instant.
func (c *Client) InboxSince(ctx context.Context, since time.Time) ([]Message, error) {
	path := fmt.Sprintf(
		"%s/mailFolders/inbox/messages?$select=%s&$filter=receivedDateTime ge %s"+
			"&$orderby=receivedDateTime desc&$top=100",
		c.mailboxPath(), messageSelect, since.UTC().Format(time.RFC3339))
	return listPaged[Message](ctx, c, encodeQuery(path))
}

// SentSince returns messages sent at or after the given instant.
//
// Reaching further back than the reporting window is deliberate: a reply sent
// last week still answers a message received last week, and the digest must not
// nag about mail that was already handled.
func (c *Client) SentSince(ctx context.Context, since time.Time) ([]Message, error) {
	path := fmt.Sprintf(
		"%s/mailFolders/sentitems/messages?$select=%s&$filter=sentDateTime ge %s"+
			"&$orderby=sentDateTime desc&$top=100",
		c.mailboxPath(), messageSelect, since.UTC().Format(time.RFC3339))
	return listPaged[Message](ctx, c, encodeQuery(path))
}

const eventSelect = "id,subject,bodyPreview,start,end,isAllDay,isCancelled,isOrganizer," +
	"responseStatus,organizer,attendees,location,onlineMeetingUrl,showAs,type"

// CalendarView returns events overlapping the window, expanding recurrences.
func (c *Client) CalendarView(ctx context.Context, start, end time.Time) ([]Event, error) {
	path := fmt.Sprintf(
		"%s/calendarView?startDateTime=%s&endDateTime=%s&$select=%s"+
			"&$orderby=start/dateTime&$top=100",
		c.mailboxPath(),
		start.UTC().Format(time.RFC3339),
		end.UTC().Format(time.RFC3339),
		eventSelect)
	return listPaged[Event](ctx, c, encodeQuery(path))
}

// encodeQuery percent-encodes spaces in $filter/$select without mangling the
// OData syntax that Graph expects to see literally.
func encodeQuery(path string) string {
	return strings.ReplaceAll(path, " ", "%20")
}

type sendMailRequest struct {
	Message         outgoing `json:"message"`
	SaveToSentItems bool     `json:"saveToSentItems"`
}

type outgoing struct {
	Subject      string      `json:"subject"`
	Body         body        `json:"body"`
	ToRecipients []Recipient `json:"toRecipients"`
}

type body struct {
	ContentType string `json:"contentType"`
	Content     string `json:"content"`
}

// SendMail sends the digest from the analyzed mailbox to the given recipients.
func (c *Client) SendMail(ctx context.Context, subject, html string, to []string) error {
	recipients := make([]Recipient, 0, len(to))
	for _, addr := range to {
		addr = strings.TrimSpace(addr)
		if addr == "" {
			continue
		}
		recipients = append(recipients, Recipient{EmailAddress: EmailAddress{Address: addr}})
	}
	if len(recipients) == 0 {
		return fmt.Errorf("no recipients configured")
	}

	payload, err := json.Marshal(sendMailRequest{
		Message: outgoing{
			Subject:      subject,
			Body:         body{ContentType: "HTML", Content: html},
			ToRecipients: recipients,
		},
		// Keep a copy in Sent Items so there is a durable record that the
		// digest went out, visible without reading logs.
		SaveToSentItems: true,
	})
	if err != nil {
		return err
	}

	endpoint := fmt.Sprintf("%s%s/sendMail", c.graphBase, c.mailboxPath())
	_, err = c.do(ctx, http.MethodPost, endpoint, payload)
	return err
}

// Ping verifies credentials and mailbox access before any real work.
//
// Worth its own call: it turns "the digest silently produced nothing" into a
// specific, actionable error at setup time.
//
// Deliberately reads the inbox folder rather than the user object. Fetching
// /users/{id} would require the User.Read.All application permission - a
// tenant-wide directory read this tool has no business holding. Reading the
// folder proves the same thing (we can reach this mailbox) using Mail.Read,
// which is already needed.
func (c *Client) Ping(ctx context.Context) error {
	endpoint := fmt.Sprintf("%s%s/mailFolders/inbox?$select=id,displayName,totalItemCount",
		c.graphBase, c.mailboxPath())
	_, err := c.do(ctx, http.MethodGet, endpoint, nil)
	return err
}
