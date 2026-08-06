# Vertex Native Model Gateway Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Dynamically expose Google publisher models from Vertex AI and provide authenticated native inference routes for every supported model action while retaining OpenAI compatibility.

**Architecture:** Add a token-protected handler layer in front of both existing `/v1` routes and a new native `/vertex/v1` surface. A paginated, TTL-cached Model Garden client uses the existing ADC token to query `v1beta1/publishers/google/models`; the OpenAI model list and native catalog share that source. The native route admits only validated Google model IDs and a fixed inference-action allowlist, then streams the upstream Vertex response without exposing arbitrary Vertex resource management APIs.

**Tech Stack:** Go standard library, `golang.org/x/oauth2/google`, Docker Compose, Vertex AI REST API.

---

## File structure

- Create: `auth.go` — constant-time Bearer-token authentication middleware.
- Create: `catalog.go` — paginated Google publisher-model client, cache, OpenAI model conversion, and `/v1/models`/`/vertex/v1/models` handlers.
- Create: `native_proxy.go` — validates and forwards native model inference requests.
- Modify: `main.go` — removes the static model list and wires authenticated routes with project/location configuration.
- Modify: `main_test.go` — removes tests for static environment-backed model lists.
- Create: `auth_test.go` — authentication contracts.
- Create: `catalog_test.go` — pagination, caching, model conversion, and catalog endpoint contracts.
- Create: `native_proxy_test.go` — method/path allowlist and upstream request/response contracts.
- Modify: `docker-compose.yml`, `.env.example`, and `README.md` — require and document `VERTEXAI_PROXY_API_KEY` and native model calls.

### Task 1: Require a client API key before using ADC-backed routes

**Files:**
- Create: `auth.go`
- Create: `auth_test.go`
- Modify: `main.go:324-371`

- [ ] **Step 1: Write failing authentication tests**

Create `auth_test.go`:

```go
package main

import (
    "net/http"
    "net/http/httptest"
    "testing"
)

func TestAPIKeyAuth(t *testing.T) {
    next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusNoContent)
    })

    tests := []struct {
        name, authorization string
        want               int
    }{
        {name: "missing key", want: http.StatusUnauthorized},
        {name: "wrong scheme", authorization: "Basic secret", want: http.StatusUnauthorized},
        {name: "wrong key", authorization: "Bearer wrong", want: http.StatusUnauthorized},
        {name: "correct key", authorization: "Bearer secret", want: http.StatusNoContent},
    }
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
            req.Header.Set("Authorization", tt.authorization)
            rec := httptest.NewRecorder()
            newAPIKeyAuth("secret")(next).ServeHTTP(rec, req)
            if rec.Code != tt.want {
                t.Fatalf("status = %d, want %d", rec.Code, tt.want)
            }
        })
    }
}
```

- [ ] **Step 2: Run the focused test and verify RED**

Run:

```bash
docker run --rm -v "$PWD:/app" -w /app golang:alpine go test ./... -run '^TestAPIKeyAuth$'
```

Expected: compilation failure because `newAPIKeyAuth` does not exist.

- [ ] **Step 3: Implement constant-time Bearer authentication**

Create `auth.go`:

```go
package main

import (
    "crypto/subtle"
    "net/http"
    "strings"
)

func newAPIKeyAuth(apiKey string) func(http.Handler) http.Handler {
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            const prefix = "Bearer "
            authorization := r.Header.Get("Authorization")
            candidate, ok := strings.CutPrefix(authorization, prefix)
            if !ok || apiKey == "" || len(candidate) != len(apiKey) || subtle.ConstantTimeCompare([]byte(candidate), []byte(apiKey)) != 1 {
                w.Header().Set("WWW-Authenticate", "Bearer")
                http.Error(w, "unauthorized", http.StatusUnauthorized)
                return
            }
            next.ServeHTTP(w, r)
        })
    }
}
```

In `main.go`, read `VERTEXAI_PROXY_API_KEY` after project/location validation; terminate startup when it is empty. Wrap every registered `/v1` and `/vertex/v1` handler with `newAPIKeyAuth(proxyAPIKey)`. Keep the upstream token injection in `makeProxy`; the middleware executes before that header is replaced.

- [ ] **Step 4: Run focused tests and full suite**

Run:

```bash
docker run --rm -v "$PWD:/app" -w /app golang:alpine go test ./... -run '^TestAPIKeyAuth$'
docker run --rm -v "$PWD:/app" -w /app golang:alpine go test ./...
```

Expected: both commands exit 0.

- [ ] **Step 5: Commit authentication support**

```bash
git add auth.go auth_test.go main.go
git commit -m "feat: authenticate proxy clients"
```

### Task 2: Replace static models with a paginated Vertex catalog

**Files:**
- Create: `catalog.go`
- Create: `catalog_test.go`
- Modify: `main.go:269-322`
- Modify: `main_test.go:164-322`

- [ ] **Step 1: Write failing catalog tests**

Create `catalog_test.go` with an `httptest.Server` whose first request requires `pageToken=""` and returns `{"publisherModels":[{"name":"publishers/google/models/gemini-first"}],"nextPageToken":"page-2"}`, and whose second request requires `pageToken="page-2"` and returns `{"publisherModels":[{"name":"publishers/google/models/gemini-second"}]}`. Test that `catalog.models(context.Background())` returns, in order, `google/gemini-first` and `google/gemini-second`; invoke it twice and assert the server handled exactly two requests total, proving the second invocation used the cache. Add a handler test asserting `/v1/models` serializes the same two IDs with `object: "list"`.

- [ ] **Step 2: Run focused catalog tests and verify RED**

Run:

```bash
docker run --rm -v "$PWD:/app" -w /app golang:alpine go test ./... -run '^(TestModelCatalog|TestOpenAIModelsHandler)$'
```

Expected: compilation failure because `modelCatalog` and `newOpenAIModelsHandler` do not exist.

- [ ] **Step 3: Implement catalog fetching, pagination, and TTL cache**

Create `catalog.go` with these types and contracts:

```go
type publisherModel struct { Name string `json:"name"` }
type publisherModelsPage struct {
    PublisherModels []publisherModel `json:"publisherModels"`
    NextPageToken   string           `json:"nextPageToken"`
}
type modelCatalog struct {
    baseURL string
    client  *http.Client
    token   func(context.Context) (string, error)
    now     func() time.Time
    ttl     time.Duration
    mu      sync.Mutex
    models  []Model
    expires time.Time
}
```

`models(ctx)` must lock around refreshes; return a copied slice while the cache is valid; otherwise repeatedly request `GET {baseURL}/v1beta1/publishers/google/models?pageSize=100&listAllVersions=true&pageToken=...`, attach `Authorization: Bearer <ADC token>`, reject non-2xx responses, and decode every page. Convert only names matching `publishers/google/models/<non-empty-id>` to `Model{ID: "google/" + id, Object: "model", Created: now.Unix(), OwnedBy: "google"}`. Sort by `ID` before caching and use a five-minute TTL.

Implement `newOpenAIModelsHandler(catalog *modelCatalog)` and `newNativeModelsHandler(catalog *modelCatalog)`. The former emits `ModelList{Object: "list", Data: models}`; the latter emits the cached `[]Model` as JSON. Both return `502 Bad Gateway` if refresh fails and no valid cache exists.

Replace `handleModels` and its `VERTEXAI_AVAILABLE_MODELS` behavior; remove its static-list tests from `main_test.go`.

- [ ] **Step 4: Run catalog and full tests**

Run:

```bash
docker run --rm -v "$PWD:/app" -w /app golang:alpine go test ./... -run '^(TestModelCatalog|TestOpenAIModelsHandler)$'
docker run --rm -v "$PWD:/app" -w /app golang:alpine go test ./...
```

Expected: both commands exit 0; the focused test observes two upstream requests, not four.

- [ ] **Step 5: Commit dynamic model discovery**

```bash
git add catalog.go catalog_test.go main.go main_test.go
git commit -m "feat: discover Vertex publisher models"
```

### Task 3: Add allowlisted native Vertex inference forwarding

**Files:**
- Create: `native_proxy.go`
- Create: `native_proxy_test.go`
- Modify: `main.go:337-360`

- [ ] **Step 1: Write failing native route tests**

Create `native_proxy_test.go` using an `httptest.Server`. Test that a request to:

```text
POST /vertex/v1/models/google/gemini-test:generateContent
```

is forwarded as:

```text
POST /v1/projects/project/locations/europe-central2/publishers/google/models/gemini-test:generateContent
```

with `Authorization: Bearer adc-token`, unchanged JSON request bytes, status `201`, content type, and response body. Add table-driven rejection cases for `GET`, publisher `meta`, model `../../endpoints`, action `delete`, and a missing action; every rejection must return `404` or `405` without the test upstream receiving a request.

- [ ] **Step 2: Run native route tests and verify RED**

Run:

```bash
docker run --rm -v "$PWD:/app" -w /app golang:alpine go test ./... -run '^TestNativeModelProxy$'
```

Expected: compilation failure because `newNativeModelProxy` does not exist.

- [ ] **Step 3: Implement strict native inference proxy**

Create `native_proxy.go` with `newNativeModelProxy(baseURL, project, location string, token func(context.Context) (string, error), client *http.Client) http.Handler`.

The handler accepts `POST` only. Parse exactly `/vertex/v1/models/google/{model}:{action}`; validate `model` against `^[A-Za-z0-9][A-Za-z0-9._-]*$`; allow exactly `generateContent`, `streamGenerateContent`, `embedContent`, `predict`, `rawPredict`, `streamRawPredict`, `serverStreamingPredict`, `predictLongRunning`, and `fetchPredictOperation`. Obtain ADC with `token(r.Context())`, make an upstream request to:

```go
fmt.Sprintf("%s/v1/projects/%s/locations/%s/publishers/google/models/%s:%s", baseURL, project, location, model, action)
```

Copy request content type and body without buffering. Copy upstream headers, status, and response body with `io.Copy`, preserving streaming. Return `401` for a token failure, `502` for transport failure, `405` for a non-POST request, and `404` for an invalid publisher, model path, or action.

In `main.go`, construct the native handler with the same regional/global API base selected for the existing proxy and register it at `/vertex/v1/models/`; apply `newAPIKeyAuth` to it.

- [ ] **Step 4: Run native and full tests**

Run:

```bash
docker run --rm -v "$PWD:/app" -w /app golang:alpine go test ./... -run '^TestNativeModelProxy$'
docker run --rm -v "$PWD:/app" -w /app golang:alpine go test ./...
```

Expected: both commands exit 0.

- [ ] **Step 5: Commit native inference forwarding**

```bash
git add native_proxy.go native_proxy_test.go main.go
git commit -m "feat: proxy native Vertex model inference"
```

### Task 4: Require the key in Compose and document both API surfaces

**Files:**
- Modify: `docker-compose.yml:4-10,27-31`
- Modify: `.env.example:1-10`
- Modify: `README.md:71-111`

- [ ] **Step 1: Write a failing Compose rendering check**

Run:

```bash
VERTEXAI_PROXY_API_KEY= docker compose config
```

Expected: non-zero exit because the required proxy key is absent.

- [ ] **Step 2: Wire the key through Docker Compose**

Add `VERTEXAI_PROXY_API_KEY=${VERTEXAI_PROXY_API_KEY:?Set VERTEXAI_PROXY_API_KEY in .env}` to `proxy.environment` and replace the fixed `OPENAI_API_KEY` with `OPENAI_API_KEY: ${VERTEXAI_PROXY_API_KEY:?Set VERTEXAI_PROXY_API_KEY in .env}`. Delete the commented `VERTEXAI_AVAILABLE_MODELS` setting because catalog discovery replaces it.

In `.env.example`, add:

```env
VERTEXAI_PROXY_API_KEY=replace-with-a-long-random-secret
```

Remove `VERTEXAI_AVAILABLE_MODELS` documentation and explain that the `google/` model list comes from Vertex at runtime. In `README.md`, document:

```bash
curl -H "Authorization: Bearer $VERTEXAI_PROXY_API_KEY" \
  http://localhost:8080/vertex/v1/models
```

and a `generateContent` request to `/vertex/v1/models/google/gemini-2.5-flash:generateContent` using the native Vertex JSON body.

- [ ] **Step 3: Verify missing-key rejection and valid Compose configuration**

Run:

```bash
VERTEXAI_PROXY_API_KEY= docker compose config
VERTEXAI_PROXY_API_KEY=test-key docker compose config
```

Expected: the first command fails with `Set VERTEXAI_PROXY_API_KEY in .env`; the second exits 0 and renders `VERTEXAI_PROXY_API_KEY` for `proxy` and `OPENAI_API_KEY: test-key` for `webui`.

- [ ] **Step 4: Restart and smoke-test authenticated model discovery**

Set a non-empty `VERTEXAI_PROXY_API_KEY` in the ignored local `.env`, then run:

```bash
docker compose up -d --build --force-recreate proxy webui
curl --fail --silent --show-error \
  -H "Authorization: Bearer $VERTEXAI_PROXY_API_KEY" \
  http://127.0.0.1:8080/v1/models
curl --fail --silent --show-error \
  -H "Authorization: Bearer $VERTEXAI_PROXY_API_KEY" \
  http://127.0.0.1:8080/vertex/v1/models
```

Expected: both calls return JSON; the first has OpenAI `object: "list"`, and the second contains catalog models. Confirm a request without the header returns HTTP 401.

- [ ] **Step 5: Commit runtime configuration and documentation**

```bash
git add docker-compose.yml .env.example README.md
git commit -m "docs: configure Vertex native model gateway"
```
