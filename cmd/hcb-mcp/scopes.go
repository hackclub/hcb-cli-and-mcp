package main

import (
	"fmt"
	"strings"
)

const defaultOAuthScope = "read"

var supportedOAuthScopes = []string{"read", "admin:read"}

func normalizeOAuthScope(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return defaultOAuthScope, nil
	}
	allowed := map[string]bool{}
	for _, scope := range supportedOAuthScopes {
		allowed[scope] = true
	}
	seen := map[string]bool{}
	var scopes []string
	for _, scope := range strings.Fields(raw) {
		if !allowed[scope] {
			return "", fmt.Errorf("unsupported OAuth scope %q", scope)
		}
		if !seen[scope] {
			seen[scope] = true
			scopes = append(scopes, scope)
		}
	}
	if len(scopes) == 0 {
		return defaultOAuthScope, nil
	}
	return strings.Join(scopes, " "), nil
}
