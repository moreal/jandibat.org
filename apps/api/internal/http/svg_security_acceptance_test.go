package apihttp_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	appactivity "github.com/moreal/jandibat.org/apps/api/internal/application/activity"
	domain "github.com/moreal/jandibat.org/apps/api/internal/domain/activity"
	apihttp "github.com/moreal/jandibat.org/apps/api/internal/http"
)

type svgSnapshotTimeline struct{}

func (svgSnapshotTimeline) Execute(_ context.Context, input appactivity.GetTimelineInput) (appactivity.GetTimelineOutput, error) {
	from := domain.Date("2026-08-01")
	to := domain.Date("2026-08-10")
	if input.From != nil {
		from = *input.From
	}
	if input.To != nil {
		to = *input.To
	}
	days := []domain.Day{{Date: from, Count: 1, Level: domain.LevelLow}}
	if to != from {
		days = append(days, domain.Day{Date: to, Count: 10, Level: domain.LevelVeryHigh})
	}
	return appactivity.GetTimelineOutput{
		From: from,
		To:   to,
		Timeline: domain.Timeline{
			Subject:      input.Subject,
			Timezone:     input.Timezone,
			Environments: []domain.Environment{},
			Days:         days,
		},
	}, nil
}

func TestSVGHTTPThemeAndPeriodSnapshots(t *testing.T) {
	t.Parallel()
	router := apihttp.NewRouter(apihttp.Dependencies{Timeline: svgSnapshotTimeline{}})
	tests := []struct {
		name       string
		path       string
		wantDigest string
	}{
		{
			name:       "light one day",
			path:       "/v1/render/snapshot.svg?from=2026-08-01&to=2026-08-01&timezone=UTC&theme=light",
			wantDigest: "3f8a7cdbe0a6ac2d585522cf31d64cbe2c7a37160eaecaf3c3482e8666355811",
		},
		{
			name:       "dark cross week without legend",
			path:       "/v1/render/snapshot.svg?from=2026-08-01&to=2026-08-10&timezone=UTC&theme=dark&weekStart=monday&cellSize=12&showLegend=false",
			wantDigest: "5da8d460037f0a525f59f3639149fc114e1f056e1944d170aa5ba70eca3d235c",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			request.Header.Set("X-Request-ID", "svg-snapshot-request")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)

			if response.Code != http.StatusOK {
				t.Fatalf("status = %d: %s", response.Code, response.Body.String())
			}
			if got := response.Header().Get("Content-Type"); got != "image/svg+xml; charset=utf-8" {
				t.Fatalf("Content-Type = %q", got)
			}
			if got := response.Header().Get("Content-Security-Policy"); got != "default-src 'none'; frame-ancestors 'none'" {
				t.Fatalf("Content-Security-Policy = %q", got)
			}
			if got := response.Header().Get("X-Content-Type-Options"); got != "nosniff" {
				t.Fatalf("X-Content-Type-Options = %q", got)
			}
			if got := response.Header().Get("Cache-Control"); got != "public, max-age=300" {
				t.Fatalf("Cache-Control = %q", got)
			}
			digest := sha256.Sum256(response.Body.Bytes())
			gotDigest := hex.EncodeToString(digest[:])
			if gotDigest != test.wantDigest {
				t.Fatalf("SVG response snapshot digest = %s, want %s\n%s", gotDigest, test.wantDigest, response.Body.Bytes())
			}
			if got := response.Header().Get("ETag"); got != fmt.Sprintf("%q", gotDigest) {
				t.Fatalf("ETag = %q, want quoted body digest", got)
			}
		})
	}
}

func TestSVGHTTPErrorSnapshots(t *testing.T) {
	t.Parallel()
	router := apihttp.NewRouter(apihttp.Dependencies{Timeline: svgSnapshotTimeline{}})
	tests := []struct {
		name string
		path string
		want string
	}{
		{
			name: "invalid from date",
			path: "/v1/render/snapshot.svg?from=2026-02-30&to=2026-08-01",
			want: `{"type":"https://jandibat.org/problems/invalid_request","title":"Bad Request","status":400,"code":"invalid_request","detail":"from: activity application: invalid date","instance":"/v1/render/snapshot.svg","requestId":"svg-error-snapshot"}` + "\n",
		},
		{
			name: "unknown theme",
			path: "/v1/render/snapshot.svg?from=2026-08-01&to=2026-08-01&theme=solarized",
			want: `{"type":"https://jandibat.org/problems/invalid_request","title":"Bad Request","status":400,"code":"invalid_request","detail":"activity render: unknown theme","instance":"/v1/render/snapshot.svg","requestId":"svg-error-snapshot"}` + "\n",
		},
		{
			name: "invalid cell size",
			path: "/v1/render/snapshot.svg?from=2026-08-01&to=2026-08-01&cellSize=33",
			want: `{"type":"https://jandibat.org/problems/invalid_request","title":"Bad Request","status":400,"code":"invalid_request","detail":"invalid request: cellSize must be between 6 and 32","instance":"/v1/render/snapshot.svg","requestId":"svg-error-snapshot"}` + "\n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			request.Header.Set("X-Request-ID", "svg-error-snapshot")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)

			if response.Code != http.StatusBadRequest || response.Body.String() != test.want {
				t.Fatalf("error snapshot = status %d body %q, want status 400 body %q", response.Code, response.Body.String(), test.want)
			}
			if got := response.Header().Get("Content-Type"); got != "application/problem+json" {
				t.Fatalf("Content-Type = %q", got)
			}
			if got := response.Header().Get("Cache-Control"); got != "private, no-store" {
				t.Fatalf("Cache-Control = %q", got)
			}
			for name, want := range map[string]string{
				"Content-Security-Policy": "default-src 'none'; frame-ancestors 'none'",
				"X-Content-Type-Options":  "nosniff",
			} {
				if got := response.Header().Get(name); got != want {
					t.Errorf("%s = %q, want %q", name, got, want)
				}
			}
		})
	}
}

func TestAuthenticationSuccessCookieUsesProductionSecurityAttributes(t *testing.T) {
	t.Parallel()
	router := apihttp.NewRouter(apihttp.Dependencies{Auth: fakeAuth{}, SecureCookies: true})
	request := httptest.NewRequest(http.MethodPost, "/v1/auth/magic-link/consume", strings.NewReader(`{"token":"opaque"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies = %#v, want one session cookie", cookies)
	}
	cookie := cookies[0]
	if cookie.Name != "jandibat_session" || cookie.Value != "issued-token" || cookie.Path != "/" ||
		!cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("session cookie attributes = %#v", cookie)
	}
}
