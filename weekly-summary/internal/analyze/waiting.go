package analyze

import (
	"sort"
	"strings"
	"time"

	"swcf/digest/internal/graph"
)

// WaitingItem is one message that appears to still need a reply.
type WaitingItem struct {
	Message graph.Message
	Age     time.Duration
}

// AgeDays is the whole number of days the message has gone unanswered.
func (w WaitingItem) AgeDays() int {
	return int(w.Age.Hours() / 24)
}

// Waiting finds mail that was addressed to her and never answered.
//
// This is the section with the most value and the most ways to be wrong. A
// nag list that includes newsletters, CCs, and things already handled gets
// ignored within two weeks, so every filter here exists to protect precision at
// the cost of recall. Better to under-report than to cry wolf.
//
// A message is "waiting" when all of the following hold:
//   - she is on the To line, not merely CC'd (being copied is not being asked)
//   - the sender looks like a person, not a mailing list or robot
//   - nothing was sent from her mailbox in that conversation after it arrived
//   - it is older than the grace period, so this morning's mail is not a failure
//
// sent should reach further back than inbox: a reply sent last week still
// answers a message received last week.
// addresses is every address she might legitimately be reached at - her
// primary plus any role aliases or shared mailboxes. The first entry is
// treated as her own identity for the "not from herself" check.
func Waiting(
	inbox []graph.Message,
	sent []graph.Message,
	addresses []string,
	grace time.Duration,
	now time.Time,
	ignoredPatterns []string,
) []WaitingItem {
	own := make(map[string]bool, len(addresses))
	for _, a := range addresses {
		if a = strings.ToLower(strings.TrimSpace(a)); a != "" {
			own[a] = true
		}
	}

	// Latest outbound activity per conversation.
	repliedAt := make(map[string]time.Time, len(sent))
	for _, m := range sent {
		if m.ConversationID == "" {
			continue
		}
		when := m.SentDateTime
		if when.IsZero() {
			when = m.ReceivedDateTime
		}
		if existing, ok := repliedAt[m.ConversationID]; !ok || when.After(existing) {
			repliedAt[m.ConversationID] = when
		}
	}

	var items []WaitingItem
	// Conversation ID -> index into items, so a thread can be collapsed to its
	// oldest message rather than whichever one happened to be seen first.
	byConversation := make(map[string]int)

	for _, m := range inbox {
		if m.IsDraft {
			continue
		}
		from := m.FromAddress()
		if from == "" || own[from] {
			continue
		}
		if !m.AddressedToAny(addresses) {
			continue
		}
		if isAutomatedSender(from, ignoredPatterns) {
			continue
		}

		age := now.Sub(m.ReceivedDateTime)
		if age < grace {
			continue
		}
		if replied, ok := repliedAt[m.ConversationID]; ok && replied.After(m.ReceivedDateTime) {
			continue
		}
		// One entry per thread: a five-message thread she never answered is one
		// thing to do, not five. Keep the OLDEST message as the representative,
		// because the honest answer to "how long have they been waiting?" is
		// measured from the first unanswered ask, not the latest nudge.
		if m.ConversationID != "" {
			if idx, seen := byConversation[m.ConversationID]; seen {
				if m.ReceivedDateTime.Before(items[idx].Message.ReceivedDateTime) {
					items[idx] = WaitingItem{Message: m, Age: age}
				}
				continue
			}
			byConversation[m.ConversationID] = len(items)
		}

		items = append(items, WaitingItem{Message: m, Age: age})
	}

	// Oldest first. The thing that has been sitting longest is the thing most
	// likely to have become a problem.
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].Message.ReceivedDateTime.Before(items[j].Message.ReceivedDateTime)
	})
	return items
}

// isAutomatedSender reports whether an address looks like bulk or robot mail.
//
// Matching is anchored rather than a naive substring test, because a substring
// test is quietly dangerous here: the pattern "news@" would also swallow
// "goodnews@apersonsdomain.org", and silently dropping a real person from the
// waiting list is the worst failure this tool can have.
//
// Three pattern shapes are supported:
//   - "@example.org"  matches anywhere in the domain
//   - "news@"         matches the local part, anchored at its start
//   - "bounce"        falls back to a plain substring match
func isAutomatedSender(address string, patterns []string) bool {
	address = strings.ToLower(strings.TrimSpace(address))
	if address == "" {
		return false
	}

	local, domain := address, ""
	if at := strings.LastIndex(address, "@"); at >= 0 {
		local, domain = address[:at], address[at:]
	}

	for _, raw := range patterns {
		p := strings.ToLower(strings.TrimSpace(raw))
		if p == "" {
			continue
		}

		switch {
		case strings.HasPrefix(p, "@"):
			if strings.Contains(domain, p) {
				return true
			}
		case strings.HasSuffix(p, "@"):
			if localPartMatches(local, strings.TrimSuffix(p, "@")) {
				return true
			}
		default:
			if strings.Contains(address, p) {
				return true
			}
		}
	}
	return false
}

// localPartMatches reports whether local is base, or base followed by a
// separator - so "noreply" matches "noreply-service" and "noreply.2" but not
// "noreplyingtoyou".
func localPartMatches(local, base string) bool {
	if base == "" {
		return false
	}
	if local == base {
		return true
	}
	if !strings.HasPrefix(local, base) {
		return false
	}
	return strings.ContainsRune("-._+", rune(local[len(base)]))
}
