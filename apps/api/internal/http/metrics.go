package apihttp

import (
	"context"
	"net"
	stdhttp "net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/moreal/jandibat.org/apps/api/internal/observability"
)

type peerAddressContextKey struct{}

func capturePeerAddress(next stdhttp.Handler) stdhttp.Handler {
	return stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		ctx := context.WithValue(r.Context(), peerAddressContextKey{}, r.RemoteAddr)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func loopbackMetrics(handler stdhttp.Handler) stdhttp.Handler {
	return stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		peer, _ := r.Context().Value(peerAddressContextKey{}).(string)
		if peer == "" {
			peer = r.RemoteAddr
		}
		host, _, err := net.SplitHostPort(strings.TrimSpace(peer))
		if err != nil {
			host = strings.TrimSpace(peer)
		}
		address := net.ParseIP(host)
		forwarded := strings.TrimSpace(r.Header.Get("Forwarded")) != "" || strings.TrimSpace(r.Header.Get("X-Forwarded-For")) != ""
		if address == nil || !address.IsLoopback() || forwarded {
			// Operational labels contain deployment and dependency state. Do not
			// make that inventory available through the public API surface.
			stdhttp.Error(w, "forbidden", stdhttp.StatusForbidden)
			return
		}
		handler.ServeHTTP(w, r)
	})
}

func observeHTTP(registry *observability.Registry) func(stdhttp.Handler) stdhttp.Handler {
	return func(next stdhttp.Handler) stdhttp.Handler {
		return stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
			started := time.Now()
			wrapped := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(wrapped, r)

			route := chi.RouteContext(r.Context()).RoutePattern()
			// A scrape is operator instrumentation traffic, not an eligible user
			// request. Excluding it also prevents each scrape from changing the
			// family currently being collected.
			if route == "/metrics" {
				return
			}
			status := wrapped.Status()
			if status == 0 {
				status = stdhttp.StatusOK
			}
			registry.ObserveHTTP(route, r.Method, status, time.Since(started))
		})
	}
}
