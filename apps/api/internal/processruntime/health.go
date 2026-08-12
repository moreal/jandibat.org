package processruntime

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/moreal/jandibat.org/apps/api/internal/operations"
)

type Readiness interface {
	Check(context.Context) operations.ReadinessReport
}

func NewHealthHandler(liveness operations.Liveness, readiness Readiness) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /livez", func(w http.ResponseWriter, _ *http.Request) {
		status := http.StatusOK
		if !liveness.Live() {
			status = http.StatusServiceUnavailable
		}
		writeHealth(w, status, map[string]any{"live": status == http.StatusOK})
	})
	ready := func(w http.ResponseWriter, r *http.Request) {
		report := operations.ReadinessReport{Ready: readiness != nil}
		if readiness != nil {
			report = readiness.Check(r.Context())
		}
		status := http.StatusOK
		if !report.Ready {
			status = http.StatusServiceUnavailable
		}
		writeHealth(w, status, report)
	}
	mux.HandleFunc("GET /readyz", ready)
	mux.HandleFunc("GET /healthz", ready)
	return mux
}

func writeHealth(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
