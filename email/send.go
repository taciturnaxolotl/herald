package email

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/smtp"
	"os"
	"strings"
	"time"

	"github.com/emersion/go-msgauth/dkim"
)

type SMTPConfig struct {
	Host               string
	Port               int
	User               string
	Pass               string
	From               string
	DKIMPrivateKey     string
	DKIMPrivateKeyFile string
	DKIMSelector       string
	DKIMDomain         string
}

type Mailer struct {
	cfg          SMTPConfig
	unsubBaseURL string
	dkimKey      *rsa.PrivateKey
}

func NewMailer(cfg SMTPConfig, unsubBaseURL string) (*Mailer, error) {
	m := &Mailer{
		cfg:          cfg,
		unsubBaseURL: unsubBaseURL,
	}

	// Parse DKIM private key if provided
	var keyData string
	if cfg.DKIMPrivateKey != "" {
		keyData = cfg.DKIMPrivateKey
	} else if cfg.DKIMPrivateKeyFile != "" {
		keyBytes, err := os.ReadFile(cfg.DKIMPrivateKeyFile)
		if err != nil {
			return nil, fmt.Errorf("failed to read DKIM private key file: %w", err)
		}
		keyData = string(keyBytes)
	}

	if keyData != "" && strings.Contains(keyData, "BEGIN") {
		// Replace literal \n with actual newlines (for .env file compatibility)
		keyData = strings.ReplaceAll(keyData, "\\n", "\n")

		block, _ := pem.Decode([]byte(keyData))
		if block == nil {
			return nil, fmt.Errorf("failed to decode DKIM private key PEM")
		}

		key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
		if err != nil {
			// Try PKCS8 format
			keyInterface, err := x509.ParsePKCS8PrivateKey(block.Bytes)
			if err != nil {
				return nil, fmt.Errorf("failed to parse DKIM private key: %w", err)
			}
			var ok bool
			key, ok = keyInterface.(*rsa.PrivateKey)
			if !ok {
				return nil, fmt.Errorf("DKIM private key is not RSA")
			}
		}
		m.dkimKey = key
	}

	return m, nil
}

// ValidateConfig tests SMTP connectivity and auth
func (m *Mailer) ValidateConfig() error {
	addr := net.JoinHostPort(m.cfg.Host, fmt.Sprintf("%d", m.cfg.Port))

	var auth smtp.Auth
	if m.cfg.User != "" && m.cfg.Pass != "" {
		auth = smtp.PlainAuth("", m.cfg.User, m.cfg.Pass, m.cfg.Host)
	}

	// Port 465 uses implicit TLS
	if m.cfg.Port == 465 {
		tlsConfig := &tls.Config{
			ServerName: m.cfg.Host,
			MinVersion: tls.VersionTLS12,
		}

		conn, err := tls.Dial("tcp", addr, tlsConfig)
		if err != nil {
			return fmt.Errorf("TLS dial: %w", err)
		}
		defer func() { _ = conn.Close() }()

		client, err := smtp.NewClient(conn, m.cfg.Host)
		if err != nil {
			return fmt.Errorf("SMTP client: %w", err)
		}
		defer func() { _ = client.Close() }()

		if auth != nil {
			if err = client.Auth(auth); err != nil {
				return fmt.Errorf("auth: %w", err)
			}
		}

		return client.Quit()
	}

	// Port 587 uses STARTTLS
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer func() { _ = conn.Close() }()

	client, err := smtp.NewClient(conn, m.cfg.Host)
	if err != nil {
		return fmt.Errorf("SMTP client: %w", err)
	}
	defer func() { _ = client.Close() }()

	// Start TLS before auth
	tlsConfig := &tls.Config{
		ServerName: m.cfg.Host,
		MinVersion: tls.VersionTLS12,
	}
	if err = client.StartTLS(tlsConfig); err != nil {
		return fmt.Errorf("STARTTLS: %w", err)
	}

	if auth != nil {
		if err = client.Auth(auth); err != nil {
			return fmt.Errorf("auth: %w", err)
		}
	}

	return client.Quit()
}

func (m *Mailer) Send(to, subject, htmlBody, textBody, unsubToken, dashboardURL, keepAliveURL string) error {
	// Add footer with keep-alive, unsubscribe, and dashboard links
	var htmlFooter strings.Builder
	var textFooter strings.Builder

	if keepAliveURL != "" || unsubToken != "" || dashboardURL != "" {
		htmlFooter.WriteString(`<hr><p style="font-size: 12px; color: #666;">`)
		textFooter.WriteString("\n\n---\n")

		if keepAliveURL != "" {
			fmt.Fprintf(&htmlFooter, `<a href="%s">keep this digest active</a>`, keepAliveURL)
			fmt.Fprintf(&textFooter, "keep this digest active: %s\n", keepAliveURL)
		}

		if dashboardURL != "" {
			if keepAliveURL != "" {
				htmlFooter.WriteString(" • ")
			}
			fmt.Fprintf(&htmlFooter, `<a href="%s">profile</a>`, dashboardURL)
			fmt.Fprintf(&textFooter, "profile: %s\n", dashboardURL)
		}

		if unsubToken != "" {
			unsubURL := m.unsubBaseURL + "/unsubscribe/" + unsubToken
			if dashboardURL != "" || keepAliveURL != "" {
				htmlFooter.WriteString(" • ")
			}
			fmt.Fprintf(&htmlFooter, `<a href="%s">unsubscribe</a>`, unsubURL)
			fmt.Fprintf(&textFooter, "unsubscribe: %s\n", unsubURL)
		}

		htmlFooter.WriteString("</p>")
		htmlBody = htmlBody + htmlFooter.String()
		textBody = textBody + textFooter.String()
	}

	// RFC 2369 list headers + bulk hints for the recurring digest.
	extraHeaders := map[string]string{
		"List-Id":      fmt.Sprintf("<herald.%s>", senderDomain(m.cfg.From)),
		"List-Archive": fmt.Sprintf("<%s>", dashboardURL),
		"List-Post":    "NO",
		"Precedence":   "bulk",
		"X-Mailer":     "Herald",
	}
	// RFC 8058 one-click unsubscribe.
	if unsubToken != "" {
		unsubURL := m.unsubBaseURL + "/unsubscribe/" + unsubToken
		extraHeaders["List-Unsubscribe"] = fmt.Sprintf("<%s>", unsubURL)
		extraHeaders["List-Unsubscribe-Post"] = "List-Unsubscribe=One-Click"
	}

	return m.deliver(to, subject, htmlBody, textBody, extraHeaders)
}

// deliver assembles a signed multipart/alternative message and sends it. It is
// the shared path for digests and confirmation emails; callers supply any
// headers beyond the base set.
func (m *Mailer) deliver(to, subject, htmlBody, textBody string, extraHeaders map[string]string) error {
	addr := net.JoinHostPort(m.cfg.Host, fmt.Sprintf("%d", m.cfg.Port))
	boundary := "==herald-" + generateMessageIDToken() + "=="

	headers := map[string]string{
		"From":         m.cfg.From,
		"To":           to,
		"Subject":      mime.QEncoding.Encode("utf-8", subject),
		"MIME-Version": "1.0",
		"Content-Type": fmt.Sprintf("multipart/alternative; boundary=%q", boundary),
		"Date":         time.Now().Format(time.RFC1123Z),
		"Message-ID":   fmt.Sprintf("<%d.%s@%s>", time.Now().Unix(), generateMessageIDToken(), senderDomain(m.cfg.From)),
	}
	for k, v := range extraHeaders {
		headers[k] = v
	}

	var msg strings.Builder
	for k, v := range headers {
		fmt.Fprintf(&msg, "%s: %s\r\n", k, v)
	}
	msg.WriteString("\r\n")

	fmt.Fprintf(&msg, "--%s\r\n", boundary)
	msg.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	msg.WriteString("Content-Transfer-Encoding: quoted-printable\r\n\r\n")
	msg.WriteString(encodeQuotedPrintable(textBody))
	msg.WriteString("\r\n")

	fmt.Fprintf(&msg, "--%s\r\n", boundary)
	msg.WriteString("Content-Type: text/html; charset=utf-8\r\n")
	msg.WriteString("Content-Transfer-Encoding: quoted-printable\r\n\r\n")
	msg.WriteString(encodeQuotedPrintable(htmlBody))
	msg.WriteString("\r\n")

	fmt.Fprintf(&msg, "--%s--\r\n", boundary)

	messageBytes := []byte(msg.String())

	// Sign with DKIM if configured
	if m.dkimKey != nil && m.cfg.DKIMDomain != "" && m.cfg.DKIMSelector != "" {
		signed, err := m.signDKIM(messageBytes)
		if err != nil {
			return fmt.Errorf("DKIM signing: %w", err)
		}
		messageBytes = signed
	}

	var auth smtp.Auth
	if m.cfg.User != "" && m.cfg.Pass != "" {
		auth = smtp.PlainAuth("", m.cfg.User, m.cfg.Pass, m.cfg.Host)
	}

	if m.cfg.Port == 465 {
		return m.sendWithTLS(addr, auth, to, messageBytes)
	}

	return m.sendWithSTARTTLS(addr, auth, to, messageBytes)
}

// senderDomain extracts the domain part from an email address.
// Falls back to the full address if parsing fails.
func senderDomain(from string) string {
	if idx := strings.LastIndex(from, "@"); idx >= 0 {
		return from[idx+1:]
	}
	return from
}

func generateMessageIDToken() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func encodeQuotedPrintable(s string) string {
	var buf strings.Builder
	w := quotedprintable.NewWriter(&buf)
	_, _ = w.Write([]byte(s))
	_ = w.Close()
	return buf.String()
}

func (m *Mailer) sendWithTLS(addr string, auth smtp.Auth, to string, msg []byte) error {
	tlsConfig := &tls.Config{
		ServerName: m.cfg.Host,
		MinVersion: tls.VersionTLS12,
	}

	dialer := &net.Dialer{Timeout: 30 * time.Second}
	conn, err := tls.DialWithDialer(dialer, "tcp", addr, tlsConfig)
	if err != nil {
		return fmt.Errorf("TLS dial: %w", err)
	}
	if err := conn.SetDeadline(time.Now().Add(30 * time.Second)); err != nil {
		_ = conn.Close()
		return fmt.Errorf("set deadline: %w", err)
	}
	defer func() { _ = conn.Close() }()

	client, err := smtp.NewClient(conn, m.cfg.Host)
	if err != nil {
		return fmt.Errorf("SMTP client: %w", err)
	}
	defer func() { _ = client.Close() }()

	if auth != nil {
		if err = client.Auth(auth); err != nil {
			return fmt.Errorf("auth: %w", err)
		}
	}

	if err = client.Mail(m.cfg.From); err != nil {
		return fmt.Errorf("mail from: %w", err)
	}

	if err = client.Rcpt(to); err != nil {
		return fmt.Errorf("rcpt to: %w", err)
	}

	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("data: %w", err)
	}

	if _, err = w.Write(msg); err != nil {
		return fmt.Errorf("write: %w", err)
	}

	if err = w.Close(); err != nil {
		return fmt.Errorf("close data: %w", err)
	}

	return client.Quit()
}

func (m *Mailer) sendWithSTARTTLS(addr string, auth smtp.Auth, to string, msg []byte) error {
	dialer := &net.Dialer{Timeout: 30 * time.Second}
	conn, err := dialer.Dial("tcp", addr)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	if err := conn.SetDeadline(time.Now().Add(30 * time.Second)); err != nil {
		_ = conn.Close()
		return fmt.Errorf("set deadline: %w", err)
	}
	defer func() { _ = conn.Close() }()

	client, err := smtp.NewClient(conn, m.cfg.Host)
	if err != nil {
		return fmt.Errorf("SMTP client: %w", err)
	}
	defer func() { _ = client.Close() }()

	if err = client.StartTLS(&tls.Config{
		ServerName: m.cfg.Host,
		MinVersion: tls.VersionTLS12,
	}); err != nil {
		return fmt.Errorf("STARTTLS: %w", err)
	}

	if auth != nil {
		if err = client.Auth(auth); err != nil {
			return fmt.Errorf("auth: %w", err)
		}
	}

	if err = client.Mail(m.cfg.From); err != nil {
		return fmt.Errorf("mail from: %w", err)
	}

	if err = client.Rcpt(to); err != nil {
		return fmt.Errorf("rcpt to: %w", err)
	}

	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("data: %w", err)
	}

	if _, err = w.Write(msg); err != nil {
		return fmt.Errorf("write: %w", err)
	}

	if err = w.Close(); err != nil {
		return fmt.Errorf("close data: %w", err)
	}

	return client.Quit()
}

func (m *Mailer) signDKIM(message []byte) ([]byte, error) {
	options := &dkim.SignOptions{
		Domain:                 m.cfg.DKIMDomain,
		Selector:               m.cfg.DKIMSelector,
		Signer:                 m.dkimKey,
		HeaderCanonicalization: dkim.CanonicalizationRelaxed,
		BodyCanonicalization:   dkim.CanonicalizationRelaxed,
		HeaderKeys: []string{
			"From",
			"To",
			"Subject",
			"Date",
			"Message-ID",
			"MIME-Version",
			"Content-Type",
			"List-Id",
			"List-Unsubscribe",
			"List-Unsubscribe-Post",
			"List-Post",
			"Precedence",
		},
		Expiration: time.Now().Add(72 * time.Hour),
	}

	var b bytes.Buffer
	if err := dkim.Sign(&b, bytes.NewReader(message), options); err != nil {
		return nil, err
	}

	return b.Bytes(), nil
}
