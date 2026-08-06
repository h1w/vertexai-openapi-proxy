package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

const modelCatalogTTL = 5 * time.Minute

// modelCatalog discovers the Google publisher model catalog and keeps one complete
// result set for a short period to avoid repeated Vertex API requests.
type modelCatalog struct {
	baseURL  string
	client   *http.Client
	getToken func(context.Context) (string, error)
	clock    func() time.Time

	mutex  sync.Mutex
	cached []Model
	expiry time.Time
}

func newModelCatalog(baseURL string, client *http.Client, getToken func(context.Context) (string, error), clock func() time.Time) *modelCatalog {
	if client == nil {
		client = http.DefaultClient
	}
	if clock == nil {
		clock = time.Now
	}
	return &modelCatalog{
		baseURL:  strings.TrimRight(baseURL, "/"),
		client:   client,
		getToken: getToken,
		clock:    clock,
	}
}

func (catalog *modelCatalog) models(ctx context.Context) ([]Model, error) {
	catalog.mutex.Lock()
	defer catalog.mutex.Unlock()

	now := catalog.clock()
	if !catalog.expiry.IsZero() && now.Before(catalog.expiry) {
		return cloneModels(catalog.cached), nil
	}

	models, err := catalog.refresh(ctx, now)
	if err != nil {
		return nil, err
	}
	catalog.cached = models
	catalog.expiry = now.Add(modelCatalogTTL)
	return cloneModels(catalog.cached), nil
}

func (catalog *modelCatalog) refresh(ctx context.Context, refreshedAt time.Time) ([]Model, error) {
	token, err := catalog.getToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("get access token: %w", err)
	}

	var models []Model
	pageToken := ""
	for {
		requestURL := catalog.baseURL + "/v1beta1/publishers/google/models?pageSize=100&listAllVersions=true&pageToken=" + url.QueryEscape(pageToken)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
		if err != nil {
			return nil, fmt.Errorf("create publisher model request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)

		resp, err := catalog.client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("list publisher models: %w", err)
		}
		if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
			resp.Body.Close()
			return nil, fmt.Errorf("list publisher models: unexpected status %s", resp.Status)
		}

		var page struct {
			PublisherModels []struct {
				Name string `json:"name"`
			} `json:"publisherModels"`
			NextPageToken string `json:"nextPageToken"`
		}
		decodeErr := json.NewDecoder(resp.Body).Decode(&page)
		resp.Body.Close()
		if decodeErr != nil {
			return nil, fmt.Errorf("decode publisher models: %w", decodeErr)
		}

		for _, publisherModel := range page.PublisherModels {
			const prefix = "publishers/google/models/"
			modelID, found := strings.CutPrefix(publisherModel.Name, prefix)
			if !found || modelID == "" {
				continue
			}
			models = append(models, Model{
				ID:      "google/" + modelID,
				Object:  "model",
				Created: refreshedAt.Unix(),
				OwnedBy: "google",
			})
		}

		if page.NextPageToken == "" {
			break
		}
		pageToken = page.NextPageToken
	}

	sort.Slice(models, func(i, j int) bool {
		return models[i].ID < models[j].ID
	})
	return models, nil
}

func cloneModels(models []Model) []Model {
	return append([]Model(nil), models...)
}

func newOpenAIModelsHandler(catalog *modelCatalog) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		models, err := catalog.models(r.Context())
		if err != nil {
			http.Error(w, "Unable to discover models", http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(ModelList{Object: "list", Data: models}); err != nil {
			logger.Error("encode OpenAI models response", "error", err)
		}
	})
}

func newNativeModelsHandler(catalog *modelCatalog) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		models, err := catalog.models(r.Context())
		if err != nil {
			http.Error(w, "Unable to discover models", http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(models); err != nil {
			logger.Error("encode native models response", "error", err)
		}
	})
}
