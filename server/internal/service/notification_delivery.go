package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/integrations/channel"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	notificationDailyReportMarker = "notify-daily-report-request:"
	notificationReportBodyMarker  = "<!-- notify-daily-report -->"
	notificationDeliveryBatchSize = 10000
	notificationDeliveryMaxTry    = 5
	notificationReportBatchSize   = 500
	maxDailyReportRunes           = 30000
)

var errNotificationDeliverySkipped = errors.New("notification delivery skipped")

type NotificationDeliveryService struct {
	Queries  *db.Queries
	Pool     *pgxpool.Pool
	Issues   *IssueService
	Email    *EmailService
	Channels *channel.Registry
}

func NewNotificationDeliveryService(q *db.Queries, pool *pgxpool.Pool, issues *IssueService, email *EmailService, channels *channel.Registry) *NotificationDeliveryService {
	return &NotificationDeliveryService{Queries: q, Pool: pool, Issues: issues, Email: email, Channels: channels}
}

type notificationDeliveryGroup struct {
	key            string
	channel        string
	workspace      pgtype.UUID
	user           pgtype.UUID
	workspaceSlug  string
	workspaceNames map[string]struct{}
	address        string
	prefs          map[string]string
	rows           []db.NotificationDeliveryOutbox
	items          []notificationEmailItem
}

func (s *NotificationDeliveryService) RunOnce(ctx context.Context, now time.Time, heartbeat func(context.Context) error) (int64, error) {
	if s == nil || s.Queries == nil || s.Pool == nil {
		return 0, fmt.Errorf("notification delivery service is not configured")
	}
	var processed int64
	if err := s.startDueDailyReports(ctx, now, heartbeat); err != nil {
		return processed, err
	}
	reports, err := s.finishDailyReports(ctx, now, heartbeat)
	if err != nil {
		return processed, err
	}
	processed += int64(reports)
	delivered, err := s.flushOutbox(ctx, heartbeat)
	if err != nil {
		return processed, err
	}
	processed += delivered
	if now.Minute() == 0 {
		cutoff := pgtype.Timestamptz{Time: now.Add(-30 * 24 * time.Hour), Valid: true}
		if _, err := s.Queries.PruneNotificationDeliveryOutbox(ctx, db.PruneNotificationDeliveryOutboxParams{Cutoff: cutoff, RowLimit: 500}); err != nil {
			slog.WarnContext(ctx, "notification delivery retention pass failed", "error", err)
		}
	}
	return processed, nil
}

func (s *NotificationDeliveryService) flushOutbox(ctx context.Context, heartbeat func(context.Context) error) (int64, error) {
	rows, err := s.Queries.ClaimNotificationDeliveryOutbox(ctx, int32(notificationDeliveryBatchSize))
	if err != nil {
		return 0, fmt.Errorf("claim notification delivery outbox: %w", err)
	}
	groups := make(map[string]*notificationDeliveryGroup)
	var handled int64
	for i, row := range rows {
		if heartbeat != nil && i > 0 && i%10 == 0 {
			if err := heartbeat(ctx); err != nil {
				return handled, err
			}
		}
		item, err := s.Queries.GetNotificationDeliveryContext(ctx, row.ID)
		if err != nil {
			s.retryDelivery(ctx, row, "context_lookup")
			continue
		}
		prefs := map[string]string{}
		if err := json.Unmarshal(item.Preferences, &prefs); err != nil {
			s.finishDelivery(ctx, row.ID, "skipped", "preferences_invalid")
			handled++
			continue
		}
		if !item.MemberActive {
			s.finishDelivery(ctx, row.ID, "skipped", "member_inactive")
			handled++
			continue
		}
		if item.InboxRead || item.InboxArchived {
			s.finishDelivery(ctx, row.ID, "skipped", "inbox_item_seen")
			handled++
			continue
		}
		if item.ChannelType == "email" {
			address := strings.TrimSpace(prefs["email_address"])
			if prefs["email_enabled"] != "true" || address == "" {
				s.finishDelivery(ctx, row.ID, "skipped", "email_disabled")
				handled++
				continue
			}
			if _, err := mail.ParseAddress(address); err != nil {
				s.finishDelivery(ctx, row.ID, "skipped", "email_invalid")
				handled++
				continue
			}
		}
		if item.ChannelType == "telegram" && prefs["telegram_enabled"] != "true" {
			s.finishDelivery(ctx, row.ID, "skipped", "telegram_disabled")
			handled++
			continue
		}
		if strings.Contains(item.IssueDescription, notificationDailyReportMarker) && item.InboxType != "daily_report" {
			s.finishDelivery(ctx, row.ID, "skipped", "daily_report_task_event")
			handled++
			continue
		}
		if !channelNotificationEventAllowed(prefs, item.InboxType, item.Details) {
			s.finishDelivery(ctx, row.ID, "skipped", "event_disabled")
			handled++
			continue
		}
		groupKey := util.UUIDToString(item.WorkspaceID) + "/" + util.UUIDToString(item.RecipientID) + "/telegram"
		address := ""
		if item.ChannelType == "email" {
			address = strings.TrimSpace(prefs["email_address"])
			groupKey = util.UUIDToString(item.RecipientID) + "/email/" + address + "/" + prefs["digest_minutes"]
		}
		group := groups[groupKey]
		if group == nil {
			group = &notificationDeliveryGroup{
				key: groupKey, channel: item.ChannelType, workspace: item.WorkspaceID,
				user: item.RecipientID, workspaceSlug: item.WorkspaceSlug,
				workspaceNames: map[string]struct{}{}, address: address, prefs: prefs,
			}
			groups[groupKey] = group
		}
		group.workspaceNames[item.WorkspaceName] = struct{}{}
		group.rows = append(group.rows, row)
		group.items = append(group.items, notificationEmailItemFromContext(item))
	}

	for _, group := range groups {
		if heartbeat != nil {
			if err := heartbeat(ctx); err != nil {
				return handled, err
			}
		}
		var sendErr error
		switch group.channel {
		case "email":
			workspaceLabel := notificationWorkspaceLabel(group.workspaceNames)
			htmlBody, textBody, err := renderNotificationEmail(group.workspaceSlug, workspaceLabel, group.items)
			if err != nil {
				sendErr = err
			} else if s.Email == nil {
				sendErr = fmt.Errorf("email service unavailable")
			} else {
				subject := notificationEmailSubject(workspaceLabel, len(group.items))
				sendErr = s.Email.SendNotificationEmail(group.address, subject, htmlBody, textBody)
			}
		case "telegram":
			if group.prefs["telegram_enabled"] != "true" {
				for _, row := range group.rows {
					s.finishDelivery(ctx, row.ID, "skipped", "telegram_disabled")
					handled++
				}
				continue
			}
			sendErr = s.sendTelegramDigest(ctx, group)
		default:
			sendErr = fmt.Errorf("unsupported notification channel")
		}
		if sendErr != nil {
			if errors.Is(sendErr, errNotificationDeliverySkipped) {
				handled += int64(len(group.rows))
				continue
			}
			for _, row := range group.rows {
				s.retryDelivery(ctx, row, notificationErrorCode(sendErr))
				handled++
			}
			slog.WarnContext(ctx, "notification digest delivery failed",
				"workspace", group.workspaceSlug, "channel", group.channel, "items", len(group.rows), "code", notificationErrorCode(sendErr))
			continue
		}
		for _, row := range group.rows {
			s.finishDelivery(ctx, row.ID, "sent", "")
			handled++
		}
		slog.InfoContext(ctx, "notification digest delivered", "workspace", group.workspaceSlug, "channel", group.channel, "items", len(group.rows))
	}
	return handled, nil
}

func notificationEmailSubject(workspaceName string, itemCount int) string {
	if itemCount == 1 {
		return "Multica · Neues Update · " + workspaceName
	}
	return fmt.Sprintf("Multica · %d neue Updates · %s", itemCount, workspaceName)
}

func notificationWorkspaceLabel(workspaces map[string]struct{}) string {
	names := make([]string, 0, len(workspaces))
	for name := range workspaces {
		if name != "" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	if len(names) <= 1 {
		if len(names) == 1 {
			return names[0]
		}
		return "Multica"
	}
	return fmt.Sprintf("%d Workspaces", len(names))
}

func notificationEmailItemFromContext(item db.GetNotificationDeliveryContextRow) notificationEmailItem {
	title := item.IssueTitle
	if title == "" {
		title = item.InboxTitle
	}
	summary := notificationEventSummary(item.InboxType, item.IssueTitle, item.InboxTitle, item.InboxBody, item.Details)
	body := item.InboxBody
	context := item.IssueDescription
	if body == "" && item.InboxType != "daily_report" {
		body = item.IssueDescription
		context = ""
	}
	issueID := ""
	if item.IssueID.Valid {
		issueID = util.UUIDToString(item.IssueID)
	}
	if item.InboxType == "daily_report" {
		context = ""
	}
	return notificationEmailItem{
		Label: notificationEmailTypeLabel(item.InboxType),
		Title: title, Summary: summary, Body: body,
		Context: context, WorkspaceName: item.WorkspaceName,
		Link:          notificationIssueURL(item.WorkspaceSlug, issueID),
		IsDailyReport: item.InboxType == "daily_report",
	}
}

func notificationEventSummary(inboxType, issueTitle, inboxTitle, body string, details []byte) string {
	issue := issueTitle
	if issue == "" {
		issue = inboxTitle
	}
	switch inboxType {
	case "issue_assigned", "assignee_changed":
		return "Dir wurde diese Aufgabe zugewiesen."
	case "unassigned":
		return "Die Zuweisung für diese Aufgabe wurde aufgehoben."
	case "mentioned":
		var mentionDetails map[string]string
		_ = json.Unmarshal(details, &mentionDetails)
		if mentionDetails["mention_all"] == "true" {
			return "Du wurdest durch eine @all-Erwähnung zu dieser Aufgabe benachrichtigt."
		}
		return "Du wurdest in einem Kommentar zu dieser Aufgabe erwähnt."
	case "status_changed":
		var statusDetails map[string]string
		_ = json.Unmarshal(details, &statusDetails)
		if statusDetails["from"] != "" && statusDetails["to"] != "" {
			return fmt.Sprintf("Der Status von „%s“ änderte sich von %s zu %s.", issue, statusDetails["from"], statusDetails["to"])
		}
		return "Der Status dieser Aufgabe hat sich geändert."
	case "task_failed":
		if strings.TrimSpace(body) != "" {
			return "Ein Agentenlauf zu dieser Aufgabe ist fehlgeschlagen."
		}
		return "Ein Agentenlauf zu dieser Aufgabe ist fehlgeschlagen; ein Fehlergrund liegt nicht vor."
	case "daily_report":
		return "Tagesbericht für " + issue + "."
	case "new_comment":
		return "Ein neuer Kommentar zu dieser Aufgabe."
	default:
		return notificationEmailTypeLabel(inboxType) + " zu „" + issue + "“."
	}
}

func channelNotificationEventAllowed(prefs map[string]string, inboxType string, details []byte) bool {
	key := ""
	switch inboxType {
	case "issue_assigned", "assignee_changed", "unassigned":
		key = "channel_event_assigned"
	case "mentioned":
		var mentionDetails map[string]string
		_ = json.Unmarshal(details, &mentionDetails)
		if mentionDetails["mention_all"] == "true" {
			key = "channel_event_mention_all"
		} else {
			key = "channel_event_mentioned"
		}
	case "status_changed":
		key = "channel_event_status_changed"
	case "task_failed":
		key = "channel_event_task_failed"
	case "task_completed", "agent_completed":
		key = "channel_event_task_completed"
	case "daily_report":
		return true
	default:
		return false
	}
	value, configured := prefs[key]
	if !configured {
		return key == "channel_event_assigned" || key == "channel_event_mentioned" || key == "channel_event_task_failed"
	}
	return value == "true"
}

func notificationErrorCode(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	return "delivery_failed"
}

func (s *NotificationDeliveryService) finishDelivery(ctx context.Context, id pgtype.UUID, status, code string) {
	if _, err := s.Queries.MarkNotificationDeliveryFinished(ctx, db.MarkNotificationDeliveryFinishedParams{
		ID: id, Status: status, ErrorCode: code,
	}); err != nil {
		slog.WarnContext(ctx, "mark notification delivery failed", "status", status, "error", err)
	}
}

func (s *NotificationDeliveryService) retryDelivery(ctx context.Context, row db.NotificationDeliveryOutbox, code string) {
	backoff := int32(1)
	switch row.Attempts {
	case 2:
		backoff = 5
	case 3:
		backoff = 15
	case 4:
		backoff = 60
	default:
		if row.Attempts > 4 {
			backoff = 240
		}
	}
	if _, err := s.Queries.RetryNotificationDelivery(ctx, db.RetryNotificationDeliveryParams{
		ID: row.ID, MaxAttempts: notificationDeliveryMaxTry, BackoffMinutes: backoff, ErrorCode: code,
	}); err != nil {
		slog.WarnContext(ctx, "retry notification delivery update failed", "error", err)
	}
}

func (s *NotificationDeliveryService) sendTelegramDigest(ctx context.Context, group *notificationDeliveryGroup) error {
	if s.Channels == nil {
		return fmt.Errorf("Telegram channel is unavailable")
	}
	binding, err := s.Queries.FindChannelBindingForMember(ctx, db.FindChannelBindingForMemberParams{
		WorkspaceID: group.workspace, MulticaUserID: group.user, ChannelType: "telegram",
	})
	if errors.Is(err, pgx.ErrNoRows) {
		for _, row := range group.rows {
			s.finishDelivery(ctx, row.ID, "skipped", "telegram_unlinked")
		}
		return errNotificationDeliverySkipped
	}
	if err != nil {
		return fmt.Errorf("find Telegram binding: %w", err)
	}
	installation, err := s.Queries.GetChannelInstallationInWorkspace(ctx, db.GetChannelInstallationInWorkspaceParams{
		ID: binding.InstallationID, WorkspaceID: group.workspace, ChannelType: "telegram",
	})
	if err != nil {
		return fmt.Errorf("load Telegram installation: %w", err)
	}
	telegram, err := s.Channels.Build(channel.Config{
		Type: channel.Type("telegram"), ID: installation.ID, Raw: installation.Config,
	})
	if err != nil {
		return fmt.Errorf("build Telegram channel: %w", err)
	}
	text := renderNotificationTelegramDigest(group)
	if utf8.RuneCountInString(text) > 3900 {
		text = truncateNotificationText(text, 3750) + "\n\nMehr Details in Multica: " + notificationSettingsURL(group.workspaceSlug)
	}
	sendCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	_, err = telegram.Send(sendCtx, channel.OutboundMessage{ChatID: binding.ChannelUserID, Text: text})
	if err != nil {
		return fmt.Errorf("send Telegram digest: %w", err)
	}
	return nil
}

func renderNotificationTelegramDigest(group *notificationDeliveryGroup) string {
	var out strings.Builder
	fmt.Fprintf(&out, "Multica · %s\n\n", notificationWorkspaceLabel(group.workspaceNames))
	for i, item := range group.items {
		if i > 0 {
			out.WriteString("\n—\n\n")
		}
		out.WriteString(item.Label)
		out.WriteString(": ")
		out.WriteString(item.Title)
		out.WriteString("\n")
		if item.Summary != "" {
			out.WriteString(item.Summary)
			out.WriteString("\n")
		}
		if item.Context != "" {
			out.WriteString("Kontext: ")
			out.WriteString(truncateNotificationText(item.Context, 500))
			out.WriteString("\n")
		}
		if item.Body != "" {
			out.WriteString(truncateNotificationText(item.Body, 800))
			out.WriteString("\n")
		}
		if item.Link != "" {
			out.WriteString(item.Link)
		}
	}
	return out.String()
}
