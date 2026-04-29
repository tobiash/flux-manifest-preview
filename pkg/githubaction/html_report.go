package githubaction

import (
	"embed"
	"fmt"
	"html/template"
	"strings"
)

//go:embed reportui/report.html.tmpl reportui/report.css reportui/report.js
var reportUI embed.FS

type htmlReportTemplateData struct {
	CSS  template.CSS
	JS   template.JS
	JSON template.JS
}

func RenderHTMLReport(data HTMLReportData) (string, error) {
	css, err := reportUI.ReadFile("reportui/report.css")
	if err != nil {
		return "", fmt.Errorf("reading report css: %w", err)
	}
	js, err := reportUI.ReadFile("reportui/report.js")
	if err != nil {
		return "", fmt.Errorf("reading report js: %w", err)
	}
	jsonData, err := reportDataJSON(data)
	if err != nil {
		return "", fmt.Errorf("encoding report data: %w", err)
	}
	tmplBytes, err := reportUI.ReadFile("reportui/report.html.tmpl")
	if err != nil {
		return "", fmt.Errorf("reading report template: %w", err)
	}
	tmpl, err := template.New("report.html.tmpl").Parse(string(tmplBytes))
	if err != nil {
		return "", fmt.Errorf("parsing report template: %w", err)
	}

	var out strings.Builder
	err = tmpl.Execute(&out, htmlReportTemplateData{
		CSS:  template.CSS(css), //nolint:gosec // Static embedded CSS controlled by this repository.
		JS:   template.JS(js),   //nolint:gosec // Static embedded JS controlled by this repository.
		JSON: jsonData,
	})
	if err != nil {
		return "", fmt.Errorf("executing report template: %w", err)
	}
	return out.String(), nil
}
