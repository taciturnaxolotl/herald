package email

import (
	"bytes"
	"html/template"
	"regexp"
	"strings"
)

// gmailShellTemplate wraps a rendered digest in a Gmail-mimicking HTML shell.
// Gmail strips <html>/<head>/<body>, scopes CSS, and renders in a constrained
// reading pane with Arial font. This shell approximates that for preview.
const gmailShellTemplate = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Gmail Preview — Herald Digest</title>
<style>
  /* Gmail reading pane simulation */
  * { box-sizing: border-box; }
  body {
    margin: 0;
    padding: 0;
    font-family: Arial, Helvetica, sans-serif;
    font-size: 14px;
    line-height: 1.5;
    color: #222;
    background: #f6f8fc;
  }
  .gmail-shell {
    max-width: 640px;
    margin: 20px auto;
    background: #fff;
    border-radius: 16px;
    box-shadow: 0 1px 3px rgba(0,0,0,0.08);
    overflow: hidden;
  }
  .gmail-header {
    padding: 16px 24px;
    border-bottom: 1px solid #e8eaed;
    display: flex;
    align-items: center;
    gap: 16px;
  }
  .gmail-avatar {
    width: 40px;
    height: 40px;
    border-radius: 50%;
    background: #1a73e8;
    color: #fff;
    display: flex;
    align-items: center;
    justify-content: center;
    font-weight: bold;
    font-size: 18px;
    flex-shrink: 0;
  }
  .gmail-sender-info { flex: 1; min-width: 0; }
  .gmail-sender { font-weight: bold; font-size: 14px; }
  .gmail-recipient { color: #5f6368; font-size: 12px; }
  .gmail-subject {
    font-size: 22px;
    line-height: 1.3;
    padding: 16px 24px 8px 24px;
  }
  .gmail-body {
    padding: 0 24px 24px 24px;
    /* Gmail strips <head>/<style>/<body> from the original email, keeping
       only the body content. The style block moves inline but Gmail scopes
       all class names. For preview we just inject the digest HTML as-is
       since browsers apply <style> correctly. */
  }
  .gmail-body img {
    max-width: 100%;
    height: auto;
  }
  /* Gmail link color for email body */
  .gmail-body a[href] {
    color: #15c;
  }
  .gmail-shell-label {
    font-size: 11px;
    color: #5f6368;
    text-transform: uppercase;
    letter-spacing: 0.5px;
    padding: 8px 24px 0 24px;
  }
</style>
</head>
<body>
<div class="gmail-shell-label">Gmail Preview</div>
<div class="gmail-shell">
  <div class="gmail-header">
    <div class="gmail-avatar">H</div>
    <div class="gmail-sender-info">
      <div class="gmail-sender">Herald</div>
      <div class="gmail-recipient">to you</div>
    </div>
  </div>
  <div class="gmail-subject">{{.Subject}}</div>
  <div class="gmail-body">
    {{.BodyHTML}}
  </div>
</div>
</body>
</html>`

// gmailUnwrap matches a full HTML document and extracts the body content.
// Gmail strips <html>/<head>/<body> wrappers, so we simulate that.
var gmailUnwrapHead = regexp.MustCompile(`(?s)<!DOCTYPE[^>]*>\s*<html[^>]*>\s*<head[^>]*>.*?</head>`)
var gmailUnwrapBody = regexp.MustCompile(`(?s)<body[^>]*>(.*)</body>\s*</html>`)

// WrapForGMailPreview takes a full HTML digest document and wraps it in a
// Gmail shell for realistic preview rendering.
func WrapForGMailPreview(digestHTML, subject string) (string, error) {
	// Strip <html>/<head>/<body> wrapping as Gmail does
	body := digestHTML
	body = gmailUnwrapHead.ReplaceAllString(body, "")
	body = gmailUnwrapBody.ReplaceAllString(body, "$1")
	body = strings.TrimSpace(body)

	tmpl, err := template.New("gmail-shell").Parse(gmailShellTemplate)
	if err != nil {
		return "", err
	}

	var buf bytes.Buffer
	err = tmpl.Execute(&buf, map[string]any{
		"Subject":  subject,
		"BodyHTML": template.HTML(body), // #nosec G203 — content is already sanitized
	})
	if err != nil {
		return "", err
	}

	return buf.String(), nil
}
