package main

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

const nativeModelsPrefix = "/vertex/v1/models/"

var nativeModelName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._@-]*$`)

func newNativeModelDispatcher(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, nativeModelsPrefix) && nativePathHasDotSegment(r.URL.Path) {
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func nativePathHasDotSegment(path string) bool {
	for _, segment := range strings.Split(strings.TrimPrefix(path, nativeModelsPrefix), "/") {
		if segment == "." || segment == ".." {
			return true
		}
	}
	return false
}

var nativeActions = map[string]struct{}{
	"generateContent":         {},
	"streamGenerateContent":   {},
	"embedContent":            {},
	"predict":                 {},
	"rawPredict":              {},
	"streamRawPredict":        {},
	"serverStreamingPredict":  {},
	"predictLongRunning":      {},
	"fetchPredictOperation":   {},
}

func newNativeModelProxy(baseURL, project, location string, token func(context.Context) (string, error), client *http.Client) http.Handler {
	base, err := url.Parse(baseURL)
	if err != nil || base.Scheme == "" || base.Host == "" {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "Proxy configuration failed", http.StatusBadGateway)
		})
	}
	if client == nil {
		client = http.DefaultClient
	}
	proxyClient := *client
	proxyClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	client = &proxyClient

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		model, action, ok := nativeModelAction(r.URL.Path)
		if !ok {
			http.NotFound(w, r)
			return
		}

		adcToken, err := token(r.Context())
		if err != nil {
			http.Error(w, "Proxy authentication failed", http.StatusBadGateway)
			return
		}

		upstreamURL := &url.URL{
			Scheme:   base.Scheme,
			Host:     base.Host,
			Path:     "/v1/projects/" + project + "/locations/" + location + "/publishers/google/models/" + model + ":" + action,
			RawQuery: r.URL.RawQuery,
		}
		upstreamRequest, err := http.NewRequestWithContext(r.Context(), http.MethodPost, upstreamURL.String(), r.Body)
		if err != nil {
			http.Error(w, "Proxy request creation failed", http.StatusBadGateway)
			return
		}
		upstreamRequest.ContentLength = r.ContentLength
		upstreamRequest.Header.Set("Authorization", "Bearer "+adcToken)
		upstreamRequest.Header.Set("Accept-Encoding", "identity")
		if contentType := r.Header.Get("Content-Type"); contentType != "" {
			upstreamRequest.Header.Set("Content-Type", contentType)
		}

		response, err := client.Do(upstreamRequest)
		if err != nil {
			http.Error(w, "Proxy request failed", http.StatusBadGateway)
			return
		}
		defer response.Body.Close()

		for key, values := range response.Header {
			w.Header()[key] = append([]string(nil), values...)
		}
		w.WriteHeader(response.StatusCode)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		_, _ = io.Copy(flushingResponseWriter{ResponseWriter: w}, response.Body)
	})
}

type flushingResponseWriter struct {
	http.ResponseWriter
}

func (w flushingResponseWriter) Write(body []byte) (int, error) {
	written, err := w.ResponseWriter.Write(body)
	if written > 0 {
		if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
			flusher.Flush()
		}
	}
	return written, err
}

func nativeModelAction(path string) (string, string, bool) {
	if !strings.HasPrefix(path, nativeModelsPrefix) {
		return "", "", false
	}

	segments := strings.Split(strings.TrimPrefix(path, nativeModelsPrefix), "/")
	if len(segments) != 2 || segments[0] != "google" {
		return "", "", false
	}

	model, action, hasAction := strings.Cut(segments[1], ":")
	if !hasAction || !nativeModelName.MatchString(model) {
		return "", "", false
	}
	if _, supported := nativeActions[action]; !supported {
		return "", "", false
	}
	return model, action, true
}
