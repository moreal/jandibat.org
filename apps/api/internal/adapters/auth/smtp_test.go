package authadapter

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/textproto"
	"net/url"
	"strings"
	"testing"
	"time"

	coreauth "github.com/moreal/jandibat.org/apps/api/internal/auth"
)

func TestNewSMTPMailerValidatesSecuritySensitiveConfiguration(t *testing.T) {
	t.Parallel()
	validURL := mustURL(t, "https://jandibat.org/auth/magic-link")
	tests := []struct {
		name   string
		mutate func(*SMTPConfig)
	}{
		{name: "address", mutate: func(config *SMTPConfig) { config.Address = "smtp.example.com" }},
		{name: "from", mutate: func(config *SMTPConfig) { config.From = "not-an-address" }},
		{name: "link scheme", mutate: func(config *SMTPConfig) { config.MagicLinkURL = mustURL(t, "http://jandibat.org/sign-in") }},
		{name: "link credentials", mutate: func(config *SMTPConfig) { config.MagicLinkURL = mustURL(t, "https://user:pass@jandibat.org/sign-in") }},
		{name: "allowed redirect credentials", mutate: func(config *SMTPConfig) {
			config.AllowedRedirectURIs = []string{"https://user:pass@jandibat.org/return"}
		}},
		{name: "partial credentials", mutate: func(config *SMTPConfig) { config.Username = "mailer" }},
		{name: "plaintext credentials", mutate: func(config *SMTPConfig) {
			config.TLSMode = TLSModeDisabled
			config.Username = "mailer"
			config.Password = "password"
		}},
		{name: "old tls", mutate: func(config *SMTPConfig) {
			config.TLSConfig = testTLSConfig(tlsVersion11)
		}},
		{name: "subject injection", mutate: func(config *SMTPConfig) { config.Templates.Subject = "hello\r\nBcc: target@example.com" }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			config := SMTPConfig{
				Address:      "smtp.example.com:587",
				From:         "jandibat.org <signin@jandibat.org>",
				MagicLinkURL: validURL,
			}
			test.mutate(&config)
			if _, err := NewSMTPMailer(config); !errors.Is(err, ErrInvalidSMTPConfig) {
				t.Fatalf("NewSMTPMailer() error = %v, want ErrInvalidSMTPConfig", err)
			}
		})
	}
}

// Keep this test independent from crypto/tls constants so the test table above
// remains readable next to the adapter's minimum-version policy.
const tlsVersion11 = 0x0302

func testTLSConfig(minVersion uint16) *tls.Config {
	return &tls.Config{MinVersion: minVersion}
}

func TestSMTPMailerRendersFixedOriginMultipartMessage(t *testing.T) {
	t.Parallel()
	mailer := newTestMailer(t, "127.0.0.1:2525", TLSModeDisabled)
	token := "secret +/&?=value"
	expiresAt := time.Date(2026, 8, 12, 12, 30, 0, 0, time.FixedZone("KST", 9*60*60))

	payload, err := mailer.renderMessage(mail.Address{Name: "Person", Address: "person@example.com"}, token, expiresAt, "")
	if err != nil {
		t.Fatalf("renderMessage() error = %v", err)
	}
	message, err := mail.ReadMessage(bufio.NewReader(strings.NewReader(string(payload))))
	if err != nil {
		t.Fatalf("mail.ReadMessage() error = %v", err)
	}
	if got := message.Header.Get("To"); got != `"Person" <person@example.com>` {
		t.Fatalf("To = %q", got)
	}
	mediaType, parameters, err := mime.ParseMediaType(message.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/alternative" {
		t.Fatalf("Content-Type = %q, error = %v", mediaType, err)
	}

	parts := multipart.NewReader(message.Body, parameters["boundary"])
	bodies := make(map[string]string)
	for {
		part, nextErr := parts.NextPart()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			t.Fatalf("NextPart() error = %v", nextErr)
		}
		decoded, readErr := io.ReadAll(quotedprintable.NewReader(part))
		if readErr != nil {
			t.Fatalf("read MIME part: %v", readErr)
		}
		partType, _, _ := mime.ParseMediaType(part.Header.Get("Content-Type"))
		bodies[partType] = string(decoded)
	}

	for _, contentType := range []string{"text/plain", "text/html"} {
		body := bodies[contentType]
		if body == "" {
			t.Fatalf("missing %s body", contentType)
		}
		if !strings.Contains(body, "2026-08-12T03:30:00Z") {
			t.Fatalf("%s does not contain UTC expiry: %q", contentType, body)
		}
	}
	var textLink string
	for _, field := range strings.Fields(bodies["text/plain"]) {
		if strings.HasPrefix(field, "https://") {
			textLink = field
			break
		}
	}
	if textLink == "" {
		t.Fatalf("text body does not contain an HTTPS link: %q", bodies["text/plain"])
	}
	parsedLink, err := url.Parse(textLink)
	if err != nil {
		t.Fatalf("parse rendered link: %v", err)
	}
	if parsedLink.Scheme != "https" || parsedLink.Host != "jandibat.org" || parsedLink.Path != "/sign-in" {
		t.Fatalf("rendered link = %s", parsedLink.Redacted())
	}
	if got := parsedLink.Query().Get("locale"); got != "ko" {
		t.Fatalf("locale query = %q", got)
	}
	if got := parsedLink.Query().Get("token"); got != "" {
		t.Fatalf("token leaked into URL query: %q", got)
	}
	if got := fragmentQuery(t, parsedLink).Get("token"); got != token {
		t.Fatalf("fragment token = %q, want original token", got)
	}
}

func TestSMTPMailerUsesOnlyExactlyAllowedRedirectURI(t *testing.T) {
	t.Parallel()
	redirectURI := "https://app.example/base?auth=magic#auth"
	mailer, err := NewSMTPMailer(SMTPConfig{
		Address:             "127.0.0.1:2525",
		From:                "jandibat.org <signin@jandibat.org>",
		MagicLinkURL:        mustURL(t, "https://app.example/base#auth"),
		AllowedRedirectURIs: []string{redirectURI},
		TLSMode:             TLSModeDisabled,
	})
	if err != nil {
		t.Fatalf("NewSMTPMailer() error = %v", err)
	}

	token := "secret +/&?=value"
	payload, err := mailer.renderMessage(
		mail.Address{Address: "person@example.com"}, token,
		time.Date(2026, 8, 12, 3, 30, 0, 0, time.UTC), redirectURI,
	)
	if err != nil {
		t.Fatalf("renderMessage() error = %v", err)
	}
	parsedLink := renderedTextLink(t, payload)
	if got := parsedLink.String(); got != "https://app.example/base?auth=magic#auth?token=secret+%2B%2F%26%3F%3Dvalue" {
		t.Fatalf("rendered link = %q", got)
	}
	if got := parsedLink.Query().Get("token"); got != "" {
		t.Fatalf("token leaked into URL query: %q", got)
	}
	if got := fragmentQuery(t, parsedLink).Get("token"); got != token {
		t.Fatalf("fragment token = %q, want original token", got)
	}

	err = mailer.SendMagicLink(context.Background(), coreauth.MagicLinkMail{
		Email:       "person@example.com",
		Token:       "must-not-leak",
		ExpiresAt:   time.Now().Add(10 * time.Minute),
		RedirectURI: "https://app.example.evil/base?auth=magic#auth",
	})
	if !errors.Is(err, ErrInvalidMagicLinkMail) {
		t.Fatalf("unallowed redirect error = %v, want ErrInvalidMagicLinkMail", err)
	}
	if strings.Contains(err.Error(), "must-not-leak") || strings.Contains(err.Error(), "app.example.evil") {
		t.Fatalf("unallowed redirect error disclosed sensitive input: %v", err)
	}
}

func TestMagicLinkFragmentPreservesRouteQueryAndRejectsMalformedQuery(t *testing.T) {
	t.Parallel()
	link := mustURL(t, "https://app.example/base?auth=magic#auth?next=connections&token=stale")
	rendered, err := magicLinkWithFragmentToken(*link, "new + secret")
	if err != nil {
		t.Fatalf("magicLinkWithFragmentToken() error = %v", err)
	}
	parsed := mustURL(t, rendered)
	if parsed.Query().Get("auth") != "magic" || parsed.Query().Get("token") != "" {
		t.Fatalf("ordinary query changed or contains token: %q", parsed.RawQuery)
	}
	fragment := fragmentQuery(t, parsed)
	if fragment.Get("next") != "connections" || fragment.Get("token") != "new + secret" {
		t.Fatalf("fragment query = %#v", fragment)
	}

	malformed := mustURL(t, "https://app.example/base#auth?bad=a;b")
	if _, err := magicLinkWithFragmentToken(*malformed, "must-not-leak"); err == nil {
		t.Fatal("malformed fragment query was accepted")
	} else if strings.Contains(err.Error(), "must-not-leak") {
		t.Fatalf("fragment error disclosed token: %v", err)
	}
}

func fragmentQuery(t *testing.T, link *url.URL) url.Values {
	t.Helper()
	_, rawQuery, found := strings.Cut(link.EscapedFragment(), "?")
	if !found {
		t.Fatalf("link fragment has no query: %q", link.Fragment)
	}
	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		t.Fatalf("parse link fragment query: %v", err)
	}
	return values
}

func renderedTextLink(t *testing.T, payload []byte) *url.URL {
	t.Helper()
	message, err := mail.ReadMessage(bufio.NewReader(bytes.NewReader(payload)))
	if err != nil {
		t.Fatalf("mail.ReadMessage() error = %v", err)
	}
	_, parameters, err := mime.ParseMediaType(message.Header.Get("Content-Type"))
	if err != nil {
		t.Fatalf("parse message content type: %v", err)
	}
	parts := multipart.NewReader(message.Body, parameters["boundary"])
	for {
		part, nextErr := parts.NextPart()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			t.Fatalf("NextPart() error = %v", nextErr)
		}
		partType, _, _ := mime.ParseMediaType(part.Header.Get("Content-Type"))
		if partType != "text/plain" {
			continue
		}
		decoded, readErr := io.ReadAll(quotedprintable.NewReader(part))
		if readErr != nil {
			t.Fatalf("read text body: %v", readErr)
		}
		for _, field := range strings.Fields(string(decoded)) {
			if strings.HasPrefix(field, "https://") || strings.HasPrefix(field, "http://") {
				parsed, parseErr := url.Parse(field)
				if parseErr != nil {
					t.Fatalf("parse rendered link: %v", parseErr)
				}
				return parsed
			}
		}
	}
	t.Fatal("text body does not contain a link")
	return nil
}

func TestSMTPMailerSendsThroughExplicitPlaintextDevelopmentServer(t *testing.T) {
	t.Parallel()
	connection, server := startTestSMTPServer(t, false)
	mailer := newPipeMailer(t, connection, TLSModeDisabled)
	message := coreauth.MagicLinkMail{
		Email:     "person@example.com",
		Token:     "do-not-log-this-token",
		ExpiresAt: time.Now().Add(10 * time.Minute),
	}
	if err := mailer.SendMagicLink(context.Background(), message); err != nil {
		t.Fatalf("SendMagicLink() error = %v", err)
	}
	delivery := server.wait(t)
	if !strings.HasPrefix(delivery.from, "MAIL FROM:<signin@jandibat.org>") {
		t.Fatalf("MAIL FROM = %q", delivery.from)
	}
	if delivery.recipient != "RCPT TO:<person@example.com>" {
		t.Fatalf("RCPT TO = %q", delivery.recipient)
	}
	if !strings.Contains(delivery.payload, "multipart/alternative") {
		t.Fatal("SMTP DATA did not contain MIME message")
	}
}

func TestSMTPMailerRequiresSTARTTLSByDefault(t *testing.T) {
	t.Parallel()
	connection, server := startTestSMTPServer(t, false)
	mailer := newPipeMailer(t, connection, "")
	err := mailer.SendMagicLink(context.Background(), coreauth.MagicLinkMail{
		Email:     "person@example.com",
		Token:     "never-sent-token",
		ExpiresAt: time.Now().Add(10 * time.Minute),
	})
	if !errors.Is(err, ErrSTARTTLSUnavailable) {
		t.Fatalf("SendMagicLink() error = %v, want ErrSTARTTLSUnavailable", err)
	}
	if strings.Contains(err.Error(), "never-sent-token") {
		t.Fatal("delivery error disclosed the magic-link token")
	}
	server.wait(t)
}

func TestSMTPMailerRejectsInvalidMessageWithoutNetworkAccess(t *testing.T) {
	t.Parallel()
	mailer := newTestMailer(t, "127.0.0.1:1", TLSModeDisabled)
	tests := []coreauth.MagicLinkMail{
		{Email: "bad address", Token: "token", ExpiresAt: time.Now()},
		{Email: "person@example.com", ExpiresAt: time.Now()},
		{Email: "person@example.com", Token: "token"},
	}
	for _, message := range tests {
		if err := mailer.SendMagicLink(context.Background(), message); !errors.Is(err, ErrInvalidMagicLinkMail) {
			t.Fatalf("SendMagicLink(%+v) error = %v", message, err)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := mailer.SendMagicLink(ctx, coreauth.MagicLinkMail{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled SendMagicLink() error = %v", err)
	}
}

func newTestMailer(t *testing.T, address string, mode TLSMode) *SMTPMailer {
	t.Helper()
	mailer, err := NewSMTPMailer(SMTPConfig{
		Address:      address,
		From:         "jandibat.org <signin@jandibat.org>",
		MagicLinkURL: mustURL(t, "https://jandibat.org/sign-in?locale=ko"),
		TLSMode:      mode,
		Timeout:      2 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewSMTPMailer() error = %v", err)
	}
	return mailer
}

func newPipeMailer(t *testing.T, connection net.Conn, mode TLSMode) *SMTPMailer {
	t.Helper()
	mailer := newTestMailer(t, "smtp.test:2525", mode)
	mailer.dialContext = func(context.Context, string, string) (net.Conn, error) {
		return connection, nil
	}
	return mailer
}

func mustURL(t *testing.T, value string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(value)
	if err != nil {
		t.Fatalf("url.Parse(%q): %v", value, err)
	}
	return parsed
}

type smtpDelivery struct {
	from      string
	recipient string
	payload   string
	err       error
}

type testSMTPServer struct {
	result <-chan smtpDelivery
}

func startTestSMTPServer(t *testing.T, advertiseSTARTTLS bool) (net.Conn, testSMTPServer) {
	t.Helper()
	clientConnection, serverConnection := net.Pipe()
	t.Cleanup(func() { _ = clientConnection.Close() })
	result := make(chan smtpDelivery, 1)
	go func() {
		defer serverConnection.Close()
		protocol := textproto.NewConn(serverConnection)
		defer protocol.Close()
		if writeErr := protocol.PrintfLine("220 localhost ESMTP test server"); writeErr != nil {
			result <- smtpDelivery{err: writeErr}
			return
		}
		delivery := smtpDelivery{}
		for {
			line, readErr := protocol.ReadLine()
			if readErr != nil {
				if errors.Is(readErr, io.EOF) && delivery.err == nil {
					result <- delivery
				} else {
					result <- smtpDelivery{err: readErr}
				}
				return
			}
			upper := strings.ToUpper(line)
			switch {
			case strings.HasPrefix(upper, "EHLO"):
				_ = protocol.PrintfLine("250-localhost")
				if advertiseSTARTTLS {
					_ = protocol.PrintfLine("250-STARTTLS")
				}
				_ = protocol.PrintfLine("250 8BITMIME")
			case strings.HasPrefix(upper, "HELO"):
				_ = protocol.PrintfLine("250 localhost")
			case strings.HasPrefix(upper, "MAIL FROM"):
				delivery.from = line
				_ = protocol.PrintfLine("250 sender accepted")
			case strings.HasPrefix(upper, "RCPT TO"):
				delivery.recipient = line
				_ = protocol.PrintfLine("250 recipient accepted")
			case upper == "DATA":
				_ = protocol.PrintfLine("354 send message")
				payload, dataErr := io.ReadAll(protocol.DotReader())
				if dataErr != nil {
					result <- smtpDelivery{err: dataErr}
					return
				}
				delivery.payload = string(payload)
				_ = protocol.PrintfLine("250 queued")
			case upper == "QUIT":
				_ = protocol.PrintfLine("221 bye")
				result <- delivery
				return
			default:
				_ = protocol.PrintfLine("500 unsupported command")
			}
		}
	}()
	return clientConnection, testSMTPServer{result: result}
}

func (server testSMTPServer) wait(t *testing.T) smtpDelivery {
	t.Helper()
	select {
	case delivery := <-server.result:
		if delivery.err != nil {
			t.Fatalf("SMTP server error: %v", delivery.err)
		}
		return delivery
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for SMTP server")
		return smtpDelivery{}
	}
}
