// Package authadapter connects authentication ports to external services.
package authadapter

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	htmltemplate "html/template"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"net/url"
	"strings"
	texttemplate "text/template"
	"time"

	coreauth "github.com/moreal/jandibat.org/apps/api/internal/auth"
)

const (
	defaultSMTPTimeout = 15 * time.Second

	defaultSubject  = "Sign in to jandibat.org"
	defaultTextBody = `Sign in to jandibat.org

Open this single-use link to continue:
{{.MagicLink}}

This link expires at {{.ExpiresAt}}.
If you did not request this email, you can ignore it.
`
	defaultHTMLBody = `<!doctype html>
<html lang="en">
  <body>
    <p>Sign in to jandibat.org</p>
    <p><a href="{{.MagicLink}}">Continue signing in</a></p>
    <p>This single-use link expires at {{.ExpiresAt}}.</p>
    <p>If you did not request this email, you can ignore it.</p>
  </body>
</html>
`
)

var (
	ErrInvalidSMTPConfig    = errors.New("auth smtp: invalid configuration")
	ErrInvalidMagicLinkMail = errors.New("auth smtp: invalid magic-link mail")
	ErrSTARTTLSUnavailable  = errors.New("auth smtp: server does not advertise STARTTLS")
)

// TLSMode controls how the SMTP connection is protected. The zero value is
// STARTTLS so accidentally omitting the setting does not send mail or SMTP
// credentials over a plaintext connection.
type TLSMode string

const (
	TLSModeSTARTTLS TLSMode = "starttls"
	TLSModeImplicit TLSMode = "implicit"
	// TLSModeDisabled exists for local mail catchers. NewSMTPMailer rejects SMTP
	// credentials in this mode.
	TLSModeDisabled TLSMode = "disabled"
)

// MagicLinkTemplates are parsed once by NewSMTPMailer. MagicLink and ExpiresAt
// are the only template fields. Leaving a field blank selects the safe default.
type MagicLinkTemplates struct {
	Subject string
	Text    string
	HTML    string
}

// SMTPConfig contains only adapter-specific settings. MagicLinkURL is the
// default public web URL that accepts a token. AllowedRedirectURIs are exact,
// trusted alternatives; request Host headers are never used to construct
// authentication links.
type SMTPConfig struct {
	Address             string
	Username            string
	Password            string
	From                string
	MagicLinkURL        *url.URL
	AllowedRedirectURIs []string
	TLSMode             TLSMode
	TLSConfig           *tls.Config
	Timeout             time.Duration
	Templates           MagicLinkTemplates
}

// SMTPMailer sends multipart text/HTML magic-link email through net/smtp.
// It never logs or includes the raw bearer token in returned errors.
type SMTPMailer struct {
	address             string
	host                string
	username            string
	password            string
	from                mail.Address
	magicLinkURL        url.URL
	allowedRedirectURLs map[string]url.URL
	tlsMode             TLSMode
	tlsConfig           *tls.Config
	timeout             time.Duration
	subject             string
	textTemplate        *texttemplate.Template
	htmlTemplate        *htmltemplate.Template
	dialContext         func(context.Context, string, string) (net.Conn, error)
}

type magicLinkTemplateData struct {
	MagicLink string
	ExpiresAt string
}

var _ coreauth.Mailer = (*SMTPMailer)(nil)

func NewSMTPMailer(config SMTPConfig) (*SMTPMailer, error) {
	address := strings.TrimSpace(config.Address)
	host, port, err := net.SplitHostPort(address)
	if err != nil || strings.TrimSpace(host) == "" || strings.TrimSpace(port) == "" {
		return nil, fmt.Errorf("%w: Address must be host:port", ErrInvalidSMTPConfig)
	}

	from, err := mail.ParseAddress(strings.TrimSpace(config.From))
	if err != nil || from.Address == "" {
		return nil, fmt.Errorf("%w: From must be one mailbox", ErrInvalidSMTPConfig)
	}
	if config.MagicLinkURL == nil || !validMagicLinkURL(config.MagicLinkURL) {
		return nil, fmt.Errorf("%w: MagicLinkURL must be an absolute HTTPS URL (HTTP is allowed only for loopback development)", ErrInvalidSMTPConfig)
	}
	allowedRedirectURLs := make(map[string]url.URL, len(config.AllowedRedirectURIs)+1)
	allowedRedirectURLs[config.MagicLinkURL.String()] = *config.MagicLinkURL
	for _, raw := range config.AllowedRedirectURIs {
		candidate, parseErr := url.Parse(raw)
		if parseErr != nil || !validMagicLinkURL(candidate) {
			return nil, fmt.Errorf("%w: AllowedRedirectURIs must contain only absolute HTTPS URLs (HTTP is allowed only for loopback development)", ErrInvalidSMTPConfig)
		}
		allowedRedirectURLs[raw] = *candidate
	}

	username := strings.TrimSpace(config.Username)
	if (username == "") != (config.Password == "") {
		return nil, fmt.Errorf("%w: Username and Password must be configured together", ErrInvalidSMTPConfig)
	}

	tlsMode := config.TLSMode
	if tlsMode == "" {
		tlsMode = TLSModeSTARTTLS
	}
	if tlsMode != TLSModeSTARTTLS && tlsMode != TLSModeImplicit && tlsMode != TLSModeDisabled {
		return nil, fmt.Errorf("%w: unsupported TLSMode %q", ErrInvalidSMTPConfig, tlsMode)
	}
	if tlsMode == TLSModeDisabled && username != "" {
		return nil, fmt.Errorf("%w: SMTP credentials require TLS", ErrInvalidSMTPConfig)
	}

	timeout := config.Timeout
	if timeout == 0 {
		timeout = defaultSMTPTimeout
	}
	if timeout < 0 {
		return nil, fmt.Errorf("%w: Timeout must be positive", ErrInvalidSMTPConfig)
	}

	tlsConfig := cloneTLSConfig(config.TLSConfig, host)
	if tlsConfig.MinVersion < tls.VersionTLS12 {
		return nil, fmt.Errorf("%w: TLS 1.2 or newer is required", ErrInvalidSMTPConfig)
	}

	templates := config.Templates
	if templates.Subject == "" {
		templates.Subject = defaultSubject
	}
	if strings.ContainsAny(templates.Subject, "\r\n") {
		return nil, fmt.Errorf("%w: template Subject must not contain newlines", ErrInvalidSMTPConfig)
	}
	if templates.Text == "" {
		templates.Text = defaultTextBody
	}
	if templates.HTML == "" {
		templates.HTML = defaultHTMLBody
	}
	textBody, err := texttemplate.New("magic-link-text").Option("missingkey=error").Parse(templates.Text)
	if err != nil {
		return nil, fmt.Errorf("%w: parse text template: %v", ErrInvalidSMTPConfig, err)
	}
	htmlBody, err := htmltemplate.New("magic-link-html").Option("missingkey=error").Parse(templates.HTML)
	if err != nil {
		return nil, fmt.Errorf("%w: parse HTML template: %v", ErrInvalidSMTPConfig, err)
	}

	magicLinkURL := *config.MagicLinkURL
	dialer := &net.Dialer{Timeout: timeout}
	return &SMTPMailer{
		address:             address,
		host:                host,
		username:            username,
		password:            config.Password,
		from:                *from,
		magicLinkURL:        magicLinkURL,
		allowedRedirectURLs: allowedRedirectURLs,
		tlsMode:             tlsMode,
		tlsConfig:           tlsConfig,
		timeout:             timeout,
		subject:             templates.Subject,
		textTemplate:        textBody,
		htmlTemplate:        htmlBody,
		dialContext:         dialer.DialContext,
	}, nil
}

func (m *SMTPMailer) SendMagicLink(ctx context.Context, message coreauth.MagicLinkMail) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	recipient, err := mail.ParseAddress(strings.TrimSpace(message.Email))
	if err != nil || recipient.Address == "" || strings.TrimSpace(message.Token) == "" || message.ExpiresAt.IsZero() {
		return ErrInvalidMagicLinkMail
	}

	payload, err := m.renderMessage(*recipient, message.Token, message.ExpiresAt, message.RedirectURI)
	if err != nil {
		return fmt.Errorf("auth smtp: render message: %w", err)
	}
	if err := m.deliver(ctx, recipient.Address, payload); err != nil {
		return fmt.Errorf("auth smtp: deliver message: %w", err)
	}
	return nil
}

func (m *SMTPMailer) renderMessage(recipient mail.Address, token string, expiresAt time.Time, redirectURI string) ([]byte, error) {
	link := m.magicLinkURL
	if redirectURI != "" {
		allowed, ok := m.allowedRedirectURLs[redirectURI]
		if !ok {
			return nil, ErrInvalidMagicLinkMail
		}
		link = allowed
	}
	magicLink, err := magicLinkWithFragmentToken(link, token)
	if err != nil {
		return nil, ErrInvalidMagicLinkMail
	}
	data := magicLinkTemplateData{
		MagicLink: magicLink,
		ExpiresAt: expiresAt.UTC().Format(time.RFC3339),
	}

	var textBody bytes.Buffer
	if err := m.textTemplate.Execute(&textBody, data); err != nil {
		return nil, errors.New("execute text template")
	}
	var htmlBody bytes.Buffer
	if err := m.htmlTemplate.Execute(&htmlBody, data); err != nil {
		return nil, errors.New("execute HTML template")
	}

	var result bytes.Buffer
	multipartWriter := multipart.NewWriter(&result)
	writeHeader(&result, "From", m.from.String())
	writeHeader(&result, "To", recipient.String())
	writeHeader(&result, "Subject", mime.QEncoding.Encode("utf-8", m.subject))
	writeHeader(&result, "MIME-Version", "1.0")
	writeHeader(&result, "Content-Type", `multipart/alternative; boundary="`+multipartWriter.Boundary()+`"`)
	result.WriteString("\r\n")

	if err := writeQuotedPrintablePart(multipartWriter, "text/plain; charset=utf-8", textBody.Bytes()); err != nil {
		return nil, errors.New("encode text body")
	}
	if err := writeQuotedPrintablePart(multipartWriter, "text/html; charset=utf-8", htmlBody.Bytes()); err != nil {
		return nil, errors.New("encode HTML body")
	}
	if err := multipartWriter.Close(); err != nil {
		return nil, errors.New("finish MIME message")
	}
	return result.Bytes(), nil
}

func magicLinkWithFragmentToken(link url.URL, token string) (string, error) {
	fragmentPath, rawFragmentQuery, _ := strings.Cut(link.EscapedFragment(), "?")
	fragmentQuery, err := url.ParseQuery(rawFragmentQuery)
	if err != nil {
		return "", errors.New("invalid fragment query")
	}
	fragmentQuery.Set("token", token)

	// Build the fragment separately so the bearer token can never be
	// serialized into the request URI sent to the web server.
	link.Fragment = ""
	link.RawFragment = ""
	return link.String() + "#" + fragmentPath + "?" + fragmentQuery.Encode(), nil
}

func (m *SMTPMailer) deliver(ctx context.Context, recipient string, payload []byte) error {
	connection, err := m.dialContext(ctx, "tcp", m.address)
	if err != nil {
		return errors.New("connect to server")
	}
	defer connection.Close()

	deadline := time.Now().Add(m.timeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	if err := connection.SetDeadline(deadline); err != nil {
		return errors.New("set connection deadline")
	}
	stopCancellation := context.AfterFunc(ctx, func() {
		_ = connection.SetDeadline(time.Now())
	})
	defer stopCancellation()

	if m.tlsMode == TLSModeImplicit {
		tlsConnection := tls.Client(connection, m.tlsConfig.Clone())
		if err := tlsConnection.HandshakeContext(ctx); err != nil {
			return errors.New("establish implicit TLS")
		}
		connection = tlsConnection
	}

	client, err := smtp.NewClient(connection, m.host)
	if err != nil {
		return errors.New("start SMTP client")
	}
	defer client.Close()

	if m.tlsMode == TLSModeSTARTTLS {
		if supported, _ := client.Extension("STARTTLS"); !supported {
			return ErrSTARTTLSUnavailable
		}
		if err := client.StartTLS(m.tlsConfig.Clone()); err != nil {
			return errors.New("establish STARTTLS")
		}
	}
	if m.username != "" {
		if err := client.Auth(smtp.PlainAuth("", m.username, m.password, m.host)); err != nil {
			return errors.New("authenticate")
		}
	}
	if err := client.Mail(m.from.Address); err != nil {
		return errors.New("set envelope sender")
	}
	if err := client.Rcpt(recipient); err != nil {
		return errors.New("set envelope recipient")
	}
	dataWriter, err := client.Data()
	if err != nil {
		return errors.New("start message data")
	}
	if _, err := io.Copy(dataWriter, bytes.NewReader(payload)); err != nil {
		_ = dataWriter.Close()
		return errors.New("write message data")
	}
	if err := dataWriter.Close(); err != nil {
		return errors.New("finish message data")
	}
	if err := client.Quit(); err != nil {
		return errors.New("finish SMTP session")
	}
	return nil
}

func cloneTLSConfig(source *tls.Config, host string) *tls.Config {
	var cloned *tls.Config
	if source == nil {
		cloned = &tls.Config{}
	} else {
		cloned = source.Clone()
	}
	if cloned.ServerName == "" {
		cloned.ServerName = host
	}
	if cloned.MinVersion == 0 {
		cloned.MinVersion = tls.VersionTLS12
	}
	return cloned
}

func validMagicLinkURL(candidate *url.URL) bool {
	if candidate == nil || candidate.IsAbs() == false || candidate.Host == "" || candidate.User != nil {
		return false
	}
	if candidate.Scheme == "https" {
		return true
	}
	if candidate.Scheme != "http" {
		return false
	}
	host := candidate.Hostname()
	if strings.EqualFold(host, "localhost") {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}

func writeHeader(target *bytes.Buffer, name, value string) {
	target.WriteString(name)
	target.WriteString(": ")
	target.WriteString(value)
	target.WriteString("\r\n")
}

func writeQuotedPrintablePart(writer *multipart.Writer, contentType string, body []byte) error {
	header := make(textproto.MIMEHeader)
	header.Set("Content-Type", contentType)
	header.Set("Content-Transfer-Encoding", "quoted-printable")
	part, err := writer.CreatePart(header)
	if err != nil {
		return err
	}
	encoder := quotedprintable.NewWriter(part)
	if _, err := encoder.Write(body); err != nil {
		return err
	}
	return encoder.Close()
}
