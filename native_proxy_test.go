package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"time"
	"testing"
)

func TestNativeModelProxy(t *testing.T) {
	var upstreamCalls atomic.Int32
	body := []byte(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		if got, want := r.Method, http.MethodPost; got != want {
			t.Errorf("upstream method = %q, want %q", got, want)
		}
		switch r.URL.Path {
		case "/v1/projects/project/locations/europe-central2/publishers/google/models/gemini-test:generateContent",
			"/v1/projects/project/locations/europe-central2/publishers/google/models/textembedding-gecko@001:predict":
		default:
			t.Errorf("upstream path = %q", r.URL.Path)
		}
		if got, want := r.Header.Get("Content-Type"), "application/json"; got != want {
			t.Errorf("upstream content type = %q, want %q", got, want)
		}
		if got, want := r.Header.Get("Authorization"), "Bearer adc-token"; got != want {
			t.Errorf("upstream authorization = %q, want %q", got, want)
		}
		gotBody, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read upstream body: %v", err)
		}
		if !bytes.Equal(gotBody, body) {
			t.Errorf("upstream body = %q, want %q", gotBody, body)
		}

		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("X-Vertex-Request-ID", "request-123")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"candidates":[]}`))
	}))
	defer upstream.Close()

	handler := newNativeModelProxy(upstream.URL, "project", "europe-central2", func(context.Context) (string, error) {
		return "adc-token", nil
	}, upstream.Client())

	t.Run("forwards allowed native inference", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/vertex/v1/models/google/gemini-test:generateContent", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer client-token")
		recorder := httptest.NewRecorder()

		handler.ServeHTTP(recorder, req)

		if got, want := recorder.Code, http.StatusCreated; got != want {
			t.Fatalf("status = %d, want %d: %s", got, want, recorder.Body.String())
		}
		if got, want := recorder.Header().Get("Content-Type"), "application/json; charset=utf-8"; got != want {
			t.Errorf("content type = %q, want %q", got, want)
		}
		if got, want := recorder.Header().Get("X-Vertex-Request-ID"), "request-123"; got != want {
			t.Errorf("request ID = %q, want %q", got, want)
		}
		if got, want := recorder.Body.String(), `{"candidates":[]}`; got != want {
			t.Errorf("body = %q, want %q", got, want)
		}
	})
	t.Run("forwards versioned Google model", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/vertex/v1/models/google/textembedding-gecko@001:predict", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()

		handler.ServeHTTP(recorder, req)

		if got, want := recorder.Code, http.StatusCreated; got != want {
			t.Fatalf("status = %d, want %d: %s", got, want, recorder.Body.String())
		}
	})

	for _, test := range []struct {
		name       string
		method     string
		path       string
		wantStatus int
	}{
		{name: "GET", method: http.MethodGet, path: "/vertex/v1/models/google/gemini-test:generateContent", wantStatus: http.StatusMethodNotAllowed},
		{name: "publisher metadata", method: http.MethodPost, path: "/vertex/v1/models/meta/gemini-test:generateContent", wantStatus: http.StatusNotFound},
		{name: "malformed path", method: http.MethodPost, path: "/vertex/v1/models/google/../../endpoints:predict", wantStatus: http.StatusNotFound},
		{name: "unknown action", method: http.MethodPost, path: "/vertex/v1/models/google/gemini-test:delete", wantStatus: http.StatusNotFound},
		{name: "missing action", method: http.MethodPost, path: "/vertex/v1/models/google/gemini-test", wantStatus: http.StatusNotFound},
	} {
		t.Run("rejects "+test.name, func(t *testing.T) {
			before := upstreamCalls.Load()
			req := httptest.NewRequest(test.method, test.path, nil)
			recorder := httptest.NewRecorder()

			handler.ServeHTTP(recorder, req)

			if got := recorder.Code; got != test.wantStatus {
				t.Errorf("status = %d, want %d", got, test.wantStatus)
			}
			if got := upstreamCalls.Load(); got != before {
				t.Errorf("upstream calls = %d, want %d", got, before)
			}
		})
	}

	t.Run("returns bad gateway when ADC fails", func(t *testing.T) {
		before := upstreamCalls.Load()
		handler := newNativeModelProxy(upstream.URL, "project", "europe-central2", func(context.Context) (string, error) {
			return "", errors.New("ADC unavailable")
		}, upstream.Client())
		req := httptest.NewRequest(http.MethodPost, "/vertex/v1/models/google/gemini-test:generateContent", nil)
		recorder := httptest.NewRecorder()

		handler.ServeHTTP(recorder, req)

		if got, want := recorder.Code, http.StatusBadGateway; got != want {
			t.Errorf("status = %d, want %d", got, want)
		}
		if got := upstreamCalls.Load(); got != before {
			t.Errorf("upstream calls = %d, want %d", got, before)
		}
	})

	t.Run("returns bad gateway on transport failure", func(t *testing.T) {
		handler := newNativeModelProxy("https://vertex.example", "project", "europe-central2", func(context.Context) (string, error) {
			return "adc-token", nil
		}, &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("network unavailable")
		})})
		req := httptest.NewRequest(http.MethodPost, "/vertex/v1/models/google/gemini-test:generateContent", nil)
		recorder := httptest.NewRecorder()

		handler.ServeHTTP(recorder, req)

		if got, want := recorder.Code, http.StatusBadGateway; got != want {
			t.Errorf("status = %d, want %d", got, want)
		}
	})
	t.Run("preserves compressed upstream responses", func(t *testing.T) {
		var encoded bytes.Buffer
		compressor := gzip.NewWriter(&encoded)
		if _, err := compressor.Write([]byte(`{"candidates":[]}`)); err != nil {
			t.Fatal(err)
		}
		if err := compressor.Close(); err != nil {
			t.Fatal(err)
		}

		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Encoding", "gzip")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write(encoded.Bytes())
		}))
		defer upstream.Close()

		handler := newNativeModelProxy(upstream.URL, "project", "europe-central2", func(context.Context) (string, error) {
			return "adc-token", nil
		}, upstream.Client())
		req := httptest.NewRequest(http.MethodPost, "/vertex/v1/models/google/gemini-test:generateContent", nil)
		recorder := httptest.NewRecorder()

		handler.ServeHTTP(recorder, req)

		if got, want := recorder.Header().Get("Content-Encoding"), "gzip"; got != want {
			t.Errorf("content encoding = %q, want %q", got, want)
		}
		if got := recorder.Body.Bytes(); !bytes.Equal(got, encoded.Bytes()) {
			t.Errorf("response body = %q, want compressed bytes %q", got, encoded.Bytes())
		}
	})

	t.Run("preserves upstream redirects", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/final" {
				_, _ = w.Write([]byte("final"))
				return
			}
			w.Header().Set("Location", "/final")
			w.WriteHeader(http.StatusTemporaryRedirect)
			_, _ = w.Write([]byte("redirect"))
		}))
		defer upstream.Close()

		handler := newNativeModelProxy(upstream.URL, "project", "europe-central2", func(context.Context) (string, error) {
			return "adc-token", nil
		}, upstream.Client())
		req := httptest.NewRequest(http.MethodPost, "/vertex/v1/models/google/gemini-test:generateContent", nil)
		recorder := httptest.NewRecorder()

		handler.ServeHTTP(recorder, req)

		if got, want := recorder.Code, http.StatusTemporaryRedirect; got != want {
			t.Errorf("status = %d, want %d", got, want)
		}
		if got, want := recorder.Header().Get("Location"), "/final"; got != want {
			t.Errorf("location = %q, want %q", got, want)
		}
		if got, want := recorder.Body.String(), "redirect"; got != want {
			t.Errorf("body = %q, want %q", got, want)
		}
	})

	t.Run("flushes streamed responses", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"candidates":[]}`))
		}))
		defer upstream.Close()

		handler := newNativeModelProxy(upstream.URL, "project", "europe-central2", func(context.Context) (string, error) {
			return "adc-token", nil
		}, upstream.Client())
		req := httptest.NewRequest(http.MethodPost, "/vertex/v1/models/google/gemini-test:streamGenerateContent", nil)
		recorder := httptest.NewRecorder()

		handler.ServeHTTP(recorder, req)

		if !recorder.Flushed {
			t.Error("streamed response was not flushed")
		}
	})

	t.Run("rejects dot segments before ServeMux canonicalization", func(t *testing.T) {
		var calls atomic.Int32
		upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			calls.Add(1)
		}))
		defer upstream.Close()

		mux := http.NewServeMux()
		mux.Handle(nativeModelsPrefix, newNativeModelProxy(upstream.URL, "project", "europe-central2", func(context.Context) (string, error) {
			return "adc-token", nil
		}, upstream.Client()))
		handler := newNativeModelDispatcher(mux)
		req := httptest.NewRequest(http.MethodPost, "/vertex/v1/models/google/../google/gemini-test:generateContent", nil)
		recorder := httptest.NewRecorder()

		handler.ServeHTTP(recorder, req)

		if got, want := recorder.Code, http.StatusNotFound; got != want {
			t.Errorf("status = %d, want %d", got, want)
		}
		if got := calls.Load(); got != 0 {
			t.Errorf("upstream calls = %d, want 0", got)
		}
	})

	t.Run("flushes upstream headers before a delayed body", func(t *testing.T) {
		bodyRelease := make(chan struct{})
		headersWritten := make(chan struct{})
		released := false
		releaseBody := func() {
			if !released {
				close(bodyRelease)
				released = true
			}
		}

		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("X-Vertex-Request-ID", "request-headers-first")
			w.WriteHeader(http.StatusCreated)
			w.(http.Flusher).Flush()
			close(headersWritten)
			<-bodyRelease
			_, _ = w.Write([]byte(`{"candidates":[]}`))
		}))
		defer upstream.Close()

		proxy := httptest.NewServer(newNativeModelProxy(upstream.URL, "project", "europe-central2", func(context.Context) (string, error) {
			return "adc-token", nil
		}, upstream.Client()))
		defer proxy.Close()
		defer releaseBody()

		request, err := http.NewRequest(http.MethodPost, proxy.URL+"/vertex/v1/models/google/gemini-test:streamGenerateContent", nil)
		if err != nil {
			t.Fatal(err)
		}
		responses := make(chan struct {
			response *http.Response
			err      error
		}, 1)
		go func() {
			response, err := proxy.Client().Do(request)
			responses <- struct {
				response *http.Response
				err      error
			}{response, err}
		}()

		select {
		case <-headersWritten:
		case <-time.After(time.Second):
			t.Fatal("upstream did not send headers")
		}

		var response *http.Response
		select {
		case result := <-responses:
			if result.err != nil {
				t.Fatal(result.err)
			}
			response = result.response
		case <-time.After(time.Second):
			t.Fatal("client did not receive upstream headers before the body")
		}
		defer response.Body.Close()

		if got, want := response.StatusCode, http.StatusCreated; got != want {
			t.Errorf("status = %d, want %d", got, want)
		}
		if got, want := response.Header.Get("X-Vertex-Request-ID"), "request-headers-first"; got != want {
			t.Errorf("request ID = %q, want %q", got, want)
		}

		releaseBody()
		gotBody, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		if got, want := string(gotBody), `{"candidates":[]}`; got != want {
			t.Errorf("body = %q, want %q", got, want)
		}
	})

}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}
