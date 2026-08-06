package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

func TestModelCatalog(t *testing.T) {
	t.Run("refreshes every page and serves sorted cache", func(t *testing.T) {
		var requests int
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requests++
			if got := r.Header.Get("Authorization"); got != "Bearer adc-token" {
				t.Errorf("Authorization = %q, want %q", got, "Bearer adc-token")
			}
			if got := r.URL.Query().Get("pageSize"); got != "100" {
				t.Errorf("pageSize = %q, want 100", got)
			}
			if got := r.URL.Query().Get("listAllVersions"); got != "true" {
				t.Errorf("listAllVersions = %q, want true", got)
			}

			switch r.URL.Query().Get("pageToken") {
			case "":
				json.NewEncoder(w).Encode(map[string]any{
					"publisherModels": []map[string]string{{"name": "publishers/google/models/gemini-first"}},
					"nextPageToken": "page-2",
				})
			case "page-2":
				json.NewEncoder(w).Encode(map[string]any{
					"publisherModels": []map[string]string{{"name": "publishers/google/models/gemini-second"}},
				})
			default:
				t.Errorf("pageToken = %q, want empty or page-2", r.URL.Query().Get("pageToken"))
				w.WriteHeader(http.StatusBadRequest)
			}
		}))
		defer server.Close()

		catalog := &modelCatalog{
			baseURL: server.URL,
			client:  server.Client(),
			getToken: func(context.Context) (string, error) {
				return "adc-token", nil
			},
			clock: func() time.Time { return time.Unix(1_700_000_000, 0) },
		}

		want := []Model{
			{ID: "google/gemini-first", Object: "model", Created: 1_700_000_000, OwnedBy: "google"},
			{ID: "google/gemini-second", Object: "model", Created: 1_700_000_000, OwnedBy: "google"},
		}
		for range 2 {
			got, err := catalog.models(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("models() = %#v, want %#v", got, want)
			}
		}
		if requests != 2 {
			t.Errorf("upstream requests = %d, want 2", requests)
		}
	})

	t.Run("returns error without a valid cache", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer server.Close()

		catalog := &modelCatalog{
			baseURL: server.URL,
			client:  server.Client(),
			getToken: func(context.Context) (string, error) {
				return "adc-token", nil
			},
			clock: time.Now,
		}

		if _, err := catalog.models(context.Background()); err == nil {
			t.Fatal("models() error = nil, want error")
		}
	})
}

func TestOpenAIModelsHandler(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"publisherModels": []map[string]string{
				{"name": "publishers/google/models/gemini-second"},
				{"name": "publishers/google/models/gemini-first"},
			},
		})
	}))
	defer server.Close()

	catalog := &modelCatalog{
		baseURL: server.URL,
		client:  server.Client(),
		getToken: func(context.Context) (string, error) {
			return "adc-token", nil
		},
		clock: func() time.Time { return time.Unix(1_700_000_000, 0) },
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	recorder := httptest.NewRecorder()
	newOpenAIModelsHandler(catalog).ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	var got ModelList
	if err := json.NewDecoder(recorder.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	want := ModelList{
		Object: "list",
		Data: []Model{
			{ID: "google/gemini-first", Object: "model", Created: 1_700_000_000, OwnedBy: "google"},
			{ID: "google/gemini-second", Object: "model", Created: 1_700_000_000, OwnedBy: "google"},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("response = %#v, want %#v", got, want)
	}

	failureCatalog := &modelCatalog{
		baseURL: "http://invalid.example",
		client:  server.Client(),
		getToken: func(context.Context) (string, error) {
			return "", errors.New("ADC unavailable")
		},
		clock: time.Now,
	}
	failureRecorder := httptest.NewRecorder()
	newOpenAIModelsHandler(failureCatalog).ServeHTTP(failureRecorder, req)
	if failureRecorder.Code != http.StatusBadGateway {
		t.Errorf("failure status = %d, want %d", failureRecorder.Code, http.StatusBadGateway)
	}
}
