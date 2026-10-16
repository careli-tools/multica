package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

var (
	ErrDailyReportNotEnabled = errors.New("daily report is not enabled for this workspace")
	ErrDailyReportAlreadyRun = errors.New("daily report already exists for this date")
)

func IsNotificationDailyReportIssue(description string) bool {
	return strings.Contains(description, notificationDailyReportMarker)
}

func (s *NotificationDeliveryService) startDueDailyReports(ctx context.Context, now time.Time, heartbeat func(context.Context) error) error {
	subscriptions, err := s.Queries.ListNotificationDailyReportPreferences(ctx, 10000)
	if err != nil {
		return fmt.Errorf("list daily report preferences: %w", err)
	}
	for i, subscription := range subscriptions {
		if heartbeat != nil && i > 0 && i%50 == 0 {
			if err := heartbeat(ctx); err != nil {
				return err
			}
		}
		prefs := map[string]string{}
		if err := json.Unmarshal(subscription.Preferences, &prefs); err != nil {
			continue
		}
		if prefs["daily_report_enabled"] != "true" || prefs["daily_report_agent_id"] == "" {
			continue
		}
		latestPrefs, err := s.Queries.GetNotificationPreference(ctx, db.GetNotificationPreferenceParams{
			WorkspaceID: subscription.WorkspaceID, UserID: subscription.UserID,
		})
		if err != nil {
			continue
		}
		if err := json.Unmarshal(latestPrefs.Preferences, &prefs); err != nil || prefs["daily_report_enabled"] != "true" {
			continue
		}
		location, err := time.LoadLocation(prefs["daily_report_timezone"])
		if err != nil {
			location = time.UTC
		}
		reportTime, err := time.Parse("15:04", prefs["daily_report_time"])
		if err != nil {
			reportTime = time.Date(0, 1, 1, 18, 0, 0, 0, time.UTC)
		}
		localNow := now.In(location)
		if localNow.Hour() < reportTime.Hour() ||
			(localNow.Hour() == reportTime.Hour() && localNow.Minute() < reportTime.Minute()) {
			continue
		}
		date := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), 0, 0, 0, 0, time.UTC)
		if _, err := s.startDailyReportRun(ctx, subscription.WorkspaceID, subscription.UserID, subscription.WorkspaceSlug, prefs, date, location, false); err != nil && !errors.Is(err, ErrDailyReportAlreadyRun) {
			slog.WarnContext(ctx, "daily report start failed", "workspace", subscription.WorkspaceSlug, "report_date", date.Format("2006-01-02"), "error", err)
		}
	}
	return nil
}

type DailyReportStartResult struct {
	ReportDate string
	IssueID    string
}

func (s *NotificationDeliveryService) StartDailyReportNow(ctx context.Context, workspaceID, userID pgtype.UUID, now time.Time) (DailyReportStartResult, error) {
	pref, err := s.Queries.GetNotificationPreference(ctx, db.GetNotificationPreferenceParams{WorkspaceID: workspaceID, UserID: userID})
	if err != nil {
		return DailyReportStartResult{}, err
	}
	prefs := map[string]string{}
	if err := json.Unmarshal(pref.Preferences, &prefs); err != nil {
		return DailyReportStartResult{}, ErrDailyReportNotEnabled
	}
	if prefs["daily_report_agent_id"] == "" {
		return DailyReportStartResult{}, ErrDailyReportNotEnabled
	}
	workspace, err := s.Queries.GetWorkspace(ctx, workspaceID)
	if err != nil {
		return DailyReportStartResult{}, err
	}
	location, err := time.LoadLocation(prefs["daily_report_timezone"])
	if err != nil {
		location = time.UTC
	}
	localNow := now.In(location)
	date := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), 0, 0, 0, 0, time.UTC)
	issueID, err := s.startDailyReportRun(ctx, workspaceID, userID, workspace.Slug, prefs, date, location, true)
	if err != nil {
		return DailyReportStartResult{}, err
	}
	return DailyReportStartResult{ReportDate: date.Format("2006-01-02"), IssueID: util.UUIDToString(issueID)}, nil
}

func (s *NotificationDeliveryService) startDailyReportRun(ctx context.Context, workspaceID, userID pgtype.UUID, workspaceSlug string, prefs map[string]string, date time.Time, location *time.Location, manual bool) (pgtype.UUID, error) {
	agentID, err := util.ParseUUID(prefs["daily_report_agent_id"])
	if err != nil {
		return pgtype.UUID{}, ErrDailyReportNotEnabled
	}
	agent, err := s.Queries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{ID: agentID, WorkspaceID: workspaceID})
	if err != nil || agent.ArchivedAt.Valid {
		return pgtype.UUID{}, ErrDailyReportNotEnabled
	}
	reportDate := pgtype.Date{Time: date, Valid: true}
	run, err := s.Queries.InsertNotificationDailyReportRun(ctx, db.InsertNotificationDailyReportRunParams{
		WorkspaceID: workspaceID, UserID: userID, ReportDate: reportDate, AgentID: agentID, IsManual: manual,
		Format:       normalizeDailyReportFormat(prefs["daily_report_format"]),
		Instructions: truncateNotificationText(prefs["daily_report_instructions"], 4000),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return pgtype.UUID{}, ErrDailyReportAlreadyRun
	}
	if err != nil {
		return pgtype.UUID{}, fmt.Errorf("reserve daily report run: %w", err)
	}
	previousDate := pgtype.Date{Time: date.AddDate(0, 0, -1), Valid: true}
	previous, previousErr := s.Queries.GetLatestNotificationDailyReport(ctx, db.GetLatestNotificationDailyReportParams{
		WorkspaceID: workspaceID, UserID: userID, PreviousDate: previousDate,
	})
	if errors.Is(previousErr, pgx.ErrNoRows) {
		previous, previousErr = s.importLatestLegacyDailyReport(ctx, workspaceID, userID, previousDate)
	}
	if previousErr != nil && !errors.Is(previousErr, pgx.ErrNoRows) {
		slog.WarnContext(ctx, "daily report context lookup failed", "workspace", workspaceSlug, "error", previousErr)
	}
	priorText := ""
	if previousErr == nil {
		priorText = fmt.Sprintf("Bericht vom %s\n\n%s", previous.ReportDate.Time.Format("2006-01-02"), previous.ReportBody)
	}
	created, err := s.Issues.Create(ctx, IssueCreateParams{
		WorkspaceID: workspaceID,
		Title:       fmt.Sprintf("Tagesbericht %s · %s", date.Format("2006-01-02"), workspaceSlug),
		Description: pgtype.Text{String: buildDailyReportPrompt(workspaceID, userID, workspaceSlug, date, location.String(), run.Format, run.Instructions, priorText), Valid: true},
		Status:      "todo", Priority: "medium",
		AssigneeType: pgtype.Text{String: "agent", Valid: true}, AssigneeID: agentID,
		CreatorType: "member", CreatorID: userID,
	}, IssueCreateOpts{})
	if err != nil {
		return pgtype.UUID{}, fmt.Errorf("create daily report issue: %w", err)
	}
	if !created.Issue.ID.Valid {
		return pgtype.UUID{}, fmt.Errorf("daily report issue id missing")
	}
	if _, err := s.Queries.SetNotificationDailyReportIssue(ctx, db.SetNotificationDailyReportIssueParams{ID: run.ID, IssueID: created.Issue.ID}); err != nil {
		return pgtype.UUID{}, fmt.Errorf("save daily report issue: %w", err)
	}
	return created.Issue.ID, nil
}

func (s *NotificationDeliveryService) importLatestLegacyDailyReport(ctx context.Context, workspaceID, userID pgtype.UUID, reportDate pgtype.Date) (db.NotificationDailyReportRun, error) {
	legacy, err := s.Queries.FindLegacyNotificationDailyReportIssue(ctx, db.FindLegacyNotificationDailyReportIssueParams{
		WorkspaceID: workspaceID,
		UserID:      util.UUIDToString(userID),
		ReportDate:  reportDate.Time.Format("2006-01-02"),
	})
	if err != nil {
		return db.NotificationDailyReportRun{}, err
	}
	if !legacy.AgentID.Valid || !legacy.ID.Valid {
		return db.NotificationDailyReportRun{}, pgx.ErrNoRows
	}
	comments, err := s.Queries.ListNotificationDailyReportComments(ctx, db.ListNotificationDailyReportCommentsParams{
		WorkspaceID: workspaceID, IssueID: legacy.ID,
	})
	if err != nil {
		return db.NotificationDailyReportRun{}, err
	}
	report := ""
	for _, comment := range comments {
		if comment.AuthorType != "agent" || comment.AuthorID != legacy.AgentID || !strings.HasPrefix(comment.Content, notificationReportBodyMarker) {
			continue
		}
		report = strings.TrimSpace(strings.TrimPrefix(comment.Content, notificationReportBodyMarker))
		break
	}
	if report == "" {
		return db.NotificationDailyReportRun{}, pgx.ErrNoRows
	}
	if _, err := s.Queries.ImportLegacyNotificationDailyReport(ctx, db.ImportLegacyNotificationDailyReportParams{
		WorkspaceID: workspaceID, UserID: userID,
		ReportDate: reportDate,
		AgentID:    legacy.AgentID, IssueID: legacy.ID,
		ReportBody: truncateNotificationText(report, maxDailyReportRunes),
	}); err != nil {
		return db.NotificationDailyReportRun{}, fmt.Errorf("import prior daily report: %w", err)
	}
	return s.Queries.GetLatestNotificationDailyReport(ctx, db.GetLatestNotificationDailyReportParams{
		WorkspaceID: workspaceID, UserID: userID, PreviousDate: reportDate,
	})
}

func (s *NotificationDeliveryService) finishDailyReports(ctx context.Context, now time.Time, heartbeat func(context.Context) error) (int, error) {
	runs, err := s.Queries.ListActiveNotificationDailyReportRuns(ctx, notificationReportBatchSize)
	if err != nil {
		return 0, fmt.Errorf("list active daily report runs: %w", err)
	}
	finished := 0
	for i, run := range runs {
		if heartbeat != nil && i > 0 && i%20 == 0 {
			if err := heartbeat(ctx); err != nil {
				return finished, err
			}
		}
		if run.Status == "creating" {
			recovered, err := s.reconcileDailyReportIssue(ctx, run, now)
			if err != nil {
				slog.WarnContext(ctx, "daily report reconciliation failed", "error", err)
				continue
			}
			if recovered == nil {
				if now.Sub(run.CreatedAt.Time) > time.Hour {
					if err := s.failDailyReport(ctx, run, "issue_not_confirmed"); err != nil {
						return finished, err
					}
					finished++
				}
				continue
			}
			run.Status = "running"
			run.IssueID = *recovered
		}
		if !run.IssueID.Valid {
			continue
		}
		preferences, err := s.Queries.GetNotificationPreference(ctx, db.GetNotificationPreferenceParams{
			WorkspaceID: run.WorkspaceID, UserID: run.UserID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			if _, cancelErr := s.Queries.SetNotificationDailyReportCancelled(ctx, run.ID); cancelErr != nil {
				return finished, fmt.Errorf("cancel daily report after preferences removal: %w", cancelErr)
			}
			continue
		}
		if err != nil {
			slog.WarnContext(ctx, "daily report preference lookup failed", "error", err)
			continue
		}
		currentPrefs := map[string]string{}
		if json.Unmarshal(preferences.Preferences, &currentPrefs) != nil || (!run.IsManual && currentPrefs["daily_report_enabled"] != "true") {
			if _, cancelErr := s.Queries.SetNotificationDailyReportCancelled(ctx, run.ID); cancelErr != nil {
				return finished, fmt.Errorf("cancel disabled daily report: %w", cancelErr)
			}
			continue
		}
		tasks, err := s.Queries.ListTasksByIssue(ctx, run.IssueID)
		if err != nil {
			slog.WarnContext(ctx, "daily report task lookup failed", "error", err)
			continue
		}
		if len(tasks) == 0 {
			if now.Sub(run.CreatedAt.Time) > 2*time.Hour {
				if err := s.failDailyReport(ctx, run, "agent_task_missing"); err != nil {
					return finished, err
				}
				finished++
			}
			continue
		}
		latest := tasks[0]
		if latest.AgentID != run.AgentID || !isTerminalTaskStatus(latest.Status) {
			continue
		}
		if latest.Status != "completed" {
			if err := s.failDailyReport(ctx, run, "agent_task_failed"); err != nil {
				return finished, err
			}
			finished++
			continue
		}
		comments, err := s.Queries.ListNotificationDailyReportComments(ctx, db.ListNotificationDailyReportCommentsParams{
			WorkspaceID: run.WorkspaceID, IssueID: run.IssueID,
		})
		if err != nil {
			slog.WarnContext(ctx, "daily report comment lookup failed", "error", err)
			continue
		}
		report := ""
		for _, comment := range comments {
			if comment.AuthorType != "agent" || util.UUIDToString(comment.AuthorID) != util.UUIDToString(run.AgentID) || !strings.HasPrefix(comment.Content, notificationReportBodyMarker) {
				continue
			}
			report = strings.TrimSpace(strings.TrimPrefix(comment.Content, notificationReportBodyMarker))
			break
		}
		if report == "" {
			if now.Sub(run.CreatedAt.Time) > 2*time.Hour {
				if err := s.failDailyReport(ctx, run, "report_comment_missing"); err != nil {
					return finished, err
				}
				finished++
			}
			continue
		}
		report = truncateNotificationText(report, maxDailyReportRunes)
		if err := s.completeDailyReport(ctx, run, report); err != nil {
			return finished, err
		}
		finished++
	}
	return finished, nil
}

func (s *NotificationDeliveryService) reconcileDailyReportIssue(ctx context.Context, run db.NotificationDailyReportRun, now time.Time) (*pgtype.UUID, error) {
	if now.Sub(run.CreatedAt.Time) < 2*time.Minute {
		return nil, nil
	}
	marker := dailyReportIssueMarker(run.WorkspaceID, run.UserID, run.ReportDate.Time)
	issue, err := s.Queries.FindNotificationDailyReportIssue(ctx, db.FindNotificationDailyReportIssueParams{
		WorkspaceID: run.WorkspaceID, Marker: pgtype.Text{String: marker, Valid: true},
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if !issue.ID.Valid || issue.AssigneeID != run.AgentID {
		return nil, nil
	}
	if _, err := s.Queries.SetNotificationDailyReportIssue(ctx, db.SetNotificationDailyReportIssueParams{ID: run.ID, IssueID: issue.ID}); err != nil {
		return nil, err
	}
	return &issue.ID, nil
}

func (s *NotificationDeliveryService) completeDailyReport(ctx context.Context, run db.NotificationDailyReportRun, report string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin daily report completion: %w", err)
	}
	defer tx.Rollback(ctx)
	qtx := s.Queries.WithTx(tx)
	updated, err := qtx.SetNotificationDailyReportCompleted(ctx, db.SetNotificationDailyReportCompletedParams{ID: run.ID, ReportBody: report})
	if err != nil {
		return fmt.Errorf("mark daily report complete: %w", err)
	}
	if updated == 0 {
		return nil
	}
	details, _ := json.Marshal(map[string]string{
		"report_date": run.ReportDate.Time.Format("2006-01-02"),
		"format":      run.Format,
	})
	if _, err := qtx.CreateInboxItem(ctx, db.CreateInboxItemParams{
		WorkspaceID:   run.WorkspaceID,
		RecipientType: "member",
		RecipientID:   run.UserID,
		Type:          "daily_report",
		Severity:      "info",
		IssueID:       run.IssueID,
		Title:         "Tagesbericht · " + run.ReportDate.Time.Format("2006-01-02"),
		Body:          pgtype.Text{String: report, Valid: true},
		ActorType:     pgtype.Text{String: "agent", Valid: true},
		ActorID:       run.AgentID,
		Details:       details,
	}); err != nil {
		return fmt.Errorf("create daily report inbox item: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit daily report completion: %w", err)
	}
	return nil
}

func (s *NotificationDeliveryService) failDailyReport(ctx context.Context, run db.NotificationDailyReportRun, code string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin daily report failure: %w", err)
	}
	defer tx.Rollback(ctx)
	qtx := s.Queries.WithTx(tx)
	updated, err := qtx.SetNotificationDailyReportFailed(ctx, db.SetNotificationDailyReportFailedParams{ID: run.ID, ErrorCode: code})
	if err != nil {
		return fmt.Errorf("mark daily report failed: %w", err)
	}
	if updated == 0 {
		return nil
	}
	details, _ := json.Marshal(map[string]string{
		"report_date": run.ReportDate.Time.Format("2006-01-02"),
		"format":      run.Format,
		"error_code":  code,
	})
	if _, err := qtx.CreateInboxItem(ctx, db.CreateInboxItemParams{
		WorkspaceID:   run.WorkspaceID,
		RecipientType: "member",
		RecipientID:   run.UserID,
		Type:          "daily_report",
		Severity:      "attention",
		IssueID:       run.IssueID,
		Title:         "Tagesbericht nicht erstellt · " + run.ReportDate.Time.Format("2006-01-02"),
		Body:          pgtype.Text{String: "Der ausgewählte Agent konnte den Tagesbericht nicht fertigstellen. Öffne den Auftrag in Multica, um den aktuellen Stand zu sehen.", Valid: true},
		ActorType:     pgtype.Text{String: "system", Valid: true},
		Details:       details,
	}); err != nil {
		return fmt.Errorf("create daily report failure inbox item: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit daily report failure: %w", err)
	}
	return nil
}

func buildDailyReportPrompt(workspaceID, userID pgtype.UUID, workspaceSlug string, date time.Time, timezone, format, instructions, previousReport string) string {
	dateText := date.Format("2006-01-02")
	var out strings.Builder
	fmt.Fprintf(&out, "%s\n\n", dailyReportIssueMarker(workspaceID, userID, date))
	fmt.Fprintf(&out, "Erstelle den Tagesbericht für den Multica-Workspace %s am %s (Zeitzone %s).\n\n", workspaceSlug, dateText, timezone)
	if previousReport != "" {
		out.WriteString("## Zuerst lesen: Bericht des Vortages\n")
		out.WriteString("Lies den folgenden Bericht vollständig, bevor du den aktuellen Workspace untersuchst. Nutze ihn nur, um Verlauf und offene Punkte zu verstehen. Prüfe jede Aussage anhand des aktuellen Projektstands; erledigte Punkte werden abgeschlossen, offene Punkte nur bei aktueller Bestätigung fortgeführt, Überholtes wird korrigiert oder weggelassen.\n\n")
		out.WriteString(previousReport)
		out.WriteString("\n\n")
	} else {
		out.WriteString("Dies ist der erste Bericht ohne Vortagesbericht. Baue den Ausgangskontext aus dem Workspace-/Projektkontext und dem aktuellen Issue- und Aktivitätsstand auf.\n\n")
	}
	out.WriteString("## Aktuellen Stand ermitteln\n")
	out.WriteString("Lies die aktuellen Issues, relevanten Kommentare und Agentenläufe dieses Workspaces. Ignoriere andere Tagesbericht-Aufträge. Erfinde keine Ergebnisse und kennzeichne Unklarheiten. Beschreibe den Zweck des Projekts so, dass auch eine Person ohne tägliche Mitarbeit den Bericht versteht. Trenne Erledigtes, Laufendes, Blocker und nächste Schritte. Nenne Issue-Kennungen oder Links, wenn sie die Aussage belegen. Kopiere weder den Aktivitätsverlauf noch den vorherigen Bericht ungefiltert.\n\n")
	if format == "dev" {
		out.WriteString("## Stil: Dev-Bericht für IT-Projekte\nSchreibe präzise und fachlich-technisch. Gliedere in Überblick, technische Änderungen, Prüfungen/Belege, Risiken/Abhängigkeiten, Blocker und nächste Schritte. Erkläre bei jeder Änderung kurz, was sie im Projekt bewirkt.\n\n")
	} else {
		out.WriteString("## Stil: Standard-Bericht für allgemeine Vorgänge\nSchreibe in normaler, gut lesbarer Sprache. Beginne mit Projektzweck und aktuellem Stand. Erkläre, was sich geändert hat und warum es relevant ist. Trenne danach Erledigtes, Laufendes, offene Punkte und nächste Schritte. Vermeide unnötige Fachsprache.\n\n")
	}
	if instructions != "" {
		out.WriteString("## Zusätzliche Vorgaben des Empfängers\n")
		out.WriteString(instructions)
		out.WriteString("\n\n")
	}
	fmt.Fprintf(&out, "Berücksichtige nur Inhalte, die in diesem Workspace für den Empfänger sichtbar sind. Antworte mit genau einem abschließenden Kommentar auf diesem Issue. Beginne den Kommentar mit %s. Versende selbst keine E-Mail und ändere keine fremden Issues.", notificationReportBodyMarker)
	return out.String()
}

func dailyReportIssueMarker(workspaceID, userID pgtype.UUID, date time.Time) string {
	return notificationDailyReportMarker + " " + util.UUIDToString(workspaceID) + "/" + util.UUIDToString(userID) + "/" + date.Format("2006-01-02")
}

func normalizeDailyReportFormat(value string) string {
	if value == "dev" {
		return "dev"
	}
	return "standard"
}

func isTerminalTaskStatus(status string) bool {
	switch status {
	case "completed", "failed", "cancelled", "canceled":
		return true
	default:
		return false
	}
}
