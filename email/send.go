package email

import (
	"crypto/tls"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/smtp"
	"strings"
)

type SMTPConfig struct {
	Host string
	Port int
	User string
	Pass string
	From string
}

type Mailer struct {
	cfg          SMTPConfig
	unsubBaseURL string
}

func NewMailer(cfg SMTPConfig, unsubBaseURL string) *Mailer {
	return &Mailer{
		cfg:          cfg,
		unsubBaseURL: unsubBaseURL,
	}
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
		defer conn.Close()

		client, err := smtp.NewClient(conn, m.cfg.Host)
		if err != nil {
			return fmt.Errorf("SMTP client: %w", err)
		}
		defer client.Close()

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
	defer conn.Close()

	client, err := smtp.NewClient(conn, m.cfg.Host)
	if err != nil {
		return fmt.Errorf("SMTP client: %w", err)
	}
	defer client.Close()

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

func (m *Mailer) Send(to, subject, htmlBody, textBody, unsubToken, dashboardURL string) error {
	addr := net.JoinHostPort(m.cfg.Host, fmt.Sprintf("%d", m.cfg.Port))

	boundary := "==herald-boundary-a1b2c3d4e5f6=="

	// Add footer with unsubscribe and dashboard links
	var htmlFooter strings.Builder
	var textFooter strings.Builder

	if unsubToken != "" || dashboardURL != "" {
		htmlFooter.WriteString(`<hr><p style="font-size: 12px; color: #666;">`)
		textFooter.WriteString("\n\n---\n")

		if dashboardURL != "" {
			htmlFooter.WriteString(fmt.Sprintf(`<a href="%s">profile</a>`, dashboardURL))
			textFooter.WriteString(fmt.Sprintf("profile: %s\n", dashboardURL))
		}

		if unsubToken != "" {
			unsubURL := m.unsubBaseURL + "/unsubscribe/" + unsubToken
			if dashboardURL != "" {
				htmlFooter.WriteString(" • ")
				textFooter.WriteString("")
			}
			htmlFooter.WriteString(fmt.Sprintf(`<a href="%s">Unsubscribe</a>`, unsubURL))
			textFooter.WriteString(fmt.Sprintf("unsubscribe: %s\n", unsubURL))
		}

		htmlFooter.WriteString("</p>")
		htmlBody = htmlBody + htmlFooter.String()
		textBody = textBody + textFooter.String()
	}

	headers := make(map[string]string)
	headers["From"] = m.cfg.From
	headers["To"] = to
	headers["Subject"] = mime.QEncoding.Encode("utf-8", subject)
	headers["MIME-Version"] = "1.0"
	headers["Content-Type"] = fmt.Sprintf("multipart/alternative; boundary=%q", boundary)

	// Add RFC 8058 unsubscribe headers
	if unsubToken != "" {
		unsubURL := m.unsubBaseURL + "/unsubscribe/" + unsubToken
		headers["List-Unsubscribe"] = fmt.Sprintf("<%s>", unsubURL)
		headers["List-Unsubscribe-Post"] = "List-Unsubscribe=One-Click"
	}

	var msg strings.Builder
	for k, v := range headers {
		msg.WriteString(fmt.Sprintf("%s: %s\r\n", k, v))
	}
	msg.WriteString("\r\n")

	msg.WriteString(fmt.Sprintf("--%s\r\n", boundary))
	msg.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	msg.WriteString("Content-Transfer-Encoding: quoted-printable\r\n\r\n")
	textQP := encodeQuotedPrintable(textBody)
	msg.WriteString(textQP)
	msg.WriteString("\r\n")

	msg.WriteString(fmt.Sprintf("--%s\r\n", boundary))
	msg.WriteString("Content-Type: text/html; charset=utf-8\r\n")
	msg.WriteString("Content-Transfer-Encoding: quoted-printable\r\n\r\n")
	htmlQP := encodeQuotedPrintable(htmlBody)
	msg.WriteString(htmlQP)
	msg.WriteString("\r\n")

	msg.WriteString(fmt.Sprintf("--%s--\r\n", boundary))

	var auth smtp.Auth
	if m.cfg.User != "" && m.cfg.Pass != "" {
		auth = smtp.PlainAuth("", m.cfg.User, m.cfg.Pass, m.cfg.Host)
	}

	if m.cfg.Port == 465 {
		return m.sendWithTLS(addr, auth, to, msg.String())
	}

	return smtp.SendMail(addr, auth, m.cfg.From, []string{to}, []byte(msg.String()))
}

func encodeQuotedPrintable(s string) string {
	var buf strings.Builder
	w := quotedprintable.NewWriter(&buf)
	w.Write([]byte(s))
	w.Close()
	return buf.String()
}

func (m *Mailer) sendWithTLS(addr string, auth smtp.Auth, to, msg string) error {
	tlsConfig := &tls.Config{
		ServerName: m.cfg.Host,
		MinVersion: tls.VersionTLS12,
	}

	conn, err := tls.Dial("tcp", addr, tlsConfig)
	if err != nil {
		return fmt.Errorf("TLS dial: %w", err)
	}
	defer conn.Close()

	client, err := smtp.NewClient(conn, m.cfg.Host)
	if err != nil {
		return fmt.Errorf("SMTP client: %w", err)
	}
	defer client.Close()

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

	if _, err = w.Write([]byte(msg)); err != nil {
		return fmt.Errorf("write: %w", err)
	}

	if err = w.Close(); err != nil {
		return fmt.Errorf("close data: %w", err)
	}

	return client.Quit()
}
