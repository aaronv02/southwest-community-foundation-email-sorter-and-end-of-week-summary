package graph

import (
	"strings"
	"time"
)

// EmailAddress is Graph's name+address pair.
type EmailAddress struct {
	Name    string `json:"name"`
	Address string `json:"address"`
}

// Recipient wraps an EmailAddress, as Graph does everywhere.
type Recipient struct {
	EmailAddress EmailAddress `json:"emailAddress"`
}

// FollowupFlag carries the flag state Outlook shows as a coloured flag.
type FollowupFlag struct {
	// "notFlagged", "complete", or "flagged".
	FlagStatus string `json:"flagStatus"`
}

// Message is the subset of a mail message the digest reasons about.
type Message struct {
	ID               string        `json:"id"`
	ConversationID   string        `json:"conversationId"`
	Subject          string        `json:"subject"`
	BodyPreview      string        `json:"bodyPreview"`
	ReceivedDateTime time.Time     `json:"receivedDateTime"`
	SentDateTime     time.Time     `json:"sentDateTime"`
	IsRead           bool          `json:"isRead"`
	IsDraft          bool          `json:"isDraft"`
	HasAttachments   bool          `json:"hasAttachments"`
	Importance       string        `json:"importance"`
	Categories       []string      `json:"categories"`
	From             *Recipient    `json:"from"`
	Sender           *Recipient    `json:"sender"`
	ToRecipients     []Recipient   `json:"toRecipients"`
	CcRecipients     []Recipient   `json:"ccRecipients"`
	Flag             *FollowupFlag `json:"flag"`
	WebLink          string        `json:"webLink"`
}

// FromAddress is the sender address, lowercased, or "" if absent.
func (m Message) FromAddress() string {
	if m.From != nil && m.From.EmailAddress.Address != "" {
		return strings.ToLower(m.From.EmailAddress.Address)
	}
	if m.Sender != nil {
		return strings.ToLower(m.Sender.EmailAddress.Address)
	}
	return ""
}

// FromName is the sender's display name, falling back to the address.
func (m Message) FromName() string {
	if m.From != nil && m.From.EmailAddress.Name != "" {
		return m.From.EmailAddress.Name
	}
	return m.FromAddress()
}

// AddressedTo reports whether addr appears in the To line specifically.
//
// The To/Cc distinction is the crux of the "waiting on you" section: being
// CC'd is being kept informed, being in To is being asked. Treating them the
// same produces a nag list nobody trusts.
func (m Message) AddressedTo(addr string) bool {
	return m.AddressedToAny([]string{addr})
}

// AddressedToAny reports whether any of the given addresses is on the To line.
//
// Multiple addresses matter more than it first appears. At a small nonprofit
// one person often receives mail at several addresses - a personal one, a role
// alias like info@ or grants@, and sometimes a shared mailbox. If the digest
// only recognises one of them, every message sent to the others fails the
// "was she actually asked?" test and the waiting list comes back empty or
// absurdly short, with no error to explain why.
func (m Message) AddressedToAny(addrs []string) bool {
	if len(addrs) == 0 {
		return false
	}
	for _, r := range m.ToRecipients {
		got := strings.ToLower(strings.TrimSpace(r.EmailAddress.Address))
		if got == "" {
			continue
		}
		for _, want := range addrs {
			if got == strings.ToLower(strings.TrimSpace(want)) {
				return true
			}
		}
	}
	return false
}

// IsFlagged reports an active (not completed) follow-up flag.
func (m Message) IsFlagged() bool {
	return m.Flag != nil && m.Flag.FlagStatus == "flagged"
}

// DateTimeTimeZone is Graph's local-time representation for calendar items.
type DateTimeTimeZone struct {
	DateTime string `json:"dateTime"`
	TimeZone string `json:"timeZone"`
}

// Time parses the value in the supplied location.
//
// Graph returns calendar times without an offset, qualified by a separate
// timezone name. Requesting a Prefer: outlook.timezone header makes these
// arrive already in the mailbox's timezone, so parsing them in that location is
// correct.
func (d DateTimeTimeZone) Time(loc *time.Location) time.Time {
	for _, layout := range []string{
		"2006-01-02T15:04:05.0000000",
		"2006-01-02T15:04:05.9999999",
		"2006-01-02T15:04:05",
	} {
		if t, err := time.ParseInLocation(layout, d.DateTime, loc); err == nil {
			return t
		}
	}
	return time.Time{}
}

// ResponseStatus is the mailbox owner's RSVP for an event.
type ResponseStatus struct {
	// "none", "organizer", "tentativelyAccepted", "accepted", "declined",
	// "notResponded".
	Response string `json:"response"`
}

// Attendee is one invitee plus their RSVP.
type Attendee struct {
	Type         string         `json:"type"`
	Status       ResponseStatus `json:"status"`
	EmailAddress EmailAddress   `json:"emailAddress"`
}

// Event is the subset of a calendar entry the digest reasons about.
type Event struct {
	ID               string           `json:"id"`
	Subject          string           `json:"subject"`
	BodyPreview      string           `json:"bodyPreview"`
	Start            DateTimeTimeZone `json:"start"`
	End              DateTimeTimeZone `json:"end"`
	IsAllDay         bool             `json:"isAllDay"`
	IsCancelled      bool             `json:"isCancelled"`
	IsOrganizer      bool             `json:"isOrganizer"`
	ResponseStatus   ResponseStatus   `json:"responseStatus"`
	Organizer        *Recipient       `json:"organizer"`
	Attendees        []Attendee       `json:"attendees"`
	Location         *Location        `json:"location"`
	OnlineMeetingURL string           `json:"onlineMeetingUrl"`
	ShowAs           string           `json:"showAs"`
	Type             string           `json:"type"`
}

// Location is the event's place, when set.
type Location struct {
	DisplayName string `json:"displayName"`
}

// Duration of the event in the given location.
func (e Event) Duration(loc *time.Location) time.Duration {
	start, end := e.Start.Time(loc), e.End.Time(loc)
	if start.IsZero() || end.IsZero() || !end.After(start) {
		return 0
	}
	return end.Sub(start)
}

// OrganizerName is the organizer's display name, or "" when unknown.
func (e Event) OrganizerName() string {
	if e.Organizer == nil {
		return ""
	}
	if e.Organizer.EmailAddress.Name != "" {
		return e.Organizer.EmailAddress.Name
	}
	return e.Organizer.EmailAddress.Address
}

// HumanAttendeeCount counts required and optional people, excluding resources
// such as conference rooms.
func (e Event) HumanAttendeeCount() int {
	n := 0
	for _, a := range e.Attendees {
		if a.Type != "resource" {
			n++
		}
	}
	return n
}
