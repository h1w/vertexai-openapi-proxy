package main

import (
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// This function will be run before any other tests in this package
func TestMain(m *testing.M) {
	// Setup: Initialize the logger
	initSlogLogger()

	// Run all tests
	exitCode := m.Run()

	// Teardown (if any needed)

	// Exit with the tests' exit code
	os.Exit(exitCode)
}

// MockTokenSource is a mock for google.Credentials.TokenSource
type MockTokenSource struct {
	AccessTokenString string
	ExpiryTime        time.Time
	Error             error
}

func (m *MockTokenSource) Token() (*oauth2.Token, error) {
	if m.Error != nil {
		return nil, m.Error
	}
	return &oauth2.Token{
		AccessToken: m.AccessTokenString,
		Expiry:      m.ExpiryTime,
	}, nil
}

func TestGetToken_Cached(t *testing.T) {
	tokenMutex.Lock()
	token = "cached_token"
	expiry = time.Now().Add(time.Hour)
	tokenMutex.Unlock()

	ctx := context.Background()
	gotToken, err := getToken(ctx)
	if err != nil {
		t.Fatalf("getToken() error = %v, wantErr %v", err, false)
	}
	if gotToken != "cached_token" {
		t.Errorf("getToken() gotToken = %v, want %v", gotToken, "cached_token")
	}
}

func TestGetToken_NewFetch(t *testing.T) {
	// Reset global token state for this test
	tokenMutex.Lock()
	token = ""
	expiry = time.Time{}
	tokenMutex.Unlock()

	// Store original FindDefaultCredentials and defer its restoration
	originalFindDefaultCredentials := googleFindDefaultCredentials
	defer func() { googleFindDefaultCredentials = originalFindDefaultCredentials }()

	// Mock google.FindDefaultCredentials
	googleFindDefaultCredentials = func(ctx context.Context, scopes ...string) (*google.Credentials, error) {
		return &google.Credentials{
			TokenSource: &MockTokenSource{
				AccessTokenString: "new_token",
				ExpiryTime:        time.Now().Add(time.Hour),
			},
		}, nil
	}

	ctx := context.Background()
	gotToken, err := getToken(ctx)
	if err != nil {
		t.Fatalf("getToken() error = %v, wantErr %v", err, false)
	}
	if gotToken != "new_token" {
		t.Errorf("getToken() gotToken = %v, want %v", gotToken, "new_token")
	}

	tokenMutex.RLock()
	if token != "new_token" {
		t.Errorf("global token not set correctly, got %s, want %s", token, "new_token")
	}
	if expiry.IsZero() {
		t.Error("global expiry not set")
	}
	tokenMutex.RUnlock()
}

func TestMakeProxy(t *testing.T) {
	// Set up environment variables for the test
	// Reset global token state for this test to ensure it fetches a new token
	tokenMutex.Lock()
	token = ""
	expiry = time.Time{}
	tokenMutex.Unlock()

	os.Setenv("VERTEXAI_LOCATION", "us-central1")
	os.Setenv("VERTEXAI_PROJECT", "test-project")
	defer os.Unsetenv("VERTEXAI_LOCATION")
	defer os.Unsetenv("VERTEXAI_PROJECT")

	// Store original FindDefaultCredentials and defer its restoration
	originalFindDefaultCredentials := googleFindDefaultCredentials
	defer func() { googleFindDefaultCredentials = originalFindDefaultCredentials }()

	// Mock google.FindDefaultCredentials
	googleFindDefaultCredentials = func(ctx context.Context, scopes ...string) (*google.Credentials, error) {
		return &google.Credentials{
			TokenSource: &MockTokenSource{
				AccessTokenString: "test-token",
				ExpiryTime:        time.Now().Add(time.Hour),
			},
		}, nil
	}

	targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("Target server did not receive Authorization header, got: %s", r.Header.Get("Authorization"))
		}
		if !strings.HasSuffix(r.URL.Path, "/testpath") {
			t.Errorf("Target server did not receive correct path, got: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("target server response"))
	}))
	defer targetServer.Close()

	targetURL, _ := url.Parse(targetServer.URL)
	proxy := makeProxy(targetURL)

	req := httptest.NewRequest("GET", "http://localhost/v1/testpath", nil)
	req.Header.Set("Authorization", "Bearer proxy-secret")
	rr := httptest.NewRecorder()

	proxy.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("handler returned wrong status code: got %v want %v",
			rr.Code, http.StatusOK)
	}

	expectedBody := "target server response"
	if rr.Body.String() != expectedBody {
		t.Errorf("handler returned unexpected body: got %v want %v",
			rr.Body.String(), expectedBody)
	}
}

func TestMakeProxy_RestoresVertexThoughtSignature(t *testing.T) {
	tokenMutex.Lock()
	originalToken, originalExpiry := token, expiry
	token, expiry = "", time.Time{}
	tokenMutex.Unlock()
	t.Cleanup(func() {
		tokenMutex.Lock()
		token, expiry = originalToken, originalExpiry
		tokenMutex.Unlock()
	})

	originalFindDefaultCredentials := googleFindDefaultCredentials
	googleFindDefaultCredentials = func(context.Context, ...string) (*google.Credentials, error) {
		return &google.Credentials{TokenSource: &MockTokenSource{
			AccessTokenString: "test-token",
			ExpiryTime:        time.Now().Add(time.Hour),
		}}, nil
	}
	t.Cleanup(func() {
		googleFindDefaultCredentials = originalFindDefaultCredentials
	})

	const upstreamToolCall = "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"id\":\"call-1\",\"type\":\"function\",\"function\":{\"name\":\"read\",\"arguments\":\"{}\"},\"extra_content\":{\"google\":{\"thought_signature\":\"vertex-signature\"}}}]}}]}\n\ndata: [DONE]\n\n"
	upstreamRequests := 0
	targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamRequests++
		if r.URL.Path != "/chat/completions" {
			t.Errorf("upstream path = %q, want /chat/completions", r.URL.Path)
		}
		if upstreamRequests == 1 {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, upstreamToolCall)
			return
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), `"thought_signature":"vertex-signature"`) {
			t.Errorf("recovered upstream request omitted signature: %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[]}`)
	}))
	t.Cleanup(targetServer.Close)

	targetURL, err := url.Parse(targetServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxy := makeProxy(targetURL)

	firstRequest := httptest.NewRequest(http.MethodPost, "http://localhost/v1/chat/completions", strings.NewReader(`{"messages":[]}`))
	firstRequest.Header.Set("Content-Type", "application/json")
	firstResponse := httptest.NewRecorder()
	proxy.ServeHTTP(firstResponse, firstRequest)
	if got := firstResponse.Body.String(); got != upstreamToolCall {
		t.Errorf("stream response changed:\n got: %q\nwant: %q", got, upstreamToolCall)
	}

	secondRequest := httptest.NewRequest(http.MethodPost, "http://localhost/v1/chat/completions", strings.NewReader(unsignedToolResultRequest))
	secondRequest.Header.Set("Content-Type", "application/json")
	secondResponse := httptest.NewRecorder()
	proxy.ServeHTTP(secondResponse, secondRequest)
	if upstreamRequests != 2 {
		t.Errorf("upstream requests = %d, want 2", upstreamRequests)
	}
}

func TestMakeProxy_RestoresGzipVertexThoughtSignature(t *testing.T) {
	tokenMutex.Lock()
	originalToken, originalExpiry := token, expiry
	token, expiry = "", time.Time{}
	tokenMutex.Unlock()
	t.Cleanup(func() {
		tokenMutex.Lock()
		token, expiry = originalToken, originalExpiry
		tokenMutex.Unlock()
	})

	originalFindDefaultCredentials := googleFindDefaultCredentials
	googleFindDefaultCredentials = func(context.Context, ...string) (*google.Credentials, error) {
		return &google.Credentials{TokenSource: &MockTokenSource{
			AccessTokenString: "test-token",
			ExpiryTime:        time.Now().Add(time.Hour),
		}}, nil
	}
	t.Cleanup(func() {
		googleFindDefaultCredentials = originalFindDefaultCredentials
	})

	const upstreamToolCall = "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"id\":\"call-1\",\"type\":\"function\",\"function\":{\"name\":\"read\",\"arguments\":\"{}\"},\"extra_content\":{\"google\":{\"thought_signature\":\"vertex-signature\"}}}]}}]}\n\ndata: [DONE]\n\n"
	upstreamRequests := 0
	targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamRequests++
		if upstreamRequests == 1 {
			w.Header().Set("Content-Encoding", "gzip")
			w.Header().Set("Content-Type", "text/event-stream")
			compressor := gzip.NewWriter(w)
			if _, err := io.WriteString(compressor, upstreamToolCall); err != nil {
				t.Fatal(err)
			}
			if err := compressor.Close(); err != nil {
				t.Fatal(err)
			}
			return
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), `"thought_signature":"vertex-signature"`) {
			t.Errorf("recovered upstream request omitted signature: %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[]}`)
	}))
	t.Cleanup(targetServer.Close)

	targetURL, err := url.Parse(targetServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxy := makeProxy(targetURL)

	firstRequest := httptest.NewRequest(http.MethodPost, "http://localhost/v1/chat/completions", strings.NewReader(`{"messages":[]}`))
	firstRequest.Header.Set("Accept-Encoding", "gzip, deflate")
	firstResponse := httptest.NewRecorder()
	proxy.ServeHTTP(firstResponse, firstRequest)
	if got := firstResponse.Header().Get("Content-Encoding"); got != "" {
		t.Errorf("content encoding = %q, want identity", got)
	}
	if got := firstResponse.Body.String(); got != upstreamToolCall {
		t.Errorf("stream response = %q, want %q", got, upstreamToolCall)
	}

	secondRequest := httptest.NewRequest(http.MethodPost, "http://localhost/v1/chat/completions", strings.NewReader(unsignedToolResultRequest))
	secondResponse := httptest.NewRecorder()
	proxy.ServeHTTP(secondResponse, secondRequest)
	if upstreamRequests != 2 {
		t.Errorf("upstream requests = %d, want 2", upstreamRequests)
	}
}

func TestMakeProxy_CapturesNonStreamingVertexThoughtSignature(t *testing.T) {
	tokenMutex.Lock()
	originalToken, originalExpiry := token, expiry
	token, expiry = "", time.Time{}
	tokenMutex.Unlock()
	t.Cleanup(func() {
		tokenMutex.Lock()
		token, expiry = originalToken, originalExpiry
		tokenMutex.Unlock()
	})

	originalFindDefaultCredentials := googleFindDefaultCredentials
	googleFindDefaultCredentials = func(context.Context, ...string) (*google.Credentials, error) {
		return &google.Credentials{TokenSource: &MockTokenSource{
			AccessTokenString: "test-token",
			ExpiryTime:        time.Now().Add(time.Hour),
		}}, nil
	}
	t.Cleanup(func() {
		googleFindDefaultCredentials = originalFindDefaultCredentials
	})

	const upstreamToolCall = `{"choices":[{"message":{"tool_calls":[{"id":"call-1","extra_content":{"google":{"thought_signature":"vertex-signature"}}}]}}]}`
	upstreamRequests := 0
	targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamRequests++
		if upstreamRequests == 1 {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, upstreamToolCall)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), `"thought_signature":"vertex-signature"`) {
			t.Errorf("recovered upstream request omitted signature: %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[]}`)
	}))
	t.Cleanup(targetServer.Close)

	targetURL, err := url.Parse(targetServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxy := makeProxy(targetURL)
	proxy.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "http://localhost/v1/chat/completions", strings.NewReader(`{"messages":[]}`)))
	proxy.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "http://localhost/v1/chat/completions", strings.NewReader(unsignedToolResultRequest)))
	if upstreamRequests != 2 {
		t.Errorf("upstream requests = %d, want 2", upstreamRequests)
	}
}

func TestMakeProxy_DoesNotForwardClientAuthorizationWhenADCFails(t *testing.T) {
	tokenMutex.Lock()
	originalToken, originalExpiry := token, expiry
	token, expiry = "", time.Time{}
	tokenMutex.Unlock()
	t.Cleanup(func() {
		tokenMutex.Lock()
		token, expiry = originalToken, originalExpiry
		tokenMutex.Unlock()
	})

	originalFindDefaultCredentials := googleFindDefaultCredentials
	googleFindDefaultCredentials = func(context.Context, ...string) (*google.Credentials, error) {
		return nil, errors.New("ADC unavailable")
	}
	t.Cleanup(func() {
		googleFindDefaultCredentials = originalFindDefaultCredentials
	})

	upstreamRequests := 0
	targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamRequests++
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(targetServer.Close)

	targetURL, err := url.Parse(targetServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxy := makeProxy(targetURL)

	req := httptest.NewRequest(http.MethodGet, "http://localhost/v1/testpath", nil)
	req.Header.Set("Authorization", "Bearer proxy-secret")
	rr := httptest.NewRecorder()

	proxy.ServeHTTP(rr, req)

	if rr.Code >= http.StatusOK && rr.Code < http.StatusMultipleChoices {
		t.Errorf("proxy returned success status %d after ADC failure", rr.Code)
	}
	if upstreamRequests != 0 {
		t.Errorf("upstream received %d requests after ADC failure, want 0", upstreamRequests)
	}
}
