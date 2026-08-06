package main

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

func newAPIKeyAuth(apiKey string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			scheme, providedKey, hasCredential := strings.Cut(r.Header.Get("Authorization"), " ")
			if apiKey == "" || !hasCredential || !strings.EqualFold(scheme, "Bearer") {
				w.Header().Set("WWW-Authenticate", "Bearer")
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}

			if len(providedKey) != len(apiKey) || subtle.ConstantTimeCompare([]byte(providedKey), []byte(apiKey)) != 1 {
				w.Header().Set("WWW-Authenticate", "Bearer")
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
