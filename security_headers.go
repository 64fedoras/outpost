package main

import "net/http"

// The web UI has no inline scripts, inline event handlers or style attributes (web/ keeps to
// that; TestWebHasNoInlineCode checks it), so the policy needs no 'unsafe-inline'. Node WebSockets
// and downloads are not browser pages, so the same policy on them does no harm.
const contentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self'; " +
	"img-src 'self' data: blob:; connect-src 'self'; object-src 'none'; " +
	"frame-ancestors 'none'; base-uri 'none'; form-action 'self'"

// secureHeaders sets browser hardening headers on every controller response. There is no
// Strict-Transport-Security on purpose: the controller serves a self-signed certificate, and HSTS
// would remove the browser's "continue anyway" option and lock the owner out after a cert change.
func secureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		h.Set("X-Frame-Options", "DENY")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		// Notifications use the Notification API, which this policy does not cover.
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
		next.ServeHTTP(w, r)
	})
}
