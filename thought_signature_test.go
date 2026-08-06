package main

import (
	"strings"
	"testing"
	"time"
)

func TestThoughtSignatureStore(t *testing.T) {
	now := time.Date(2026, time.August, 6, 12, 0, 0, 0, time.UTC)
	store := newThoughtSignatureStore(5*time.Minute, func() time.Time { return now })

	store.put("call-1", "signature-a")
	if got, ok := store.get("call-1"); !ok || got != "signature-a" {
		t.Fatalf("get() = %q, %v; want signature-a, true", got, ok)
	}

	store.put("call-1", "signature-b")
	if got, ok := store.get("call-1"); !ok || got != "signature-b" {
		t.Fatalf("replacement get() = %q, %v; want signature-b, true", got, ok)
	}

	now = now.Add(5*time.Minute + time.Nanosecond)
	if _, ok := store.get("call-1"); ok {
		t.Fatal("expired signature remained available")
	}
}

const unsignedToolResultRequest = `{
  "model":"google/gemini-2.5-flash",
  "messages":[
    {"role":"assistant","tool_calls":[{"id":"call-1","type":"function","function":{"name":"read","arguments":"{}"}}]},
    {"role":"tool","tool_call_id":"call-1","content":"# README"}
  ]
}`

func TestRestoreThoughtSignatures(t *testing.T) {
	store := newThoughtSignatureStore(time.Hour, time.Now)
	store.put("call-1", "vertex-signature")

	body, changed := restoreThoughtSignatures([]byte(unsignedToolResultRequest), store)
	if !changed {
		t.Fatal("restoreThoughtSignatures() did not report recovery")
	}
	if !strings.Contains(string(body), `"thought_signature":"vertex-signature"`) {
		t.Fatalf("recovered request omitted signature: %s", body)
	}
}

func TestCaptureThoughtSignaturesFromSSEEvent(t *testing.T) {
	store := newThoughtSignatureStore(time.Hour, time.Now)
	event := []byte("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"id\":\"call-1\",\"extra_content\":{\"google\":{\"thought_signature\":\"signature-one\"}}},{\"id\":\"call-2\",\"extra_content\":{\"google\":{\"thought_signature\":\"signature-two\"}}}]}}]}\n\n")

	captureThoughtSignaturesFromSSEEvent(event, store)

	for id, want := range map[string]string{"call-1": "signature-one", "call-2": "signature-two"} {
		if got, ok := store.get(id); !ok || got != want {
			t.Errorf("get(%q) = %q, %v; want %q, true", id, got, ok, want)
		}
	}
}

func TestRestoreThoughtSignaturesLeavesExistingAndUnknownSignaturesUntouched(t *testing.T) {
	store := newThoughtSignatureStore(time.Hour, time.Now)
	store.put("known-call", "stored-signature")
	testCases := []struct {
		name string
		body string
	}{
		{
			name: "client supplied signature",
			body: `{"messages":[{"role":"assistant","tool_calls":[{"id":"known-call","extra_content":{"google":{"thought_signature":"client-signature"}}}]}]}`,
		},
		{
			name: "unknown tool call",
			body: `{"messages":[{"role":"assistant","tool_calls":[{"id":"unknown-call"}]}]}`,
		},
		{
			name: "malformed JSON",
			body: `{"messages":[`,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			body, changed := restoreThoughtSignatures([]byte(testCase.body), store)
			if changed {
				t.Fatalf("restoreThoughtSignatures() changed %s", testCase.name)
			}
			if got := string(body); got != testCase.body {
				t.Fatalf("body = %q, want %q", got, testCase.body)
			}
		})
	}
}

func TestRestoreThoughtSignaturesRecoversNullExtraContent(t *testing.T) {
	store := newThoughtSignatureStore(time.Hour, time.Now)
	store.put("call-1", "vertex-signature")

	body, changed := restoreThoughtSignatures([]byte(`{"messages":[{"role":"assistant","tool_calls":[{"id":"call-1","extra_content":{"google":null}}]}]}`), store)
	if !changed {
		t.Fatal("restoreThoughtSignatures() did not recover a null Google extension")
	}
	if !strings.Contains(string(body), `"thought_signature":"vertex-signature"`) {
		t.Fatalf("recovered request omitted signature: %s", body)
	}
}

func TestCaptureThoughtSignaturesFromJSONResponseClosesUpstreamBody(t *testing.T) {
	upstream := &closeTrackingReadCloser{Reader: strings.NewReader(strings.Repeat("x", maxThoughtSignatureJSONResponseBytes+1))}
	captured := captureThoughtSignaturesFromJSONResponse(upstream, newThoughtSignatureStore(time.Hour, time.Now))
	if err := captured.Close(); err != nil {
		t.Fatal(err)
	}
	if !upstream.closed {
		t.Fatal("closing captured body did not close the upstream body")
	}
}

type closeTrackingReadCloser struct {
	*strings.Reader
	closed bool
}

func (body *closeTrackingReadCloser) Close() error {
	body.closed = true
	return nil
}
