package service

import (
	"bytes"
	"embed"
	"fmt"
	"html"
	"html/template"
	"io/fs"
	"net/url"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/resend/resend-go/v2"
)

//go:embed notify-email/email.html notify-email/assets/*.png
var notificationEmailFiles embed.FS

type notificationEmailItem struct {
	Label         string
	Title         string
	WorkspaceName string
	Summary       string
	Body          string
	Context       string
	Link          string
	IsDailyReport bool
}

type notificationEmailTemplateData struct {
	Title       string
	Section     string
	Eyebrow     string
	Intro       string
	Preheader   string
	SettingsURL string
	Content     template.HTML
}

func notificationAppURL() string {
	base := strings.TrimRight(strings.TrimSpace(os.Getenv("FRONTEND_ORIGIN")), "/")
	if base == "" {
		base = "https://multica.careli.de"
	}
	return base
}

func notificationSettingsURL(workspaceSlug string) string {
	return notificationAppURL() + "/" + url.PathEscape(workspaceSlug) + "/settings?tab=notifications"
}

func notificationIssueURL(workspaceSlug, issueID string) string {
	if workspaceSlug == "" || issueID == "" {
		return ""
	}
	return notificationAppURL() + "/" + url.PathEscape(workspaceSlug) + "/issues/" + url.PathEscape(issueID)
}

func notificationEmailTypeLabel(inboxType string) string {
	switch inboxType {
	case "issue_assigned", "assignee_changed", "unassigned":
		return "Zuweisung"
	case "mentioned":
		return "Erwähnung"
	case "status_changed":
		return "Statusänderung"
	case "task_failed":
		return "Agentenlauf fehlgeschlagen"
	case "task_completed", "agent_completed":
		return "Agentenlauf abgeschlossen"
	case "daily_report":
		return "Tagesbericht"
	case "new_comment":
		return "Neuer Kommentar"
	default:
		return "Multica-Update"
	}
}

func truncateNotificationText(value string, maxRunes int) string {
	value = strings.TrimSpace(value)
	if utf8.RuneCountInString(value) <= maxRunes {
		return value
	}
	runes := []rune(value)
	return strings.TrimSpace(string(runes[:maxRunes])) + "…"
}

func renderNotificationEmailContent(items []notificationEmailItem) template.HTML {
	var out strings.Builder
	for _, item := range items {
		bodyLimit := 1800
		if item.IsDailyReport {
			bodyLimit = maxDailyReportRunes
		}
		out.WriteString(`<table role="presentation" cellpadding="0" cellspacing="0" border="0" width="100%" style="margin:0 0 18px;border:1px solid #e3e4dd;border-radius:12px;"><tr><td style="padding:18px 20px;">`)
		if item.WorkspaceName != "" {
			out.WriteString(`<p style="margin:0 0 7px;font-family:Manrope,Arial,Helvetica,sans-serif;font-size:11px;line-height:16px;font-weight:700;color:#64748b;">`)
			out.WriteString(html.EscapeString(item.WorkspaceName))
			out.WriteString(`</p>`)
		}
		out.WriteString(`<p style="margin:0 0 7px;font-family:Manrope,Arial,Helvetica,sans-serif;font-size:11px;line-height:16px;font-weight:800;letter-spacing:1px;text-transform:uppercase;color:#ff4f79;">`)
		out.WriteString(html.EscapeString(notificationEmailTypeLabelForItem(item)))
		out.WriteString(`</p><h2 style="margin:0 0 10px;font-family:Manrope,Arial,Helvetica,sans-serif;font-size:19px;line-height:27px;font-weight:750;color:#2b6c8c;">`)
		out.WriteString(html.EscapeString(item.Title))
		out.WriteString(`</h2>`)
		if item.Summary != "" {
			out.WriteString(`<p style="margin:0 0 10px;font-family:Manrope,Arial,Helvetica,sans-serif;font-size:15px;line-height:23px;color:#334155;">`)
			out.WriteString(html.EscapeString(item.Summary))
			out.WriteString(`</p>`)
		}
		if item.Context != "" {
			out.WriteString(`<p style="margin:0 0 10px;font-family:Manrope,Arial,Helvetica,sans-serif;font-size:13px;line-height:21px;color:#64748b;"><strong>Kontext:</strong> `)
			out.WriteString(html.EscapeString(truncateNotificationText(item.Context, 900)))
			out.WriteString(`</p>`)
		}
		if item.Body != "" {
			out.WriteString(`<div style="margin:0 0 12px;font-family:Manrope,Arial,Helvetica,sans-serif;font-size:14px;line-height:22px;color:#475569;white-space:pre-wrap;">`)
			out.WriteString(html.EscapeString(truncateNotificationText(item.Body, bodyLimit)))
			out.WriteString(`</div>`)
		}
		if item.Link != "" {
			out.WriteString(`<p style="margin:0;font-family:Manrope,Arial,Helvetica,sans-serif;font-size:13px;line-height:20px;"><a href="`)
			out.WriteString(html.EscapeString(item.Link))
			out.WriteString(`" style="color:#99004f;text-decoration:underline;">In Multica öffnen</a></p>`)
		}
		out.WriteString(`</td></tr></table>`)
	}
	return template.HTML(out.String()) // All user-controlled values above are HTML-escaped.
}

func notificationEmailTypeLabelForItem(item notificationEmailItem) string {
	if item.Label != "" {
		return item.Label
	}
	return "Update"
}

func renderNotificationEmail(workspaceSlug, workspaceName string, items []notificationEmailItem) (string, string, error) {
	if len(items) == 0 {
		return "", "", fmt.Errorf("notification email has no items")
	}
	title := fmt.Sprintf("%d Updates · %s", len(items), workspaceName)
	section := "Benachrichtigungen"
	intro := fmt.Sprintf("Hier sind die neuen Entwicklungen im Workspace %s.", workspaceName)
	if len(items) == 1 && items[0].IsDailyReport {
		title = items[0].Title
		section = "Tagesbericht"
		intro = "Der Bericht ordnet den aktuellen Stand in den Projektverlauf ein."
	}
	preheader := truncateNotificationText(items[0].Summary, 110)
	if preheader == "" {
		preheader = items[0].Title
	}
	data := notificationEmailTemplateData{
		Title: title, Section: section, Eyebrow: workspaceName, Intro: intro,
		Preheader: preheader, SettingsURL: notificationSettingsURL(workspaceSlug),
		Content: renderNotificationEmailContent(items),
	}
	tmpl, err := template.ParseFS(notificationEmailFiles, "notify-email/email.html")
	if err != nil {
		return "", "", fmt.Errorf("parse notification email template: %w", err)
	}
	var rendered bytes.Buffer
	if err := tmpl.Execute(&rendered, data); err != nil {
		return "", "", fmt.Errorf("render notification email template: %w", err)
	}
	var plain strings.Builder
	plain.WriteString(title)
	plain.WriteString("\n\n")
	for _, item := range items {
		bodyLimit := 1800
		if item.IsDailyReport {
			bodyLimit = maxDailyReportRunes
		}
		plain.WriteString(notificationEmailTypeLabelForItem(item))
		plain.WriteString(": ")
		plain.WriteString(item.Title)
		plain.WriteString("\n")
		if item.WorkspaceName != "" {
			plain.WriteString("Workspace: ")
			plain.WriteString(item.WorkspaceName)
			plain.WriteString("\n")
		}
		if item.Summary != "" {
			plain.WriteString(item.Summary)
			plain.WriteString("\n")
		}
		if item.Context != "" {
			plain.WriteString("Kontext: ")
			plain.WriteString(truncateNotificationText(item.Context, 900))
			plain.WriteString("\n")
		}
		if item.Body != "" {
			plain.WriteString(truncateNotificationText(item.Body, bodyLimit))
			plain.WriteString("\n")
		}
		if item.Link != "" {
			plain.WriteString(item.Link)
			plain.WriteString("\n")
		}
		plain.WriteString("\n")
	}
	plain.WriteString("Benachrichtigungen verwalten: ")
	plain.WriteString(data.SettingsURL)
	return rendered.String(), plain.String(), nil
}

func notificationEmailAssets() ([]*resend.Attachment, error) {
	entries := []struct{ path, filename, contentID string }{
		{"notify-email/assets/logo-careli-pink.png", "logo-careli-pink.png", "careli-logo-pink"},
		{"notify-email/assets/logo-careli-white.png", "logo-careli-white.png", "careli-logo-white"},
	}
	assets := make([]*resend.Attachment, 0, len(entries))
	for _, entry := range entries {
		content, err := fs.ReadFile(notificationEmailFiles, entry.path)
		if err != nil {
			return nil, fmt.Errorf("read notification email asset %s: %w", entry.filename, err)
		}
		assets = append(assets, &resend.Attachment{
			Content: content, Filename: entry.filename, ContentType: "image/png", ContentId: entry.contentID,
		})
	}
	return assets, nil
}
