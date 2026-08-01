package adminui

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"discordAudio/internal/adminauth"
)

// newProxy builds a reverse proxy to one backend service.
//
// prefix is the gateway-side path ("/api/bot"), which is replaced by "/admin" on
// the way out: the panel talks to /api/bot/state and the bot answers
// /admin/state. Keeping the two namespaces distinct means a backend route can
// never be reached by a path the panel did not intend.
func (g *Gateway) newProxy(target *url.URL, token, prefix string) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(target)
			r.Out.URL.Path = "/admin" + strings.TrimPrefix(r.In.URL.Path, prefix)
			r.Out.URL.RawQuery = r.In.URL.RawQuery

			// Order matters and this is the security-critical part of the whole
			// gateway.
			//
			// 1. Strip every X-Admin-* header the client sent. r.Out starts as a
			//    copy of the incoming request, so a browser that sends
			//    "X-Admin-Role: owner" would otherwise have it forwarded — and
			//    with our service token attached, the backend would believe it.
			//    Stripping by prefix rather than by name so a header added later
			//    is covered without anyone having to remember this function.
			stripAdminHeaders(r.Out.Header)

			// 2. Attach the identity WE established, and the service token. The
			//    token lives only on this side: it is never sent to the browser,
			//    so a stolen session cannot be replayed against the backends
			//    directly.
			sess, ok := sessionFrom(r.In.Context())
			if !ok {
				return // guarded upstream; without a session nothing is attached
			}
			r.Out.Header.Set(adminauth.HeaderToken, token)
			r.Out.Header.Set(adminauth.HeaderUserID, sess.UserID)
			r.Out.Header.Set(adminauth.HeaderUserName, sess.UserName)
			r.Out.Header.Set(adminauth.HeaderRole, sess.Role.String())
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			// A backend being down is normal during a deploy and must read as
			// such in the panel, not as a panel bug.
			// The log line is English like the rest of the code; the message is
			// Russian because it is rendered in the panel, next to Russian labels.
			g.logf("[adminui] %s %s: %v", r.Method, r.URL.Path, err)
			writeJSON(w, http.StatusBadGateway, map[string]string{
				"error": "Сервис не отвечает",
			})
		},
	}
}

// stripAdminHeaders removes every X-Admin-* header. Canonical form is what Go
// stores, but the check is case-insensitive anyway: nothing should depend on a
// caller's capitalisation for a security decision.
func stripAdminHeaders(h http.Header) {
	for name := range h {
		if strings.HasPrefix(strings.ToLower(name), "x-admin-") {
			h.Del(name)
		}
	}
}
