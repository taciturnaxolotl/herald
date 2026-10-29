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
	"net/mail"
	"net/smtp"
	"os"
	"sort"
	"strconv"
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

const smtpTimeout = 30 * time.Second

type Mailer struct {
	cfg     SMTPConfig
	dkimKey *rsa.PrivateKey
	timeout time.Duration
}

func NewMailer(cfg SMTPConfig) (*Mailer, error) {
	m := &Mailer{cfg: cfg, timeout: smtpTimeout}

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

// ValidateConfig tests SMTP connectivity and auth.
func (m *Mailer) ValidateConfig() error {
	client, err := m.connect()
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()
	return client.Quit()
}

// connect dials the server (implicit TLS on 465, STARTTLS otherwise) and
// authenticates. The whole session shares one deadline, so a server that
// accepts and then goes silent cannot hang a send or startup.
func (m *Mailer) connect() (*smtp.Client, error) {
	addr := net.JoinHostPort(m.cfg.Host, strconv.Itoa(m.cfg.Port))
	tlsConfig := &tls.Config{ServerName: m.cfg.Host, MinVersion: tls.VersionTLS12}
	dialer := &net.Dialer{Timeout: m.timeout}

	var conn net.Conn
	var err error
	if m.cfg.Port == 465 {
		conn, err = tls.DialWithDialer(dialer, "tcp", addr, tlsConfig)
	} else {
		conn, err = dialer.Dial("tcp", addr)
	}
	if err != nil {
		return nil, fmt.Errorf("dial: %w", err)
	}
	if err := conn.SetDeadline(time.Now().Add(m.timeout)); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("set deadline: %w", err)
	}

	client, err := smtp.NewClient(conn, m.cfg.Host)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("SMTP client: %w", err)
	}
	if m.cfg.Port != 465 {
		if err := client.StartTLS(tlsConfig); err != nil {
			_ = client.Close()
			return nil, fmt.Errorf("STARTTLS: %w", err)
		}
	}
	if m.cfg.User != "" && m.cfg.Pass != "" {
		if err := client.Auth(smtp.PlainAuth("", m.cfg.User, m.cfg.Pass, m.cfg.Host)); err != nil {
			_ = client.Close()
			return nil, fmt.Errorf("auth: %w", err)
		}
	}
	return client, nil
}

// transmit delivers one already-built message.
func (m *Mailer) transmit(to string, msg []byte) error {
	client, err := m.connect()
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()

	if err := client.Mail(m.fromAddress()); err != nil {
		return fmt.Errorf("mail from: %w", err)
	}
	if err := client.Rcpt(to); err != nil {
		return fmt.Errorf("rcpt to: %w", err)
	}
	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("data: %w", err)
	}
	if _, err := w.Write(msg); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("close data: %w", err)
	}
	return client.Quit()
}

// Send delivers a rendered digest; its footer links are already in the bodies,
// and they also drive the RFC 2369 list headers.
func (m *Mailer) Send(to, subject, htmlBody, textBody string, links DigestLinks) error {
	from := m.fromAddress()
	extraHeaders := map[string]string{
		"Auto-Submitted":   "auto-generated",
		"List-Id":          fmt.Sprintf("<herald.%s>", senderDomain(from)),
		"List-Post":        "NO",
		"Precedence":       "bulk",
		"Reply-To":         from,
		"X-MC-MailingList": from,
	}
	if links.Dashboard != "" {
		extraHeaders["List-Archive"] = fmt.Sprintf("<%s>", links.Dashboard)
	}
	// RFC 8058 one-click unsubscribe.
	if links.Unsubscribe != "" {
		extraHeaders["List-Unsubscribe"] = fmt.Sprintf("<%s>", links.Unsubscribe)
		extraHeaders["List-Unsubscribe-Post"] = "List-Unsubscribe=One-Click"
	}

	return m.deliver(to, subject, htmlBody, textBody, extraHeaders)
}

// deliver assembles a signed multipart/alternative message and sends it. It is
// the shared path for digests and confirmation emails; callers supply any
// headers beyond the base set.
func (m *Mailer) deliver(to, subject, htmlBody, textBody string, extraHeaders map[string]string) error {
	boundary := "==herald-" + generateMessageIDToken() + "=="

	// Fixed-order base headers for reproducible output. Real MUAs emit a
	// stable conventional order; per-message shuffling is an anomaly that
	// content filters notice.
	type header struct{ key, value string }
	base := []header{
		{"From", m.fromHeader()},
		{"To", to},
		{"Subject", mime.QEncoding.Encode("utf-8", subject)},
		{"Date", time.Now().Format(time.RFC1123Z)},
		{"Message-ID", fmt.Sprintf("<%d.%s@%s>", time.Now().Unix(), generateMessageIDToken(), senderDomain(m.fromAddress()))},
		{"MIME-Version", "1.0"},
		{"Content-Type", fmt.Sprintf("multipart/alternative; boundary=%q", boundary)},
	}

	var msg strings.Builder
	for _, h := range base {
		fmt.Fprintf(&msg, "%s: %s\r\n", h.key, h.value)
	}

	// Extra headers in sorted order for determinism.
	extraKeys := make([]string, 0, len(extraHeaders))
	for k := range extraHeaders {
		extraKeys = append(extraKeys, k)
	}
	sort.Strings(extraKeys)
	for _, k := range extraKeys {
		fmt.Fprintf(&msg, "%s: %s\r\n", k, extraHeaders[k])
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

	return m.transmit(to, messageBytes)
}

// fromAddress returns the bare address of the configured sender, which may be
// written with a display name. The envelope sender must never carry one.
func (m *Mailer) fromAddress() string {
	if a, err := mail.ParseAddress(m.cfg.From); err == nil {
		return a.Address
	}
	return m.cfg.From
}

// fromHeader returns the From header, naming the sender when the configured
// address does not. A bare address reads as machine traffic to spam filters.
func (m *Mailer) fromHeader() string {
	a, err := mail.ParseAddress(m.cfg.From)
	if err != nil {
		return m.cfg.From
	}
	if a.Name == "" {
		a.Name = "Herald"
	}
	return a.String()
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
			"Auto-Submitted",
			"List-Id",
			"List-Unsubscribe",
			"List-Unsubscribe-Post",
			"List-Post",
			"List-Archive",
			// Oversign List-* headers so intermediaries can't inject them.
			"List-Id",
			"List-Unsubscribe",
			"List-Unsubscribe-Post",
			"List-Post",
			"List-Archive",
			"Precedence",
			"Reply-To",
			"X-MC-MailingList",
		},
	}

	var b bytes.Buffer
	if err := dkim.Sign(&b, bytes.NewReader(message), options); err != nil {
		return nil, err
	}

	return b.Bytes(), nil
}
