package main

import (
	"encoding/json"
	"strings"
	"testing"

	"swcf/digest/internal/graph"
	"swcf/digest/internal/labels"
)

// Protocol-shape tests.
//
// These do not need a mailbox. They cover the parts that break silently: a
// malformed tool schema means Claude never calls the tool and says nothing, and
// a reply to a notification corrupts the stream with no error anywhere.

func TestToolSchemasAreWellFormed(t *testing.T) {
	tools := toolDefinitions()
	if len(tools) != 8 {
		t.Fatalf("expected 8 tools, got %d", len(tools))
	}

	seen := map[string]bool{}
	for _, tool := range tools {
		name, _ := tool["name"].(string)
		if name == "" {
			t.Error("a tool has no name")
			continue
		}
		if seen[name] {
			t.Errorf("duplicate tool name %q", name)
		}
		seen[name] = true

		desc, _ := tool["description"].(string)
		if len(desc) < 40 {
			// The description is the only thing telling the model when to reach
			// for this tool. A terse one means it gets used at the wrong moment
			// or never.
			t.Errorf("tool %q has a description too short to guide use", name)
		}

		schema, ok := tool["inputSchema"].(map[string]any)
		if !ok {
			t.Errorf("tool %q has no inputSchema", name)
			continue
		}
		if schema["type"] != "object" {
			t.Errorf("tool %q schema type = %v, want object", name, schema["type"])
		}
		if _, ok := schema["properties"].(map[string]any); !ok {
			t.Errorf("tool %q schema has no properties map", name)
		}

		// Everything must survive a JSON round trip, since that is how it
		// reaches Claude.
		if _, err := json.Marshal(tool); err != nil {
			t.Errorf("tool %q does not marshal: %v", name, err)
		}
	}

	for _, required := range []string{
		"whats_waiting", "weekly_summary", "whats_next_week", "find_mail",
		"read_message", "list_categories", "suggest_labels", "apply_labels",
	} {
		if !seen[required] {
			t.Errorf("missing tool %q", required)
		}
	}
}

// The write tool must state, in the text the model actually reads, that it
// needs prior agreement. This is the only guardrail against "sort my emails"
// silently relabelling a hundred messages.
func TestWriteToolDescribesItsOwnGuardrail(t *testing.T) {
	var apply, suggest map[string]any
	for _, tool := range toolDefinitions() {
		switch tool["name"] {
		case "apply_labels":
			apply = tool
		case "suggest_labels":
			suggest = tool
		}
	}

	applyDesc := strings.ToLower(apply["description"].(string))
	for _, phrase := range []string{"only call this after", "agreement"} {
		if !strings.Contains(applyDesc, phrase) {
			t.Errorf("apply_labels description should contain %q; got: %s", phrase, applyDesc)
		}
	}

	suggestDesc := suggest["description"].(string)
	if !strings.Contains(suggestDesc, "CHANGES NOTHING") {
		t.Error("suggest_labels must say plainly that it is read-only")
	}
}

func TestInitializeEchoesASupportedProtocolVersion(t *testing.T) {
	s := &mcpServer{}

	cases := []struct{ requested, want string }{
		{"2024-11-05", "2024-11-05"},
		{"2025-06-18", "2025-06-18"},
		{"1999-01-01", defaultProtocolVersion}, // unknown: fall back
		{"", defaultProtocolVersion},
	}

	for _, tc := range cases {
		params, _ := json.Marshal(map[string]string{"protocolVersion": tc.requested})
		req := &rpcRequest{Params: params}

		got := s.initializeResult(req, "test")
		if got["protocolVersion"] != tc.want {
			t.Errorf("requested %q -> %v, want %q", tc.requested, got["protocolVersion"], tc.want)
		}
		if _, ok := got["capabilities"].(map[string]any)["tools"]; !ok {
			t.Error("initialize must advertise the tools capability")
		}
	}
}

// A notification carries no id and must draw no response. Replying to one puts
// an unexpected message on the stream, which some clients treat as a fatal
// protocol error.
func TestNotificationsAreNotAnswered(t *testing.T) {
	s := &mcpServer{}

	for _, method := range []string{"notifications/initialized", "notifications/cancelled"} {
		req := &rpcRequest{Method: method} // no ID
		// reply() and replyError() both no-op without an id; if they did not,
		// this would panic on the nil writer.
		s.handle(req, "test")
	}
}

func TestRefsAreShortAndResolveBack(t *testing.T) {
	refs := newRefStore()

	// Real Graph IDs are ~150 characters. Round-tripping fifty of those through
	// a model wastes thousands of tokens for no benefit.
	longID := strings.Repeat("AAMkAGI2", 20)
	msg := graph.Message{ID: longID, Subject: "Letter of inquiry"}

	ref := refs.put(msg)
	if len(ref) > 6 {
		t.Errorf("ref %q is too long to be worth the indirection", ref)
	}

	got, ok := refs.get(ref)
	if !ok {
		t.Fatalf("ref %q did not resolve", ref)
	}
	if got.ID != longID {
		t.Error("ref resolved to the wrong message")
	}
	if got.Subject != "Letter of inquiry" {
		t.Error("the whole message should be retained, not just the id")
	}

	if _, ok := refs.get("m999"); ok {
		t.Error("an unknown ref must not resolve")
	}
	// Whitespace from a model's output should not defeat a lookup.
	if _, ok := refs.get("  " + ref + "  "); !ok {
		t.Error("refs should tolerate surrounding whitespace")
	}
}

func TestRefsAreUnique(t *testing.T) {
	refs := newRefStore()
	seen := map[string]bool{}
	for i := range 100 {
		ref := refs.put(graph.Message{ID: string(rune('a' + i%26))})
		if seen[ref] {
			t.Fatalf("duplicate ref %q", ref)
		}
		seen[ref] = true
	}
}

func TestCategoryDescriptionSteersTowardHonestUncertainty(t *testing.T) {
	text := describeCategories()

	if !strings.Contains(text, labels.NeedsReviewID) {
		t.Error("the needs-review option must be offered")
	}
	if !strings.Contains(text, "rather than guessing") {
		t.Error("the model should be told to admit uncertainty rather than guess")
	}
	for _, id := range []string{"donors", "grants", "scholarships", "board", "finance",
		"events", "partners", "press", "sector", "vendors"} {
		if !strings.Contains(text, id) {
			t.Errorf("category %q missing from the description", id)
		}
	}
}

// The category descriptions are the entire classifier here - there is no
// training data - so each must actually distinguish itself from its neighbours.
func TestCategoryDescriptionsAreSubstantive(t *testing.T) {
	for _, c := range labels.Selectable() {
		if len(c.Description) < 120 {
			t.Errorf("category %q description is too thin to separate it from its neighbours", c.ID)
		}
		if c.Name == "" || c.Color == "" {
			t.Errorf("category %q is missing a name or colour", c.ID)
		}
	}

	// Colours must be distinct, or two categories look identical in Outlook.
	seen := map[string]string{}
	for _, c := range labels.Taxonomy {
		if prev, dup := seen[c.Color]; dup {
			t.Errorf("categories %q and %q share colour %s", prev, c.Name, c.Color)
		}
		seen[c.Color] = c.Name
	}
}

func TestIsOursOnlyClaimsOurLabels(t *testing.T) {
	if !labels.IsOurs("Grants") {
		t.Error("Grants is one of ours")
	}
	if !labels.IsOurs("  grants  ") {
		t.Error("matching should tolerate case and whitespace")
	}
	if labels.IsOurs("Personal") {
		// A label she applied by hand must never be treated as ours, or the
		// sorter would quietly overwrite her own filing.
		t.Error("a category we do not manage must not be claimed")
	}
}

func TestUnknownToolIsRejected(t *testing.T) {
	s := &mcpServer{}
	params, _ := json.Marshal(map[string]any{"name": "delete_everything"})

	got := s.callTool(params)
	if !got.IsError {
		t.Fatal("an unknown tool must return an error")
	}
	if !strings.Contains(got.Content[0].Text, "delete_everything") {
		t.Error("the error should name the tool that was attempted")
	}
}

func TestMalformedToolCallIsRejected(t *testing.T) {
	s := &mcpServer{}
	if got := s.callTool([]byte("{not json")); !got.IsError {
		t.Error("a malformed tool call must return an error, not panic")
	}
}
