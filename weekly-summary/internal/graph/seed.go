package graph

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// Write operations used only by the `seed` command to populate a TEST mailbox.
//
// These need Mail.ReadWrite and Calendars.ReadWrite, which are deliberately
// broader than the digest's own read-only permissions. Register a separate app
// in the development tenant for seeding; never grant these to the production
// app that runs on her laptop.

// SeedCategory tags everything the seeder creates so `--wipe` can find it
// again without guessing from subject lines.
const SeedCategory = "SWCF-SEED"

// SeedMessage describes one message to inject.
type SeedMessage struct {
	Subject     string
	Body        string
	FromName    string
	FromAddress string
	Received    time.Time
	IsRead      bool
	Flagged     bool
	// When true the message is placed in Sent Items rather than the inbox.
	Sent bool
	// When set, the message is addressed to this address instead of the mailbox
	// owner - used to simulate being CC'd rather than asked.
	ToOverride string
	CcMailbox  bool
}

// CreateMessage injects a message directly into a folder.
//
// Note the limitation this cannot escape: conversationId is assigned by the
// service and cannot be set. Injected messages therefore do not thread, so
// reply-detection ("did she answer this?") cannot be exercised this way. Use
// SendAs for that - real transport produces real conversation IDs.
func (c *Client) CreateMessage(ctx context.Context, m SeedMessage) error {
	folder := "inbox"
	if m.Sent {
		folder = "sentitems"
	}

	to := c.mailbox
	if m.ToOverride != "" {
		to = m.ToOverride
	}

	payload := map[string]any{
		"subject":      m.Subject,
		"body":         map[string]string{"contentType": "Text", "content": m.Body},
		"from":         map[string]any{"emailAddress": map[string]string{"name": m.FromName, "address": m.FromAddress}},
		"sender":       map[string]any{"emailAddress": map[string]string{"name": m.FromName, "address": m.FromAddress}},
		"toRecipients": []map[string]any{{"emailAddress": map[string]string{"address": to}}},
		"isRead":       m.IsRead,
		"isDraft":      false,
		"categories":   []string{SeedCategory},
	}

	if m.CcMailbox {
		payload["ccRecipients"] = []map[string]any{
			{"emailAddress": map[string]string{"address": c.mailbox}},
		}
	}
	if !m.Received.IsZero() {
		payload["receivedDateTime"] = m.Received.UTC().Format(time.RFC3339)
		payload["sentDateTime"] = m.Received.UTC().Format(time.RFC3339)
	}
	if m.Flagged {
		payload["flag"] = map[string]string{"flagStatus": "flagged"}
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	endpoint := fmt.Sprintf("%s/users/%s/mailFolders/%s/messages",
		c.graphBase, url.PathEscape(c.mailbox), folder)
	_, err = c.do(ctx, http.MethodPost, endpoint, body)
	return err
}

// SendAs sends real mail from another mailbox to the analyzed one.
//
// Slower than injection and requires a second user, but it is the only way to
// produce genuine conversation threading - which is what the "still waiting on
// you" logic actually keys on.
func (c *Client) SendAs(ctx context.Context, fromMailbox, subject, bodyText string) error {
	payload, err := json.Marshal(sendMailRequest{
		Message: outgoing{
			Subject:      subject,
			Body:         body{ContentType: "Text", Content: bodyText},
			ToRecipients: []Recipient{{EmailAddress: EmailAddress{Address: c.mailbox}}},
		},
		SaveToSentItems: false,
	})
	if err != nil {
		return err
	}

	endpoint := fmt.Sprintf("%s/users/%s/sendMail", c.graphBase, url.PathEscape(fromMailbox))
	_, err = c.do(ctx, http.MethodPost, endpoint, payload)
	return err
}

// ReplyTo sends a real reply from the analyzed mailbox, joining the thread.
func (c *Client) ReplyTo(ctx context.Context, messageID, comment string) error {
	payload, err := json.Marshal(map[string]string{"comment": comment})
	if err != nil {
		return err
	}
	endpoint := fmt.Sprintf("%s/users/%s/messages/%s/reply",
		c.graphBase, url.PathEscape(c.mailbox), url.PathEscape(messageID))
	_, err = c.do(ctx, http.MethodPost, endpoint, payload)
	return err
}

// SeedEvent describes one calendar entry to inject.
type SeedEvent struct {
	Subject   string
	Start     time.Time
	End       time.Time
	Organizer bool
	// One of: accepted, declined, notResponded, tentativelyAccepted.
	Response  string
	Attendees int
	AllDay    bool
}

// CreateEvent injects a calendar entry.
//
// responseStatus is read-only on create, so an injected event always looks
// organizer-owned. Testing the "never responded to an invite" path needs a real
// invitation sent from another user, same as with mail threading.
func (c *Client) CreateEvent(ctx context.Context, e SeedEvent, tz string) error {
	attendees := make([]map[string]any, 0, e.Attendees)
	for i := range e.Attendees {
		attendees = append(attendees, map[string]any{
			"type": "required",
			"emailAddress": map[string]string{
				"address": fmt.Sprintf("attendee%d@example.org", i+1),
				"name":    fmt.Sprintf("Attendee %d", i+1),
			},
		})
	}

	payload := map[string]any{
		"subject":    e.Subject,
		"start":      map[string]string{"dateTime": e.Start.Format("2006-01-02T15:04:05"), "timeZone": tz},
		"end":        map[string]string{"dateTime": e.End.Format("2006-01-02T15:04:05"), "timeZone": tz},
		"isAllDay":   e.AllDay,
		"categories": []string{SeedCategory},
		"attendees":  attendees,
		// Do not actually email the fictional attendees.
		"responseRequested": false,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	endpoint := fmt.Sprintf("%s/users/%s/events", c.graphBase, url.PathEscape(c.mailbox))
	_, err = c.do(ctx, http.MethodPost, endpoint, body)
	return err
}

// SeededMessages finds everything the seeder created.
func (c *Client) SeededMessages(ctx context.Context) ([]Message, error) {
	path := fmt.Sprintf(
		"/users/%s/messages?$select=id,subject,categories&$filter=categories/any(c:c eq '%s')&$top=100",
		url.PathEscape(c.mailbox), SeedCategory)
	return listPaged[Message](ctx, c, encodeQuery(path))
}

// SeededEvents finds calendar entries the seeder created.
func (c *Client) SeededEvents(ctx context.Context) ([]Event, error) {
	path := fmt.Sprintf(
		"/users/%s/events?$select=id,subject,categories&$filter=categories/any(c:c eq '%s')&$top=100",
		url.PathEscape(c.mailbox), SeedCategory)
	return listPaged[Event](ctx, c, encodeQuery(path))
}

// DeleteMessage removes one message.
func (c *Client) DeleteMessage(ctx context.Context, id string) error {
	endpoint := fmt.Sprintf("%s/users/%s/messages/%s",
		c.graphBase, url.PathEscape(c.mailbox), url.PathEscape(id))
	_, err := c.do(ctx, http.MethodDelete, endpoint, nil)
	return err
}

// DeleteEvent removes one calendar entry.
func (c *Client) DeleteEvent(ctx context.Context, id string) error {
	endpoint := fmt.Sprintf("%s/users/%s/events/%s",
		c.graphBase, url.PathEscape(c.mailbox), url.PathEscape(id))
	_, err := c.do(ctx, http.MethodDelete, endpoint, nil)
	return err
}
