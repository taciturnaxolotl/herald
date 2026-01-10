package email

import (
	"bytes"
	"embed"
	htmltemplate "html/template"
	texttemplate "text/template"
	"time"
)

//go:embed templates/*
var templateFS embed.FS

type DigestData struct {
	ConfigName string
	TotalItems int
	FeedGroups []FeedGroup
}

type FeedGroup struct {
	FeedName string
	FeedURL  string
	Items    []FeedItem
}

type FeedItem struct {
	Title     string
	Link      string
	Content   string
	Published time.Time
}

var (
	htmlTmpl *htmltemplate.Template
	textTmpl *texttemplate.Template
)

func init() {
	var err error
	htmlTmpl, err = htmltemplate.ParseFS(templateFS, "templates/digest.html")
	if err != nil {
		panic("failed to parse HTML template: " + err.Error())
	}
	textTmpl, err = texttemplate.ParseFS(templateFS, "templates/digest.txt")
	if err != nil {
		panic("failed to parse text template: " + err.Error())
	}
}

func RenderDigest(data *DigestData, inline bool, daysUntilExpiry int, showUrgentBanner, showWarningBanner bool) (html string, text string, err error) {
	tmplData := struct {
		*DigestData
		Inline            bool
		DaysUntilExpiry   int
		ShowUrgentBanner  bool
		ShowWarningBanner bool
	}{
		DigestData:        data,
		Inline:            inline,
		DaysUntilExpiry:   daysUntilExpiry,
		ShowUrgentBanner:  showUrgentBanner,
		ShowWarningBanner: showWarningBanner,
	}

	var htmlBuf, textBuf bytes.Buffer

	if err = htmlTmpl.Execute(&htmlBuf, tmplData); err != nil {
		return "", "", err
	}

	if err = textTmpl.Execute(&textBuf, tmplData); err != nil {
		return "", "", err
	}

	return htmlBuf.String(), textBuf.String(), nil
}
