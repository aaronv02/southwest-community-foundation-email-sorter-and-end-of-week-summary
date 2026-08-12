package graph

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// Outlook category support.
//
// A category MUST already exist in the mailbox's master list before it can be
// applied to a message. Skipping that step makes every write silently useless -
// the request succeeds and no label appears - so EnsureCategories runs before
// any labelling.

// OutlookCategory is an entry in the mailbox master category list.
type OutlookCategory struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	Color       string `json:"color"`
}

// MasterCategories lists the categories defined on the mailbox.
func (c *Client) MasterCategories(ctx context.Context) ([]OutlookCategory, error) {
	path := fmt.Sprintf("/users/%s/outlook/masterCategories", url.PathEscape(c.mailbox))
	return listPaged[OutlookCategory](ctx, c, path)
}

// EnsureCategories creates any of the wanted categories the mailbox lacks.
//
// Returns the names it created. Creation is sequential: the master list is one
// small resource, and parallel writes to it invite conflicts for no useful
// speedup.
func (c *Client) EnsureCategories(ctx context.Context, wanted []struct{ Name, Color string }) ([]string, error) {
	existing, err := c.MasterCategories(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading the mailbox category list: %w", err)
	}

	have := make(map[string]bool, len(existing))
	for _, e := range existing {
		have[strings.ToLower(strings.TrimSpace(e.DisplayName))] = true
	}

	var created []string
	endpoint := fmt.Sprintf("%s/users/%s/outlook/masterCategories",
		c.graphBase, url.PathEscape(c.mailbox))

	for _, w := range wanted {
		if have[strings.ToLower(strings.TrimSpace(w.Name))] {
			continue
		}
		body, err := json.Marshal(map[string]string{
			"displayName": w.Name,
			"color":       w.Color,
		})
		if err != nil {
			return created, err
		}
		if _, err := c.do(ctx, http.MethodPost, endpoint, body); err != nil {
			// A duplicate name is harmless and can happen if another client
			// created it between our read and write.
			if apiErr, ok := err.(*APIError); ok && apiErr.Status == http.StatusConflict {
				continue
			}
			return created, fmt.Errorf("creating category %q: %w", w.Name, err)
		}
		created = append(created, w.Name)
	}
	return created, nil
}

// LabelChange is one message's new category set.
type LabelChange struct {
	MessageID string
	// The complete replacement set. Graph overwrites rather than merges, so
	// any category the user applied by hand must be carried across here or it
	// disappears.
	Categories []string
}

// LabelResult reports what happened, per message.
type LabelResult struct {
	Succeeded []string
	Failed    map[string]string
}

// ApplyLabels writes categories to many messages using JSON batching.
//
// One message failing never aborts the rest: partial success is normal and
// worth reporting honestly rather than rolling back a whole sort.
func (c *Client) ApplyLabels(ctx context.Context, changes []LabelChange) (LabelResult, error) {
	result := LabelResult{Failed: map[string]string{}}
	if len(changes) == 0 {
		return result, nil
	}

	const batchSize = 20
	endpoint := c.graphBase + "/$batch"

	for start := 0; start < len(changes); start += batchSize {
		end := min(start+batchSize, len(changes))
		batch := changes[start:end]

		requests := make([]map[string]any, 0, len(batch))
		for i, ch := range batch {
			requests = append(requests, map[string]any{
				"id":      fmt.Sprint(i),
				"method":  "PATCH",
				"url":     fmt.Sprintf("/users/%s/messages/%s", c.mailbox, ch.MessageID),
				"headers": map[string]string{"Content-Type": "application/json"},
				"body":    map[string]any{"categories": ch.Categories},
			})
		}

		payload, err := json.Marshal(map[string]any{"requests": requests})
		if err != nil {
			return result, err
		}

		raw, err := c.do(ctx, http.MethodPost, endpoint, payload)
		if err != nil {
			for _, ch := range batch {
				result.Failed[ch.MessageID] = err.Error()
			}
			continue
		}

		var envelope struct {
			Responses []struct {
				ID     string          `json:"id"`
				Status int             `json:"status"`
				Body   json.RawMessage `json:"body"`
			} `json:"responses"`
		}
		if err := json.Unmarshal(raw, &envelope); err != nil {
			return result, fmt.Errorf("decoding batch response: %w", err)
		}

		for _, r := range envelope.Responses {
			var idx int
			if _, err := fmt.Sscanf(r.ID, "%d", &idx); err != nil || idx < 0 || idx >= len(batch) {
				continue
			}
			id := batch[idx].MessageID
			if r.Status >= 200 && r.Status < 300 {
				result.Succeeded = append(result.Succeeded, id)
			} else {
				reason := fmt.Sprintf("status %d", r.Status)
				if r.Status == http.StatusForbidden {
					reason += " (the app may lack the Mail.ReadWrite permission)"
				}
				result.Failed[id] = reason
			}
		}
	}

	return result, nil
}

// MessageByID fetches one full message, including its body.
func (c *Client) MessageByID(ctx context.Context, id string) (*Message, error) {
	endpoint := fmt.Sprintf("%s/users/%s/messages/%s?$select=%s,body",
		c.graphBase, url.PathEscape(c.mailbox), url.PathEscape(id), messageSelect)

	raw, err := c.do(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}

	var m struct {
		Message
		Body struct {
			ContentType string `json:"contentType"`
			Content     string `json:"content"`
		} `json:"body"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("decoding message: %w", err)
	}

	out := m.Message
	// Prefer the full body over the short preview when one is available.
	if m.Body.Content != "" {
		out.BodyPreview = m.Body.Content
		if strings.EqualFold(m.Body.ContentType, "html") {
			out.BodyPreview = stripHTML(m.Body.Content)
		}
	}
	return &out, nil
}

// stripHTML reduces an HTML body to readable text.
//
// Deliberately crude rather than a real parser: the output is read by a
// language model, which copes fine with imperfect whitespace, and the point is
// to cut the token cost of markup by an order of magnitude.
func stripHTML(s string) string {
	var b strings.Builder
	depth := 0
	for _, r := range s {
		switch {
		case r == '<':
			depth++
		case r == '>':
			if depth > 0 {
				depth--
			}
			b.WriteRune(' ')
		case depth == 0:
			b.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}
