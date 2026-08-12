package apihttp

import (
	"net/http"
	"strings"
)

const privateNoStore = "private, no-store"

// cachePrivateByDefault prevents a new API response from becoming cacheable
// merely because a handler forgot to declare a policy. Handlers may opt a
// response into shared caching by setting an explicit public Cache-Control
// value before writing the response.
func cachePrivateByDefault(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wrapped := &cachePolicyResponseWriter{ResponseWriter: w}
		next.ServeHTTP(wrapped, r)
		wrapped.finalize()
	})
}

type cachePolicyResponseWriter struct {
	http.ResponseWriter
	finalized bool
}

func (w *cachePolicyResponseWriter) WriteHeader(status int) {
	w.finalize()
	w.ResponseWriter.WriteHeader(status)
}

func (w *cachePolicyResponseWriter) Write(body []byte) (int, error) {
	w.finalize()
	return w.ResponseWriter.Write(body)
}

// Unwrap lets net/http.ResponseController reach optional capabilities of the
// original writer without making this middleware advertise unsupported ones.
func (w *cachePolicyResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (w *cachePolicyResponseWriter) finalize() {
	if w.finalized {
		return
	}
	w.finalized = true

	header := w.Header()
	cacheControl := header.Get("Cache-Control")
	if strings.TrimSpace(cacheControl) == "" {
		cacheControl = privateNoStore
		header.Set("Cache-Control", cacheControl)
	}
	if permitsSharedCaching(cacheControl) {
		removeVary(header, "Cookie", "Authorization")
		return
	}
	addVary(header, "Cookie", "Authorization")
}

func permitsSharedCaching(cacheControl string) bool {
	public := false
	for _, rawDirective := range strings.Split(cacheControl, ",") {
		directive := strings.TrimSpace(strings.SplitN(rawDirective, "=", 2)[0])
		switch {
		case strings.EqualFold(directive, "public"):
			public = true
		case strings.EqualFold(directive, "private"), strings.EqualFold(directive, "no-store"):
			return false
		}
	}
	return public
}

func addVary(header http.Header, names ...string) {
	if varyContains(header, "*") {
		return
	}
	for _, name := range names {
		if !varyContains(header, name) {
			header.Add("Vary", name)
		}
	}
}

func removeVary(header http.Header, names ...string) {
	blocked := make(map[string]struct{}, len(names))
	for _, name := range names {
		blocked[strings.ToLower(name)] = struct{}{}
	}
	kept := make([]string, 0)
	for _, value := range header.Values("Vary") {
		for _, rawName := range strings.Split(value, ",") {
			name := strings.TrimSpace(rawName)
			if name == "" {
				continue
			}
			if _, remove := blocked[strings.ToLower(name)]; !remove {
				kept = append(kept, name)
			}
		}
	}
	header.Del("Vary")
	for _, name := range kept {
		header.Add("Vary", name)
	}
}

func varyContains(header http.Header, wanted string) bool {
	for _, value := range header.Values("Vary") {
		for _, name := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(name), wanted) {
				return true
			}
		}
	}
	return false
}
