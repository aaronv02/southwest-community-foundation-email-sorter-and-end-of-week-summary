package graph

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Delegated sign-in via the OAuth device authorization flow.
//
// The scheduled digest uses app-only auth, which is right for an unattended
// job but has a hard requirement: it only works against an organizational
// tenant. Microsoft does not support the client-credentials flow for personal
// Microsoft accounts at all.
//
// This is the other path. The user signs in once in a browser, and the tool
// keeps a refresh token. It works with any Outlook account, needs no admin
// consent (the user consents for themselves), and needs no client secret.
//
// It is deliberately NOT the default for the scheduled job: delegated refresh
// tokens expire through inactivity, password changes, and conditional-access
// policy, and they do it silently weeks later on a machine nobody is watching.
// For testing, and for mailboxes where app-only is unavailable, it is correct.

// Delegated permissions requested at sign-in. None of these need an admin:
// the signing-in user consents on their own behalf.
//
// offline_access is what yields the refresh token; without it the tool would
// need an interactive sign-in on every run.
var delegatedScopes = []string{
	"offline_access",
	"https://graph.microsoft.com/Mail.ReadWrite",
	"https://graph.microsoft.com/Mail.Send",
	"https://graph.microsoft.com/Calendars.Read",
	"https://graph.microsoft.com/User.Read",
}

// DeviceCodePrompt is what the user must do to complete sign-in.
type DeviceCodePrompt struct {
	UserCode        string
	VerificationURL string
	ExpiresIn       time.Duration
	// How long to wait between polls, as dictated by the service.
	Interval time.Duration
	// Opaque handle used to poll for completion.
	deviceCode string
}

type deviceCodeResponse struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
	Error           string `json:"error"`
	ErrorDesc       string `json:"error_description"`
}

// StartDeviceCode begins an interactive sign-in.
//
// tenant should usually be "common", which accepts both work/school and
// personal Microsoft accounts.
func StartDeviceCode(ctx context.Context, loginBase, tenant, clientID string) (*DeviceCodePrompt, error) {
	form := url.Values{
		"client_id": {clientID},
		"scope":     {strings.Join(delegatedScopes, " ")},
	}

	endpoint := fmt.Sprintf("%s/%s/oauth2/v2.0/devicecode", loginBase, tenant)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint,
		strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := (&http.Client{Timeout: httpTimeout}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("starting sign-in: %w", err)
	}
	defer resp.Body.Close()

	var dc deviceCodeResponse
	if err := json.NewDecoder(resp.Body).Decode(&dc); err != nil {
		return nil, fmt.Errorf("decoding sign-in response: %w", err)
	}
	if dc.Error != "" {
		return nil, fmt.Errorf("sign-in could not start: %s: %s", dc.Error, dc.ErrorDesc)
	}
	if dc.UserCode == "" || dc.DeviceCode == "" {
		return nil, fmt.Errorf("sign-in response was missing a code (HTTP %d)", resp.StatusCode)
	}

	interval := time.Duration(max(dc.Interval, 5)) * time.Second
	return &DeviceCodePrompt{
		UserCode:        dc.UserCode,
		VerificationURL: dc.VerificationURI,
		ExpiresIn:       time.Duration(dc.ExpiresIn) * time.Second,
		Interval:        interval,
		deviceCode:      dc.DeviceCode,
	}, nil
}

type tokenGrantResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	Error        string `json:"error"`
	ErrorDesc    string `json:"error_description"`
}

// PollDeviceCode waits for the user to finish signing in, returning a refresh
// token on success.
func PollDeviceCode(ctx context.Context, loginBase, tenant, clientID string, p *DeviceCodePrompt) (string, error) {
	endpoint := fmt.Sprintf("%s/%s/oauth2/v2.0/token", loginBase, tenant)
	deadline := time.Now().Add(p.ExpiresIn)
	interval := p.Interval

	for {
		if time.Now().After(deadline) {
			return "", fmt.Errorf("sign-in timed out - run the command again")
		}

		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(interval):
		}

		form := url.Values{
			"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
			"client_id":   {clientID},
			"device_code": {p.deviceCode},
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint,
			strings.NewReader(form.Encode()))
		if err != nil {
			return "", err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

		resp, err := (&http.Client{Timeout: httpTimeout}).Do(req)
		if err != nil {
			// Transient network trouble mid-sign-in should not abandon a flow
			// the user is actively completing in their browser.
			continue
		}

		var tr tokenGrantResponse
		decodeErr := json.NewDecoder(resp.Body).Decode(&tr)
		resp.Body.Close()
		if decodeErr != nil {
			continue
		}

		switch tr.Error {
		case "":
			if tr.RefreshToken == "" {
				return "", fmt.Errorf(
					"sign-in succeeded but returned no refresh token; " +
						"check that offline_access is among the app's delegated permissions")
			}
			return tr.RefreshToken, nil

		case "authorization_pending":
			// Expected: the user has not finished yet.

		case "slow_down":
			// The service is asking for more space between polls.
			interval += 5 * time.Second

		case "authorization_declined":
			return "", fmt.Errorf("sign-in was declined in the browser")

		case "expired_token":
			return "", fmt.Errorf("the sign-in code expired - run the command again")

		default:
			return "", fmt.Errorf("sign-in failed: %s: %s", tr.Error, tr.ErrorDesc)
		}
	}
}

// redeemRefreshToken exchanges a refresh token for a fresh access token.
//
// Microsoft rotates refresh tokens, so the new one must be persisted or the
// tool will work until the current token expires and then fail for good.
func (c *Client) redeemRefreshToken(ctx context.Context) (string, error) {
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {c.refreshToken},
		"client_id":     {c.clientID},
		"scope":         {strings.Join(delegatedScopes, " ")},
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
		return "", fmt.Errorf("refreshing sign-in: %w", err)
	}
	defer resp.Body.Close()

	var tr tokenGrantResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return "", fmt.Errorf("decoding refresh response: %w", err)
	}
	if tr.Error != "" {
		return "", fmt.Errorf(
			"the saved sign-in is no longer valid (%s).\nRun:  digest --login", tr.Error)
	}
	if tr.AccessToken == "" {
		return "", fmt.Errorf("refresh returned no access token (HTTP %d)", resp.StatusCode)
	}

	if tr.RefreshToken != "" && tr.RefreshToken != c.refreshToken {
		c.refreshToken = tr.RefreshToken
		if c.onRefreshToken != nil {
			// Persisting is best-effort: failing to save a rotated token is
			// worth a warning, not an aborted run.
			c.onRefreshToken(tr.RefreshToken)
		}
	}

	c.token = tr.AccessToken
	c.tokenExpiry = time.Now().Add(time.Duration(tr.ExpiresIn-120) * time.Second)
	return c.token, nil
}
