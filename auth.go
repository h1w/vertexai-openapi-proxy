package main

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

const bearerPrefix = "Bearer "

func newAPIKeyAuth(apiKey string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authorization := r.Header.Get("Authorization")
			if apiKey == "" || !strings.HasPrefix(authorization, bearerPrefix) {
				w.Header().Set("WWW-Authenticate", "Bearer")
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}

			providedKey := authorization[len(bearerPrefix):]
			if len(providedKey) != len(apiKey) || subtle.ConstantTimeCompare([]byte(providedKey), []byte(apiKey)) != 1 {
				w.Header().Set("WWW-Authenticate", "Bearer")
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
