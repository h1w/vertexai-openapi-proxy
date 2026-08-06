package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAPIKeyAuth(t *testing.T) {
	tests := []struct {
		name          string
		authorization string
		wantStatus    int
	}{
		{name: "missing header", wantStatus: http.StatusUnauthorized},
		{name: "non-Bearer scheme", authorization: "Basic secret", wantStatus: http.StatusUnauthorized},
		{name: "incorrect key", authorization: "Bearer incorrect", wantStatus: http.StatusUnauthorized},
		{name: "exact Bearer key", authorization: "Bearer secret", wantStatus: http.StatusNoContent},
		{name: "lowercase bearer key", authorization: "bearer secret", wantStatus: http.StatusNoContent},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			})
			handler := newAPIKeyAuth("secret")(next)
			req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
			if tt.authorization != "" {
				req.Header.Set("Authorization", tt.authorization)
			}
			recorder := httptest.NewRecorder()

			handler.ServeHTTP(recorder, req)

			if recorder.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", recorder.Code, tt.wantStatus)
			}
			if tt.wantStatus == http.StatusUnauthorized && recorder.Header().Get("WWW-Authenticate") != "Bearer" {
				t.Errorf("WWW-Authenticate = %q, want %q", recorder.Header().Get("WWW-Authenticate"), "Bearer")
			}
		})
	}
}
