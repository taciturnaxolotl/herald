package email

import (
	htmltemplate "html/template"
	"strings"
)

// confirmTextTmpl and confirmHTMLTmpl are fixed templates. The ONLY dynamic
// values are the SSH key fingerprint (hex, safe) and the confirmation URL
// (server-built). No user-supplied strings -- filenames, feed titles, config
// text -- are ever interpolated, so the confirmation email cannot be used to
// smuggle attacker content to the recipient.
const confirmTextTmpl = `Someone asked Herald to send an email digest to this address.

Request from SSH key:
  {{.Fingerprint}}

To start receiving digests, confirm here:
  {{.URL}}

If you did not set this up, ignore this email. Nothing will be sent unless
you confirm, and this request expires on its own.

-- Herald
`

var confirmHTMLTmpl = htmltemplate.Must(htmltemplate.New("confirm").Parse(`<p>Someone asked Herald to send an email digest to this address.</p>
<p>Request from SSH key:<br><code>{{.Fingerprint}}</code></p>
<p><a href="{{.URL}}">Confirm this subscription</a> to start receiving digests.</p>
<p style="font-size:12px;color:#666">If you did not set this up, ignore this email. Nothing will be sent unless you confirm, and this request expires on its own.</p>`))

type confirmData struct {
	Fingerprint string
	URL         string
}

// SendVerification sends a confirmation ("double opt-in") email asking the
// recipient to confirm a subscription. fingerprint is the requester's SSH key
// fingerprint; verifyURL is the confirmation link.
func (m *Mailer) SendVerification(to, fingerprint, verifyURL string) error {
	data := confirmData{Fingerprint: fingerprint, URL: verifyURL}

	var htmlBuf strings.Builder
	if err := confirmHTMLTmpl.Execute(&htmlBuf, data); err != nil {
		return err
	}

	textBody := confirmTextTmpl
	textBody = strings.ReplaceAll(textBody, "{{.Fingerprint}}", fingerprint)
	textBody = strings.ReplaceAll(textBody, "{{.URL}}", verifyURL)

	// Transactional, not bulk: mark as auto-generated so it is not treated as
	// list mail, and omit List-Unsubscribe (there is nothing to unsubscribe
	// from yet).
	extraHeaders := map[string]string{
		"Auto-Submitted": "auto-generated",
		"X-Mailer":       "Herald",
	}

	return m.deliver(to, "Confirm your Herald subscription", htmlBuf.String(), textBody, extraHeaders)
}
