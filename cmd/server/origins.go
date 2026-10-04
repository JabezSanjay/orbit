package main

import "strings"

// parseAllowedOrigins splits ORBIT_ALLOWED_ORIGINS into its entries and derives
// the host patterns for the WebSocket upgrade.
//
// Entries may be written with or without a scheme ("http://localhost:5173" or
// "localhost:5173"). coder/websocket's OriginPatterns are matched against the
// Origin header's host:port only, so a scheme-qualified entry would never match
// there; wsHosts therefore has the scheme (and any trailing slash or path)
// stripped. corsOrigins keeps the scheme, and originAllowed matches it against
// scheme://host when present, so CORS stays as strict as the entry is written.
func parseAllowedOrigins(raw string) (corsOrigins, wsHosts []string) {
	seen := make(map[string]bool)
	for _, o := range strings.Split(raw, ",") {
		o = strings.TrimRight(strings.TrimSpace(o), "/")
		if o == "" {
			continue
		}
		corsOrigins = append(corsOrigins, o)

		host := o
		if _, rest, ok := strings.Cut(host, "://"); ok {
			host = rest
		}
		host, _, _ = strings.Cut(host, "/")
		if host != "" && !seen[strings.ToLower(host)] {
			seen[strings.ToLower(host)] = true
			wsHosts = append(wsHosts, host)
		}
	}
	return corsOrigins, wsHosts
}
