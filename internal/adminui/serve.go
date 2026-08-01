package adminui

import (
	"context"
	"net/http"
	"time"

	"golang.org/x/crypto/acme/autocert"
)

// ListenAndServe runs the gateway until ctx is cancelled.
//
// TLS is decided by ADMIN_DOMAIN. With a domain, certificates are obtained and
// renewed automatically from Let's Encrypt — no certbot, no cron, no second
// container. Without one, plain HTTP, which is the right thing for reaching the
// panel through an SSH tunnel while a domain is not set up yet.
func (g *Gateway) ListenAndServe(ctx context.Context) error {
	g.srv = &http.Server{
		Addr:              g.cfg.Addr,
		Handler:           g.mux,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	errCh := make(chan error, 1)
	if g.cfg.Domain == "" {
		g.logf("[adminui] serving plain HTTP on %s (no ADMIN_DOMAIN, so no TLS)", g.cfg.Addr)
		go func() { errCh <- g.srv.ListenAndServe() }()
	} else {
		m := &autocert.Manager{
			Prompt: autocert.AcceptTOS,
			// Without HostPolicy the manager would request a certificate for any
			// name in an incoming SNI, which is how a public server becomes
			// somebody else's certificate mill and gets rate-limited.
			HostPolicy: autocert.HostWhitelist(g.cfg.Domain),
			Cache:      autocert.DirCache(g.cfg.CertCache),
		}
		g.srv.Addr = ":443"
		g.srv.TLSConfig = m.TLSConfig()

		// Port 80 serves the ACME challenge and redirects everything else. Let's
		// Encrypt needs it reachable to issue the certificate at all.
		go func() {
			redirect := m.HTTPHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, "https://"+g.cfg.Domain+r.URL.RequestURI(), http.StatusMovedPermanently)
			}))
			if err := http.ListenAndServe(":80", redirect); err != nil && err != http.ErrServerClosed {
				g.logf("[adminui] port 80 (ACME challenge and redirect): %v", err)
			}
		}()

		g.logf("[adminui] serving https://%s", g.cfg.Domain)
		go func() { errCh <- g.srv.ListenAndServeTLS("", "") }()
	}

	select {
	case err := <-errCh:
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return g.srv.Shutdown(shutdownCtx)
	}
}
