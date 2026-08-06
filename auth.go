package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strings"
)

func newAPIKeyAuth(apiKey string) func(http.Handler) http.Handler {
	configuredKeyDigest := sha256.Sum256([]byte(apiKey))
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			scheme, providedKey, hasCredential := strings.Cut(r.Header.Get("Authorization"), " ")
			providedKeyDigest := sha256.Sum256([]byte(providedKey))
			credentialsMatch := subtle.ConstantTimeCompare(configuredKeyDigest[:], providedKeyDigest[:]) == 1
			if apiKey == "" || !hasCredential || !strings.EqualFold(scheme, "Bearer") || !credentialsMatch {
				w.Header().Set("WWW-Authenticate", "Bearer")
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}

			next.ServeHTTP(w, r)

		})
	}
}
