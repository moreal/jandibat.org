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
	"go.uber.org/zap"
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

func observeHTTP(registry *observability.Registry, logger *zap.Logger) func(stdhttp.Handler) stdhttp.Handler {
	return func(next stdhttp.Handler) stdhttp.Handler {
		return stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
			started := time.Now()
			wrapped := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			completed := false
			defer func() {
				route := chi.RouteContext(r.Context()).RoutePattern()
				if route == "" {
					route = "unmatched"
				}
				method := boundedRequestMethod(r.Method)
				status := wrapped.Status()
				outcome := "aborted"
				if completed {
					outcome = "completed"
					if status == 0 {
						status = stdhttp.StatusOK
					}
				}
				elapsed := time.Since(started)
				observability.Log(logger, "http.request_completed",
					observability.SafeString("request_id", middleware.GetReqID(r.Context())),
					zap.String("method", method),
					zap.String("operation", method+" "+route),
					zap.String("outcome", outcome),
					zap.Int("status", status),
					zap.Duration("duration", elapsed),
				)
				// A scrape is operator instrumentation traffic, not an eligible user
				// request. Excluding it also prevents each scrape from changing the
				// family currently being collected.
				if route != "/metrics" {
					registry.ObserveHTTP(route, method, status, elapsed)
				}
			}()
			next.ServeHTTP(wrapped, r)
			completed = true
		})
	}
}

func boundedRequestMethod(method string) string {
	switch strings.ToUpper(strings.TrimSpace(method)) {
	case stdhttp.MethodGet:
		return stdhttp.MethodGet
	case stdhttp.MethodHead:
		return stdhttp.MethodHead
	case stdhttp.MethodPost:
		return stdhttp.MethodPost
	case stdhttp.MethodPut:
		return stdhttp.MethodPut
	case stdhttp.MethodPatch:
		return stdhttp.MethodPatch
	case stdhttp.MethodDelete:
		return stdhttp.MethodDelete
	case stdhttp.MethodConnect:
		return stdhttp.MethodConnect
	case stdhttp.MethodOptions:
		return stdhttp.MethodOptions
	case stdhttp.MethodTrace:
		return stdhttp.MethodTrace
	default:
		return "OTHER"
	}
}
