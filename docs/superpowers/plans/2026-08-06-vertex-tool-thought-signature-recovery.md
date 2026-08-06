# Vertex Tool Thought-Signature Recovery Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Preserve Vertex Gemini tool-call thought signatures across OpenAI-compatible tool-result turns so clients such as Pi can execute tools without a missing-signature `400`.

**Architecture:** Add a concurrency-safe, TTL-bound store keyed by OpenAI `tool_call.id`. The Chat Completions proxy records signatures from JSON and SSE responses, then restores only a missing `extra_content.google.thought_signature` in later assistant tool calls before forwarding the request. Request and response bytes remain untouched where no capture or restoration occurs.

**Tech Stack:** Go standard library (`encoding/json`, `sync`, `time`, `net/http`, `httputil`); existing `httptest` tests.

---

## File structure

- Create `thought_signature.go`: TTL storage plus JSON helpers for extracting upstream signatures and restoring missing request signatures.
- Create `thought_signature_test.go`: deterministic unit coverage for store lifetime, JSON recovery, and upstream JSON/SSE extraction.
- Modify `main.go`: connect a thought-signature store to the OpenAI proxy; wrap successful upstream bodies without changing wire bytes.
- Modify `main_test.go`: integration-level handler test for upstream SSE capture followed by recovery on the tool-result request.
- Modify `Dockerfile:8`: include the new thought-signature source file in the build-stage source copy.
- Modify `README.md`: document tool-call continuation compatibility and the model-capability boundary.

### Task 1: Build a deterministic thought-signature store

**Files:**
- Create: `thought_signature.go`
- Test: `thought_signature_test.go`

- [ ] **Step 1: Write the failing lifetime and replacement tests**

```go
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
```

- [ ] **Step 2: Run the new test and verify it fails**

Run: `go test ./... -run '^TestThoughtSignatureStore$'`

Expected: FAIL because `newThoughtSignatureStore` is undefined.

- [ ] **Step 3: Implement the bounded store in `thought_signature.go`**

```go
const thoughtSignatureTTL = 30 * time.Minute

type thoughtSignatureEntry struct {
	signature string
	expiresAt time.Time
}

type thoughtSignatureStore struct {
	mutex   sync.Mutex
	ttl     time.Duration
	now     func() time.Time
	entries map[string]thoughtSignatureEntry
}

func newThoughtSignatureStore(ttl time.Duration, now func() time.Time) *thoughtSignatureStore {
	if ttl <= 0 {
		ttl = thoughtSignatureTTL
	}
	if now == nil {
		now = time.Now
	}
	return &thoughtSignatureStore{ttl: ttl, now: now, entries: make(map[string]thoughtSignatureEntry)}
}

func (store *thoughtSignatureStore) put(id, signature string) {
	if id == "" || signature == "" {
		return
	}
	store.mutex.Lock()
	defer store.mutex.Unlock()
	now := store.now()
	store.removeExpired(now)
	store.entries[id] = thoughtSignatureEntry{signature: signature, expiresAt: now.Add(store.ttl)}
}

func (store *thoughtSignatureStore) get(id string) (string, bool) {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	now := store.now()
	store.removeExpired(now)
	entry, ok := store.entries[id]
	return entry.signature, ok
}

func (store *thoughtSignatureStore) removeExpired(now time.Time) {
	for id, entry := range store.entries {
		if !entry.expiresAt.After(now) {
			delete(store.entries, id)
		}
	}
}
```

Use `sync.Mutex` and lazy cleanup. Do not add a cleanup goroutine or log signatures.

- [ ] **Step 4: Run the targeted test and verify it passes**

Run: `go test ./... -run '^TestThoughtSignatureStore$'`

Expected: PASS.

- [ ] **Step 5: Commit the store**

```bash
git add thought_signature.go thought_signature_test.go
git commit -m "feat: store Vertex tool thought signatures"
```

### Task 2: Recover and capture only opaque thought signatures

**Files:**
- Modify: `thought_signature.go`
- Modify: `thought_signature_test.go`

- [ ] **Step 1: Write failing JSON recovery and extraction tests**

Use this tool-result request fixture in `thought_signature_test.go`:

```go
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
```

Add table rows proving: a client-provided `extra_content.google.thought_signature` remains byte-for-byte the supplied value; an unknown call ID returns `changed == false` and the original request bytes; malformed JSON returns `changed == false` and the original request bytes. Add a response fixture containing two SSE `data:` JSON events with different tool call IDs and assert both signatures become available in the store.

- [ ] **Step 2: Run recovery tests and verify they fail**

Run: `go test ./... -run '^(TestRestoreThoughtSignatures|TestCaptureThoughtSignaturesFromSSE)$'`

Expected: FAIL because recovery and capture functions are undefined.

- [ ] **Step 3: Add JSON-only helpers that preserve all unrelated fields**

Implement these functions in `thought_signature.go`:

```go
func restoreThoughtSignatures(body []byte, store *thoughtSignatureStore) ([]byte, bool)
func captureThoughtSignatures(body []byte, store *thoughtSignatureStore)
func captureThoughtSignaturesFromSSEEvent(event []byte, store *thoughtSignatureStore)
```

Implementation requirements:

- Decode request objects as `map[string]json.RawMessage`; marshal only parent objects containing a recovered tool call. If no recovery is possible, return the original `body` slice.
- Iterate only `messages` where `role == "assistant"`, then only `tool_calls` with a non-empty `id`.
- Treat a present non-empty `extra_content.google.thought_signature` as authoritative; never overwrite it.
- Decode upstream `choices[].message.tool_calls[]` and `choices[].delta.tool_calls[]`; record only non-empty ID/signature pairs.
- For SSE, accept one complete event's `data:` lines, ignore `[DONE]`, join multiple data lines with a newline, and pass the JSON bytes to `captureThoughtSignatures`.
- Parsing errors are no-ops. Do not propagate parse errors to clients or upstream.

- [ ] **Step 4: Run recovery tests and verify they pass**

Run: `go test ./... -run '^(TestRestoreThoughtSignatures|TestCaptureThoughtSignaturesFromSSE)$'`

Expected: PASS.

- [ ] **Step 5: Commit recovery helpers**

```bash
git add thought_signature.go thought_signature_test.go
git commit -m "feat: recover missing Vertex tool signatures"
```

### Task 3: Wire capture and recovery into the OpenAI proxy

**Files:**
- Modify: `main.go:147-273`
- Modify: `main_test.go:105-210`
- Modify: `thought_signature.go`
- Modify: `Dockerfile:8`
- Test: `thought_signature_test.go`

- [ ] **Step 1: Write the failing end-to-end handler test**

In `main_test.go`, create an `httptest.Server` upstream. Its first `/chat/completions` request must write this exact SSE event and `[DONE]`:

```go
const upstreamToolCall = "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"id\":\"call-1\",\"type\":\"function\",\"function\":{\"name\":\"read\",\"arguments\":\"{}\"},\"extra_content\":{\"google\":{\"thought_signature\":\"vertex-signature\"}}}]}}]}\n\ndata: [DONE]\n\n"
```

Issue a second proxy request using `unsignedToolResultRequest`. At the upstream, decode that request and assert the assistant tool call contains `extra_content.google.thought_signature == "vertex-signature"`. Add a sibling assertion that the first client response equals `upstreamToolCall` exactly.

- [ ] **Step 2: Run the handler test and verify it fails**

Run: `go test ./... -run '^TestMakeProxy_RestoresVertexThoughtSignature$'`

Expected: FAIL because the second upstream request lacks the signature.

- [ ] **Step 3: Add byte-preserving response capture and request recovery**

In `thought_signature.go`, add an `io.ReadCloser` wrapper for SSE responses. Its `Read` method must return exactly the upstream bytes while retaining only incomplete SSE event bytes between reads; whenever `\n\n` terminates an event, call `captureThoughtSignaturesFromSSEEvent`.

For `application/json` non-streaming responses, in `ModifyResponse` read at most `1 MiB + 1 byte`. When the complete JSON body is at most `1 MiB`, call `captureThoughtSignatures`, then restore the original body with `io.NopCloser(bytes.NewReader(body))`. When it exceeds the limit, restore the consumed prefix and unread suffix with `io.MultiReader` and skip capture. Do not buffer a streaming response.

In `makeProxy`:

```go
signatures := newThoughtSignatureStore(thoughtSignatureTTL, time.Now)
```

Before `reverseProxy.ServeHTTP`, for `POST /v1/chat/completions`, call `restoreThoughtSignatures` after existing body reading. If it reports a change, replace `r.Body` and `r.ContentLength` with the recovered JSON body. Preserve the original `Content-Length` behavior when it reports no change.

At the end of existing `ModifyResponse`, select capture based on `Content-Type` using `mime.ParseMediaType`: wrap `text/event-stream` responses; otherwise inspect only `application/json`. Keep the existing gzip error logging before this capture step.

Update the Docker build-stage source copy so the new Go file is compiled:

```dockerfile
COPY auth.go catalog.go main.go native_proxy.go thought_signature.go ./
```

- [ ] **Step 4: Run focused proxy tests and verify they pass**

Run: `go test ./... -run '^(TestMakeProxy_RestoresVertexThoughtSignature|TestMakeProxy|TestThoughtSignature)'`

Expected: PASS.

- [ ] **Step 5: Commit proxy integration**

```bash
git add main.go main_test.go thought_signature.go thought_signature_test.go
git commit -m "fix: restore Vertex tool thought signatures"
```

### Task 4: Document and smoke-test the real Pi workflow

**Files:**
- Modify: `README.md:187-195`

- [ ] **Step 1: Add a concise troubleshooting entry**

Add this bullet under `## Troubleshooting`:

```markdown
* **CLI tools fail after the first tool call**: Some OpenAI-compatible clients omit Vertex's opaque Gemini thought signature when they return a tool result. The proxy restores the signature by tool-call ID during its short retention window. Use a tool-capable chat model such as `google/gemini-2.5-flash`; TTS and image-generation models do not become tool-capable through the proxy.
```

- [ ] **Step 2: Format and run the complete unit suite**

Run: `gofmt -w main.go main_test.go thought_signature.go thought_signature_test.go && go test ./...`

Expected: every package passes.

- [ ] **Step 3: Build and run the changed proxy image**

Run: `docker compose up --build -d proxy`

Expected: the `proxy` service is running with the rebuilt image.

- [ ] **Step 4: Smoke-test Pi against the live proxy**

Create a disposable Pi `models.json` provider that uses `api: "openai-completions"`, `baseUrl: "http://127.0.0.1:8081/v1"`, model `google/gemini-2.5-flash`, and `apiKey: "$VERTEXAI_PROXY_API_KEY"`. Run:

```bash
PI_CODING_AGENT_DIR=/tmp/vertex-proxy-pi VERTEXAI_PROXY_API_KEY="$VERTEXAI_PROXY_API_KEY" \
pi --provider vertex-proxy --model google/gemini-2.5-flash --no-context-files --no-session --mode json --print \
  "Use the read tool to inspect README.md, then reply with its first heading."
```

Expected: Pi emits a tool call, tool result, and final answer; no `Function call is missing a thought_signature` error occurs.

- [ ] **Step 5: Commit documentation**

```bash
git add README.md
git commit -m "docs: explain Vertex tool signature recovery"
```
