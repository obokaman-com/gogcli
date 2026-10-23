package cmd

import (
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

func searchHeadersOf(t *testing.T, msg map[string]any) map[string]any {
	t.Helper()
	headers, ok := msg["headers"].(map[string]any)
	if !ok {
		t.Fatalf("headers missing or not an object: %#v", msg["headers"])
	}
	return headers
}

func TestGmailMessagesSearch_HeadersMetadataUsesSameGet(t *testing.T) {
	svc, stub := newSearchStub(t, []string{"m1", "m2", "m3"}, searchTestHTMLBody, searchTestPlainBody)
	raw, stdout := runSearchJSON(t, svc, "--headers", "list-id, Sender,LIST-ID,X-Missing,Message-ID")

	if got := stub.gets(); got != 3 {
		t.Fatalf("message gets = %d, want exactly one per result (3)", got)
	}
	for i, set := range stub.getHeaderSets {
		if stub.getFormats[i] != "metadata" {
			t.Fatalf("format = %q, want metadata", stub.getFormats[i])
		}
		want := []string{"From", "Subject", "Date", "List-Id", "Sender", "X-Missing", "Message-Id"}
		if !slices.Equal(set, want) {
			t.Fatalf("metadataHeaders = %v, want %v", set, want)
		}
	}

	got := searchHeadersOf(t, raw["messages"][0])
	want := map[string]any{
		"List-Id":   "Some Pub <somepub.substack.example>",
		"Sender":    "Group <group@example.com>",
		"X-Missing": "",
		// Canonical keys rewrite acronyms: Message-ID comes back as Message-Id.
		"Message-Id": "<abc@mail.example>",
	}
	if !maps.Equal(got, want) {
		t.Fatalf("headers = %#v, want %#v", got, want)
	}
	for _, key := range []string{`"to"`, `"body"`} {
		if strings.Contains(stdout, key) {
			t.Fatalf("--headers must not add %s: %s", key, stdout)
		}
	}
}

func TestGmailMessagesSearch_HeadersFullReadsExistingPayload(t *testing.T) {
	svc, stub := newSearchStub(t, []string{"m1"}, searchTestHTMLBody, searchTestPlainBody)
	raw, _ := runSearchJSON(t, svc, "--include-body", "--headers", "List-Id,Received,message-id")

	if got := stub.gets(); got != 1 || stub.getFormats[0] != "full" {
		t.Fatalf("gets=%d formats=%v, want one full get", got, stub.getFormats)
	}
	if len(stub.getHeaderSets[0]) != 0 {
		t.Fatalf("full get must not send metadataHeaders: %v", stub.getHeaderSets[0])
	}
	headers := searchHeadersOf(t, raw["messages"][0])
	if headers["List-Id"] != "Some Pub <somepub.substack.example>" {
		t.Fatalf("List-Id = %q", headers["List-Id"])
	}
	if headers["Message-Id"] != "<abc@mail.example>" {
		t.Fatalf("Message-Id = %q", headers["Message-Id"])
	}
	// Repeated headers keep the first occurrence, as everywhere else in gog.
	if headers["Received"] != "first hop" {
		t.Fatalf("Received = %q, want the first occurrence", headers["Received"])
	}
}

func TestGmailMessagesSearch_HeadersWithRecipientsShareOneRequest(t *testing.T) {
	svc, stub := newSearchStub(t, []string{"m1"}, searchTestHTMLBody, searchTestPlainBody)
	raw, _ := runSearchJSON(t, svc, "--include-recipients", "--headers", "to,List-Id")

	want := []string{"From", "Subject", "Date", "To", "Cc", "Bcc", "List-Id"}
	if got := stub.getHeaderSets[0]; !slices.Equal(got, want) {
		t.Fatalf("metadataHeaders = %v, want %v", got, want)
	}
	msg := raw["messages"][0]
	if headers := searchHeadersOf(t, msg); headers["To"] != msg["to"] || msg["to"] == "" {
		t.Fatalf("headers.To = %q, to = %q", headers["To"], msg["to"])
	}
}

func TestGmailMessagesSearch_HeadersAreSanitized(t *testing.T) {
	svc, stub := newSearchStub(t, []string{"m1"}, searchTestHTMLBody, searchTestPlainBody)
	stub.mu.Lock()
	stub.headerOverrides = map[string]string{"List-Id": "Pub https://list.example/x <pub.list.example>"}
	stub.mu.Unlock()

	raw, stdout := runSearchJSON(t, svc, "--sanitize-content", "--headers", "List-Id")
	if got := searchHeadersOf(t, raw["messages"][0])["List-Id"]; got != "Pub [url removed] <pub.list.example>" {
		t.Fatalf("sanitized List-Id = %q", got)
	}
	if strings.Contains(stdout, "list.example/x") {
		t.Fatalf("url leaked: %s", stdout)
	}
}

func TestGmailMessagesSearch_WrapsSelectedHeaders(t *testing.T) {
	const marker = "<<<EXTERNAL_UNTRUSTED_CONTENT"
	cases := map[string][]string{
		"envelope":     nil,
		"results-only": {"--results-only", "--select", "id,headers"},
		"dotted":       {"--select", "messages.0.id,messages.0.headers.List-Id,messages.0.headers.Id"},
	}
	for name, extra := range cases {
		t.Run(name, func(t *testing.T) {
			svc, _ := newSearchStub(t, []string{"m1"}, searchTestHTMLBody, searchTestPlainBody)
			// "id" canonicalizes to "Id", so it cannot mark the message ID as content.
			args := append([]string{
				"--json", "--account", "a@b.com", "gmail", "messages", "search", "in:sent",
				"--headers", "List-Id,id", "--wrap-untrusted",
			}, extra...)
			result := executeWithGmailTestService(t, args, svc)
			if result.err != nil {
				t.Fatal(result.err)
			}

			var id, listID any
			switch name {
			case "envelope":
				var payload struct {
					Messages []map[string]any `json:"messages"`
				}
				if err := json.Unmarshal([]byte(result.stdout), &payload); err != nil {
					t.Fatal(err)
				}
				id, listID = payload.Messages[0]["id"], searchHeadersOf(t, payload.Messages[0])["List-Id"]
			case "results-only":
				var messages []map[string]any
				if err := json.Unmarshal([]byte(result.stdout), &messages); err != nil {
					t.Fatal(err)
				}
				id, listID = messages[0]["id"], searchHeadersOf(t, messages[0])["List-Id"]
			case "dotted":
				var got map[string]any
				if err := json.Unmarshal([]byte(result.stdout), &got); err != nil {
					t.Fatal(err)
				}
				id, listID = got["messages.0.id"], got["messages.0.headers.List-Id"]
			}

			if id != "m1" {
				t.Fatalf("message ID changed: %#v", id)
			}
			if value, _ := listID.(string); !strings.Contains(value, marker) {
				t.Fatalf("List-Id was not wrapped: %q", value)
			}
		})
	}
}

func TestMCPGmailSearchBuildsHeadersArg(t *testing.T) {
	tool := findMCPTool(t, "gmail_search")
	args, err := tool.BuildArgs(mcp.CallToolRequest{Params: mcp.CallToolParams{
		Arguments: map[string]any{"query": "in:sent", "headers": []any{" List-Id", "Sender "}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(args, "--headers=List-Id,Sender") {
		t.Fatalf("args %v missing --headers=List-Id,Sender", args)
	}

	args, err = tool.BuildArgs(mcp.CallToolRequest{Params: mcp.CallToolParams{
		Arguments: map[string]any{"query": "in:sent"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(args, func(arg string) bool { return strings.HasPrefix(arg, "--headers") }) {
		t.Fatalf("default must not request headers: %v", args)
	}
}

func TestGmailMessagesSearch_HeadersValidationBeforeAuthentication(t *testing.T) {
	for _, raw := range []string{"Bad Header", "Bad:Header", "X-\nInjected", "X-Ünicode", strings.Repeat("X", 257), strings.Repeat("X-Test,", 51)} {
		t.Run(raw[:min(len(raw), 30)], func(t *testing.T) {
			result := executeWithTestRuntime(t, []string{"gmail", "messages", "search", "in:inbox", "--headers", raw}, nil)
			if result.err == nil || !strings.Contains(result.err.Error(), "--headers") {
				t.Fatalf("expected header usage error before auth, got %v", result.err)
			}
		})
	}
	names := make([]string, 50)
	for i := range names {
		names[i] = "X-Test"
	}
	got, err := gmailSearchHeaderNames(strings.Join(names, ","))
	if err != nil || !slices.Equal(got, []string{"X-Test"}) {
		t.Fatalf("names=%v err=%v", got, err)
	}
}
