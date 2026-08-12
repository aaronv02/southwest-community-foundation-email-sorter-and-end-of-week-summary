package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"swcf/digest/internal/analyze"
	"swcf/digest/internal/config"
	"swcf/digest/internal/graph"
	"swcf/digest/internal/labels"
)

// MCP server mode: lets Claude Desktop read and label this mailbox.
//
// The protocol is JSON-RPC 2.0, one message per line, over stdin and stdout.
//
// CRITICAL: stdout carries the protocol and nothing else. A stray fmt.Println
// anywhere in this path corrupts the stream and Claude reports the connection
// as broken with no useful error. All diagnostics go to stderr.

const (
	mcpServerName = "swcf-outlook"
	// Protocol revisions this server understands. The client's requested
	// version is echoed back when recognised, otherwise the default is used.
	defaultProtocolVersion = "2024-11-05"
)

var supportedProtocolVersions = map[string]bool{
	"2024-11-05": true,
	"2025-03-26": true,
	"2025-06-18": true,
}

// How much mail each tool is willing to pull. These caps exist to protect her
// Claude usage allowance: a full-inbox sort would be tens of thousands of
// tokens in one shot, and an agent that exhausts her limit mid-task is worse
// than one that works in batches and says how many are left.
// waitingLookback and sentLookback are shared with the scheduled digest, in
// main.go, so the two delivery paths never disagree about what counts as
// unanswered.
const (
	maxSuggestBatch = 50
	searchLookback  = 90 * 24 * time.Hour
)

// ---------------------------------------------------------------------------
// Protocol plumbing
// ---------------------------------------------------------------------------

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type toolContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type toolResult struct {
	Content []toolContent `json:"content"`
	IsError bool          `json:"isError,omitempty"`
}

// refStore maps short handles to messages.
//
// Graph message IDs are ~150 characters of base64. Handing fifty of them to a
// model and getting them back costs thousands of tokens for no benefit, so
// tools emit "m1", "m2" instead and this remembers what they point at. Keeping
// the whole message also means applying a label does not need a second fetch,
// and lets existing categories be preserved rather than overwritten.
type refStore struct {
	mu   sync.Mutex
	byID map[string]graph.Message
	next int
}

func newRefStore() *refStore {
	return &refStore{byID: map[string]graph.Message{}}
}

func (r *refStore) put(m graph.Message) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.next++
	ref := fmt.Sprintf("m%d", r.next)
	r.byID[ref] = m
	return ref
}

func (r *refStore) get(ref string) (graph.Message, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	m, ok := r.byID[strings.TrimSpace(ref)]
	return m, ok
}

type mcpServer struct {
	cfg    *config.Config
	client *graph.Client
	refs   *refStore
	out    *bufio.Writer
	writeM sync.Mutex
}

// runMCP serves the protocol until stdin closes.
func runMCP(version string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	client, err := newClient(cfg)
	if err != nil {
		return err
	}

	s := &mcpServer{
		cfg:    cfg,
		client: client,
		refs:   newRefStore(),
		out:    bufio.NewWriter(os.Stdout),
	}

	fmt.Fprintf(os.Stderr, "%s %s ready for mailbox %s\n", mcpServerName, version, cfg.Mailbox)

	scanner := bufio.NewScanner(os.Stdin)
	// Tool arguments can be large; the 64KB default is not enough.
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var req rpcRequest
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			fmt.Fprintf(os.Stderr, "malformed message: %v\n", err)
			continue
		}
		s.handle(&req, version)
	}

	if err := scanner.Err(); err != nil && err != io.EOF {
		return err
	}
	return nil
}

func (s *mcpServer) handle(req *rpcRequest, version string) {
	// A request without an id is a notification: act on it, never reply.
	isNotification := len(req.ID) == 0 || string(req.ID) == "null"

	switch req.Method {
	case "initialize":
		s.reply(req, s.initializeResult(req, version))

	case "notifications/initialized", "notifications/cancelled":
		// Nothing to do, and nothing to say.

	case "ping":
		if !isNotification {
			s.reply(req, map[string]any{})
		}

	case "tools/list":
		s.reply(req, map[string]any{"tools": toolDefinitions()})

	case "tools/call":
		s.reply(req, s.callTool(req.Params))

	default:
		if !isNotification {
			s.replyError(req, -32601, "unknown method: "+req.Method)
		}
	}
}

func (s *mcpServer) initializeResult(req *rpcRequest, version string) map[string]any {
	protocol := defaultProtocolVersion
	var params struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := json.Unmarshal(req.Params, &params); err == nil {
		if supportedProtocolVersions[params.ProtocolVersion] {
			protocol = params.ProtocolVersion
		}
	}

	return map[string]any{
		"protocolVersion": protocol,
		"capabilities":    map[string]any{"tools": map[string]any{}},
		"serverInfo": map[string]any{
			"name":    mcpServerName,
			"version": version,
		},
	}
}

func (s *mcpServer) reply(req *rpcRequest, result any) {
	if len(req.ID) == 0 {
		return
	}
	s.write(rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: result})
}

func (s *mcpServer) replyError(req *rpcRequest, code int, msg string) {
	if len(req.ID) == 0 {
		return
	}
	s.write(rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: code, Message: msg}})
}

func (s *mcpServer) write(resp rpcResponse) {
	s.writeM.Lock()
	defer s.writeM.Unlock()

	payload, err := json.Marshal(resp)
	if err != nil {
		fmt.Fprintf(os.Stderr, "could not encode response: %v\n", err)
		return
	}
	s.out.Write(payload)
	s.out.WriteByte('\n')
	s.out.Flush()
}

// ---------------------------------------------------------------------------
// Tool definitions
// ---------------------------------------------------------------------------

func toolDefinitions() []map[string]any {
	obj := func(props map[string]any, required ...string) map[string]any {
		schema := map[string]any{"type": "object", "properties": props}
		if len(required) > 0 {
			schema["required"] = required
		}
		return schema
	}

	return []map[string]any{
		{
			"name": "whats_waiting",
			"description": "Emails still waiting on a reply: sent directly to her (not CC'd), " +
				"from a real person rather than a mailing list, older than the grace period, and " +
				"never answered. Oldest first. Use this for questions like 'what am I " +
				"forgetting?', 'what do I owe people?', or 'who am I ignoring?'.",
			"inputSchema": obj(map[string]any{
				"min_days_old": map[string]any{
					"type":        "integer",
					"description": "Only show items older than this many days. Default 2.",
				},
			}),
		},
		{
			"name": "weekly_summary",
			"description": "The full weekly summary: what is waiting, what went unread, meetings " +
				"and mail volume for the week, and the week ahead. Same content as the Friday " +
				"digest email. Use for 'how was my week?' or 'catch me up'.",
			"inputSchema": obj(map[string]any{}),
		},
		{
			"name": "whats_next_week",
			"description": "The coming week's calendar plus any meeting invitations she has not " +
				"responded to. Use for 'what's coming up?' or 'what do I need to prepare for?'.",
			"inputSchema": obj(map[string]any{}),
		},
		{
			"name": "find_mail",
			"description": "Search her mail by sender, subject text, or age. Searches both " +
				"received and sent mail. Use for 'anything from Tessa?', 'what did I tell the " +
				"board about the audit?', or 'show me unread grant mail'.",
			"inputSchema": obj(map[string]any{
				"from": map[string]any{
					"type":        "string",
					"description": "Match sender name or address, partial and case-insensitive.",
				},
				"subject": map[string]any{
					"type":        "string",
					"description": "Match subject text, partial and case-insensitive.",
				},
				"days": map[string]any{
					"type":        "integer",
					"description": "How far back to look. Default 30, maximum 90.",
				},
				"unread_only": map[string]any{
					"type":        "boolean",
					"description": "Only messages she has not opened.",
				},
				"include_sent": map[string]any{
					"type":        "boolean",
					"description": "Include mail she sent. Useful for 'what did I say to them?'.",
				},
			}),
		},
		{
			"name": "read_message",
			"description": "The full text of one message, using a reference like 'm3' from an " +
				"earlier result. Use before drafting a reply, or when the subject line is not " +
				"enough to answer her question.",
			"inputSchema": obj(map[string]any{
				"ref": map[string]any{
					"type":        "string",
					"description": "A message reference such as 'm3'.",
				},
			}, "ref"),
		},
		{
			"name": "list_categories",
			"description": "The labels available for sorting, with a description of what belongs " +
				"in each. Read this before suggesting labels so the descriptions guide the choice.",
			"inputSchema": obj(map[string]any{}),
		},
		{
			"name": "suggest_labels",
			"description": "Fetches recent unlabelled mail so you can propose a category for each. " +
				"THIS CHANGES NOTHING - it only reads. Decide a label for each message using the " +
				"category descriptions, show her the proposals, and only call apply_labels once " +
				"she agrees. If a message genuinely fits no category, or two fit equally well, " +
				"propose 'needs-review' rather than guessing.",
			"inputSchema": obj(map[string]any{
				"count": map[string]any{
					"type":        "integer",
					"description": "How many messages to fetch. Default 25, maximum 50.",
				},
			}),
		},
		{
			"name": "apply_labels",
			"description": "Writes labels to specific messages. ONLY call this after showing her " +
				"the proposed labels and getting explicit agreement. Never call it in the same " +
				"turn as suggest_labels. Labels are reversible and nothing is moved or deleted, " +
				"but she should always see what will change first.",
			"inputSchema": obj(map[string]any{
				"labels": map[string]any{
					"type":        "array",
					"description": "The messages to label and the category for each.",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"ref": map[string]any{
								"type":        "string",
								"description": "Message reference such as 'm3'.",
							},
							"category": map[string]any{
								"type":        "string",
								"description": "Category id, e.g. 'grants' or 'donors'.",
							},
						},
						"required": []string{"ref", "category"},
					},
				},
			}, "labels"),
		},
	}
}

// ---------------------------------------------------------------------------
// Tool dispatch
// ---------------------------------------------------------------------------

func (s *mcpServer) callTool(raw json.RawMessage) toolResult {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(raw, &params); err != nil {
		return errResult("could not read the tool call: " + err.Error())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	var (
		text string
		err  error
	)

	switch params.Name {
	case "whats_waiting":
		text, err = s.toolWhatsWaiting(ctx, params.Arguments)
	case "weekly_summary":
		text, err = s.toolWeeklySummary(ctx)
	case "whats_next_week":
		text, err = s.toolNextWeek(ctx)
	case "find_mail":
		text, err = s.toolFindMail(ctx, params.Arguments)
	case "read_message":
		text, err = s.toolReadMessage(ctx, params.Arguments)
	case "list_categories":
		text, err = describeCategories(), nil
	case "suggest_labels":
		text, err = s.toolSuggestLabels(ctx, params.Arguments)
	case "apply_labels":
		text, err = s.toolApplyLabels(ctx, params.Arguments)
	default:
		return errResult("unknown tool: " + params.Name)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "tool %s failed: %v\n", params.Name, err)
		return errResult(err.Error())
	}
	return toolResult{Content: []toolContent{{Type: "text", Text: text}}}
}

func errResult(msg string) toolResult {
	return toolResult{
		Content: []toolContent{{Type: "text", Text: msg}},
		IsError: true,
	}
}

// ---------------------------------------------------------------------------
// Fetch helpers
// ---------------------------------------------------------------------------

func (s *mcpServer) now() time.Time {
	return time.Now().In(s.cfg.Location())
}

func (s *mcpServer) fetchInboxAndSent(ctx context.Context, since time.Time) ([]graph.Message, []graph.Message, error) {
	inbox, err := s.client.InboxSince(ctx, since)
	if err != nil {
		return nil, nil, fmt.Errorf("reading the inbox: %w", err)
	}
	sent, err := s.client.SentSince(ctx, since.Add(-sentLookback+waitingLookback))
	if err != nil {
		return nil, nil, fmt.Errorf("reading sent mail: %w", err)
	}
	return inbox, sent, nil
}

// ---------------------------------------------------------------------------
// Tools
// ---------------------------------------------------------------------------

func (s *mcpServer) toolWhatsWaiting(ctx context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		MinDaysOld int `json:"min_days_old"`
	}
	_ = json.Unmarshal(raw, &args)

	grace := time.Duration(s.cfg.WaitingGraceHours) * time.Hour
	if args.MinDaysOld > 0 {
		grace = time.Duration(args.MinDaysOld) * 24 * time.Hour
	}

	now := s.now()
	inbox, sent, err := s.fetchInboxAndSent(ctx, now.Add(-waitingLookback))
	if err != nil {
		return "", err
	}

	items := analyze.Waiting(inbox, sent, s.cfg.Addresses(), grace, now, s.cfg.IgnoredSenderPatterns)
	if len(items) == 0 {
		return "Nothing is waiting on a reply. Every email addressed directly to her has " +
			"either been answered or is newer than the grace period.", nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%d email(s) waiting on a reply, oldest first:\n\n", len(items))
	for _, it := range items {
		ref := s.refs.put(it.Message)
		fmt.Fprintf(&b, "[%s] %s\n     from %s · waiting %d day(s)",
			ref, subjectOf(it.Message), it.Message.FromName(), it.AgeDays())
		if it.Message.HasAttachments {
			b.WriteString(" · has attachments")
		}
		if it.Message.IsFlagged() {
			b.WriteString(" · flagged")
		}
		b.WriteString("\n")
		if preview := strings.TrimSpace(it.Message.BodyPreview); preview != "" {
			fmt.Fprintf(&b, "     %s\n", truncateRunes(preview, 160))
		}
		b.WriteString("\n")
	}
	b.WriteString("Use read_message with a reference like " +
		"[m1] to see the full text before drafting a reply.")
	return b.String(), nil
}

func (s *mcpServer) toolWeeklySummary(ctx context.Context) (string, error) {
	loc := s.cfg.Location()
	now := s.now()
	window := analyze.ResolveWindow(now, loc, "")

	inbox, sent, err := s.fetchInboxAndSent(ctx, window.Start.Add(-waitingLookback))
	if err != nil {
		return "", err
	}
	events, err := s.client.CalendarView(ctx, window.Start, window.NextEnd)
	if err != nil {
		return "", fmt.Errorf("reading the calendar: %w", err)
	}

	d := analyze.Build(
		analyze.Input{Inbox: inbox, Sent: sent, Events: events},
		window,
		analyze.Options{
			Mailbox:         s.cfg.Mailbox,
			Addresses:       s.cfg.Addresses(),
			Location:        loc,
			Now:             now,
			WaitingGrace:    time.Duration(s.cfg.WaitingGraceHours) * time.Hour,
			IgnoredPatterns: s.cfg.IgnoredSenderPatterns,
		},
	)

	return s.renderDigest(d), nil
}

// renderDigest writes the summary as plain text for a conversation, rather than
// the HTML the email uses.
func (s *mcpServer) renderDigest(d analyze.Digest) string {
	loc := d.Location
	var b strings.Builder

	fmt.Fprintf(&b, "WEEK OF %s\n\n", d.Window.Describe())

	b.WriteString("STILL WAITING ON YOU\n")
	if len(d.Waiting) == 0 {
		b.WriteString("  Nothing outstanding.\n")
	} else {
		for _, it := range d.Waiting {
			ref := s.refs.put(it.Message)
			fmt.Fprintf(&b, "  [%s] %s — %s, waiting %d day(s)\n",
				ref, subjectOf(it.Message), it.Message.FromName(), it.AgeDays())
		}
	}

	if len(d.Flagged) > 0 {
		b.WriteString("\nFLAGGED AND STILL OPEN\n")
		for _, m := range d.Flagged {
			ref := s.refs.put(m)
			fmt.Fprintf(&b, "  [%s] %s — %s\n", ref, subjectOf(m), m.FromName())
		}
	}

	b.WriteString("\nUNREAD\n")
	if d.Unread.Total == 0 {
		b.WriteString("  Inbox fully read.\n")
	} else {
		fmt.Fprintf(&b, "  %d unread", d.Unread.Total)
		if d.Unread.StaleCount > 0 {
			fmt.Fprintf(&b, ", %d more than a week old", d.Unread.StaleCount)
		}
		b.WriteString("\n")
		for _, g := range d.Unread.People {
			fmt.Fprintf(&b, "    %s — %d\n", g.Name, g.Count)
		}
		if len(d.Unread.Automated) > 0 {
			total := 0
			for _, g := range d.Unread.Automated {
				total += g.Count
			}
			fmt.Fprintf(&b, "    (%d newsletters and automated)\n", total)
		}
	}

	fmt.Fprintf(&b, "\nYOUR WEEK\n  %d meeting(s)", d.Review.MeetingsHeld)
	if d.Review.MeetingHours > 0 {
		fmt.Fprintf(&b, ", %.1f hours", d.Review.MeetingHours)
	}
	fmt.Fprintf(&b, "\n  %d email(s) sent to %d person/people\n",
		d.Review.EmailsSent, d.Review.PeopleWrittenTo)
	if d.Review.BusiestDay != "" {
		fmt.Fprintf(&b, "  Busiest day: %s\n", d.Review.BusiestDay)
	}

	if len(d.Calendar.NextWeekUnanswered) > 0 {
		b.WriteString("\nAWAITING YOUR RSVP\n")
		for _, e := range d.Calendar.NextWeekUnanswered {
			fmt.Fprintf(&b, "  %s — %s\n", e.Subject, e.Start.Time(loc).Format("Mon 2 Jan 3:04 PM"))
		}
	}

	if len(d.Calendar.NextWeek) > 0 {
		b.WriteString("\nWEEK AHEAD\n")
		for _, e := range d.Calendar.NextWeek {
			fmt.Fprintf(&b, "  %s — %s\n", e.Subject, e.Start.Time(loc).Format("Mon 2 Jan 3:04 PM"))
		}
	}

	b.WriteString("\nThis only covers what is visible in Outlook. It cannot see calls, " +
		"site visits, or work done in other systems, and it reports how she RSVP'd rather " +
		"than whether she attended.")
	return b.String()
}

func (s *mcpServer) toolNextWeek(ctx context.Context) (string, error) {
	loc := s.cfg.Location()
	now := s.now()
	window := analyze.ResolveWindow(now, loc, "")

	events, err := s.client.CalendarView(ctx, window.Start, window.NextEnd)
	if err != nil {
		return "", fmt.Errorf("reading the calendar: %w", err)
	}
	gaps := analyze.BuildCalendarGaps(events, loc, window)

	var b strings.Builder
	if len(gaps.NextWeekUnanswered) > 0 {
		b.WriteString("INVITATIONS AWAITING HER RSVP\n")
		for _, e := range gaps.NextWeekUnanswered {
			fmt.Fprintf(&b, "  %s — %s", e.Subject, e.Start.Time(loc).Format("Mon 2 Jan, 3:04 PM"))
			if name := e.OrganizerName(); name != "" {
				fmt.Fprintf(&b, ", from %s", name)
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	if len(gaps.NextWeek) == 0 {
		b.WriteString("Nothing scheduled next week yet.")
		return b.String(), nil
	}

	fmt.Fprintf(&b, "NEXT WEEK — %d event(s)", len(gaps.NextWeek))
	if gaps.NextWeekHours > 0 {
		fmt.Fprintf(&b, ", %.1f hours committed", gaps.NextWeekHours)
	}
	b.WriteString("\n")
	for _, e := range gaps.NextWeek {
		fmt.Fprintf(&b, "  %s — %s", e.Subject, e.Start.Time(loc).Format("Mon 2 Jan, 3:04 PM"))
		if n := e.HumanAttendeeCount(); n > 1 {
			fmt.Fprintf(&b, ", %d people", n)
		}
		b.WriteString("\n")
	}
	return b.String(), nil
}

func (s *mcpServer) toolFindMail(ctx context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		From        string `json:"from"`
		Subject     string `json:"subject"`
		Days        int    `json:"days"`
		UnreadOnly  bool   `json:"unread_only"`
		IncludeSent bool   `json:"include_sent"`
	}
	_ = json.Unmarshal(raw, &args)

	if args.From == "" && args.Subject == "" && !args.UnreadOnly {
		return "", fmt.Errorf("give at least one of: from, subject, or unread_only")
	}

	days := args.Days
	if days <= 0 {
		days = 30
	}
	if days > 90 {
		days = 90
	}
	since := s.now().Add(-time.Duration(days) * 24 * time.Hour)
	if since.Before(s.now().Add(-searchLookback)) {
		since = s.now().Add(-searchLookback)
	}

	inbox, err := s.client.InboxSince(ctx, since)
	if err != nil {
		return "", fmt.Errorf("reading the inbox: %w", err)
	}
	pool := inbox
	if args.IncludeSent {
		sent, err := s.client.SentSince(ctx, since)
		if err != nil {
			return "", fmt.Errorf("reading sent mail: %w", err)
		}
		pool = append(pool, sent...)
	}

	from := strings.ToLower(args.From)
	subject := strings.ToLower(args.Subject)

	var hits []graph.Message
	for _, m := range pool {
		if args.UnreadOnly && m.IsRead {
			continue
		}
		if from != "" &&
			!strings.Contains(strings.ToLower(m.FromAddress()), from) &&
			!strings.Contains(strings.ToLower(m.FromName()), from) {
			continue
		}
		if subject != "" && !strings.Contains(strings.ToLower(m.Subject), subject) {
			continue
		}
		hits = append(hits, m)
	}

	if len(hits) == 0 {
		return "Nothing matched.", nil
	}

	sort.SliceStable(hits, func(i, j int) bool {
		return receivedOrSent(hits[i]).After(receivedOrSent(hits[j]))
	})

	const cap = 40
	truncated := false
	if len(hits) > cap {
		hits = hits[:cap]
		truncated = true
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%d match(es), newest first:\n\n", len(hits))
	for _, m := range hits {
		ref := s.refs.put(m)
		when := receivedOrSent(m).In(s.cfg.Location()).Format("Mon 2 Jan")
		fmt.Fprintf(&b, "[%s] %s\n     %s · %s", ref, subjectOf(m), m.FromName(), when)
		if !m.IsRead {
			b.WriteString(" · unread")
		}
		b.WriteString("\n")
	}
	if truncated {
		fmt.Fprintf(&b, "\nShowing the newest %d. Narrow the search for more.", cap)
	}
	return b.String(), nil
}

func (s *mcpServer) toolReadMessage(ctx context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		Ref string `json:"ref"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("could not read the arguments: %w", err)
	}

	m, ok := s.refs.get(args.Ref)
	if !ok {
		return "", fmt.Errorf("no message called %q in this conversation - "+
			"run a search or whats_waiting first to get references", args.Ref)
	}

	full, err := s.client.MessageByID(ctx, m.ID)
	if err != nil {
		return "", fmt.Errorf("reading the message: %w", err)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Subject: %s\nFrom: %s <%s>\nReceived: %s\n",
		subjectOf(*full), full.FromName(), full.FromAddress(),
		full.ReceivedDateTime.In(s.cfg.Location()).Format("Mon 2 Jan 2006, 3:04 PM"))
	if len(full.Categories) > 0 {
		fmt.Fprintf(&b, "Labels: %s\n", strings.Join(full.Categories, ", "))
	}
	b.WriteString("\n")
	b.WriteString(truncateRunes(strings.TrimSpace(full.BodyPreview), 6000))
	return b.String(), nil
}

func describeCategories() string {
	var b strings.Builder
	b.WriteString("Categories available for sorting:\n\n")
	b.WriteString(labels.Describe())
	b.WriteString("\nUse the id (the first word) when calling apply_labels. " +
		"If a message fits none of these, or two fit equally well, use '")
	b.WriteString(labels.NeedsReviewID)
	b.WriteString("' rather than guessing - an honest 'unsure' is more useful than a " +
		"confident wrong label.")
	return b.String()
}

func (s *mcpServer) toolSuggestLabels(ctx context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		Count int `json:"count"`
	}
	_ = json.Unmarshal(raw, &args)

	count := args.Count
	if count <= 0 {
		count = 25
	}
	if count > maxSuggestBatch {
		count = maxSuggestBatch
	}

	inbox, err := s.client.InboxSince(ctx, s.now().Add(-searchLookback))
	if err != nil {
		return "", fmt.Errorf("reading the inbox: %w", err)
	}

	// Unlabelled means "carries none of OUR categories". A category she applied
	// by hand is her business and must not be treated as unsorted.
	var pending []graph.Message
	for _, m := range inbox {
		labelled := false
		for _, name := range m.Categories {
			if labels.IsOurs(name) {
				labelled = true
				break
			}
		}
		if !labelled {
			pending = append(pending, m)
		}
	}

	total := len(pending)
	if total == 0 {
		return "Every recent message already has a label. Nothing to sort.", nil
	}
	if len(pending) > count {
		pending = pending[:count]
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%d unlabelled message(s); showing %d.\n\n", total, len(pending))
	b.WriteString(labels.Describe())
	b.WriteString("\nMESSAGES\n\n")

	for _, m := range pending {
		ref := s.refs.put(m)
		fmt.Fprintf(&b, "[%s] %s\n     from %s <%s> · %s\n",
			ref, subjectOf(m), m.FromName(), m.FromAddress(),
			m.ReceivedDateTime.In(s.cfg.Location()).Format("Mon 2 Jan"))
		if preview := strings.TrimSpace(m.BodyPreview); preview != "" {
			fmt.Fprintf(&b, "     %s\n", truncateRunes(preview, 220))
		}
		b.WriteString("\n")
	}

	fmt.Fprintf(&b, "Propose a category for each, show her the list, and wait for her "+
		"agreement before calling apply_labels. %d message(s) remain after these.",
		total-len(pending))
	return b.String(), nil
}

func (s *mcpServer) toolApplyLabels(ctx context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		Labels []struct {
			Ref      string `json:"ref"`
			Category string `json:"category"`
		} `json:"labels"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("could not read the arguments: %w", err)
	}
	if len(args.Labels) == 0 {
		return "", fmt.Errorf("no labels were given")
	}
	if len(args.Labels) > maxSuggestBatch {
		return "", fmt.Errorf("too many at once (%d); apply at most %d per call",
			len(args.Labels), maxSuggestBatch)
	}

	// Resolve everything before writing anything, so a typo in the last entry
	// does not leave the mailbox half-labelled.
	type resolved struct {
		msg graph.Message
		cat labels.Category
	}
	var work []resolved
	var problems []string

	for _, l := range args.Labels {
		m, ok := s.refs.get(l.Ref)
		if !ok {
			problems = append(problems, fmt.Sprintf("%s: no such message reference", l.Ref))
			continue
		}
		cat := labels.ByID(strings.TrimSpace(l.Category))
		if cat == nil {
			if byName := labels.ByName(l.Category); byName != nil {
				cat = byName
			} else {
				problems = append(problems,
					fmt.Sprintf("%s: unknown category %q", l.Ref, l.Category))
				continue
			}
		}
		work = append(work, resolved{msg: m, cat: *cat})
	}

	if len(problems) > 0 {
		return "", fmt.Errorf("nothing was changed:\n  %s", strings.Join(problems, "\n  "))
	}

	// The categories must exist on the mailbox before they can be applied.
	wanted := make([]struct{ Name, Color string }, 0, len(labels.Taxonomy))
	for _, c := range labels.Taxonomy {
		wanted = append(wanted, struct{ Name, Color string }{c.Name, c.Color})
	}
	if _, err := s.client.EnsureCategories(ctx, wanted); err != nil {
		return "", fmt.Errorf("setting up the labels in Outlook: %w", err)
	}

	changes := make([]graph.LabelChange, 0, len(work))
	for _, w := range work {
		// Preserve any category she applied by hand; Graph replaces the whole
		// array rather than merging.
		var keep []string
		for _, name := range w.msg.Categories {
			if !labels.IsOurs(name) {
				keep = append(keep, name)
			}
		}
		changes = append(changes, graph.LabelChange{
			MessageID:  w.msg.ID,
			Categories: append(keep, w.cat.Name),
		})
	}

	result, err := s.client.ApplyLabels(ctx, changes)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Labelled %d message(s).\n", len(result.Succeeded))
	byCat := map[string]int{}
	for i, w := range work {
		if i < len(changes) && result.Failed[changes[i].MessageID] == "" {
			byCat[w.cat.Name]++
		}
	}
	names := make([]string, 0, len(byCat))
	for n := range byCat {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Fprintf(&b, "  %s: %d\n", n, byCat[n])
	}

	if len(result.Failed) > 0 {
		fmt.Fprintf(&b, "\n%d could not be labelled:\n", len(result.Failed))
		for id, reason := range result.Failed {
			fmt.Fprintf(&b, "  %s: %s\n", truncateRunes(id, 20), reason)
		}
	}

	b.WriteString("\nNothing was moved or deleted. She can change any label in Outlook " +
		"the normal way.")
	return b.String(), nil
}

// ---------------------------------------------------------------------------

func subjectOf(m graph.Message) string {
	s := strings.TrimSpace(m.Subject)
	if s == "" {
		return "(no subject)"
	}
	return truncateRunes(s, 110)
}

func receivedOrSent(m graph.Message) time.Time {
	if !m.ReceivedDateTime.IsZero() {
		return m.ReceivedDateTime
	}
	return m.SentDateTime
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
