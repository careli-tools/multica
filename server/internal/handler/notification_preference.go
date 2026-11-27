package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/inbox"
	"github.com/multica-ai/multica/server/internal/logger"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// validNotifGroups is the set of notification preference group keys that the
// API accepts. Keys not in this set are rejected. `system_notifications` is
// not an inbox event group — it's a delivery-channel toggle controlling
// whether native OS notification banners fire — but it shares the same
// preferences map so a single endpoint covers all user notification
// preferences. The `auto_archive_*` keys are not event groups either: each
// says whether one action you take on an issue archives your notifications on
// it. Completing and triggering an agent archive unless "muted"; commenting,
// status changes, reassigning, priority, project, date and label edits archive
// only with "all". `squad_handoffs` narrows `comments` to leave out the
// comments a squad's agents hand work to each other with (MUL-10008).
// `after_auto_archive` is a legacy preference kept accepted for older
// clients; current clients always stay on an auto-archived issue.
var validNotifGroups = map[string]bool{
	"assignments":                   true,
	"status_changes":                true,
	"comments":                      true,
	"squad_handoffs":                true,
	"chat_replies":                  true,
	"mentions":                      true,
	"reactions":                     true,
	"updates":                       true,
	"agent_activity":                true,
	"new_issues":                    true,
	"system_notifications":          true,
	"auto_archive_completed":        true,
	"auto_archive_on_agent_trigger": true,
	"auto_archive_on_comment":       true,
	"auto_archive_on_status":        true,
	"auto_archive_on_assignee":      true,
	"auto_archive_on_priority":      true,
	"auto_archive_on_project":       true,
	"auto_archive_on_dates":         true,
	"auto_archive_on_labels":        true,
	"after_auto_archive":            true,
	"others_issues":                 true,
}

var validDeliveryPreferenceKeys = map[string]bool{
	"email_enabled":                true,
	"email_address":                true,
	"telegram_enabled":             true,
	"digest_minutes":               true,
	"daily_report_enabled":         true,
	"daily_report_time":            true,
	"daily_report_timezone":        true,
	"daily_report_agent_id":        true,
	"daily_report_format":          true,
	"daily_report_instructions":    true,
	"channel_event_assigned":       true,
	"channel_event_mentioned":      true,
	"channel_event_mention_all":    true,
	"channel_event_status_changed": true,
	"channel_event_task_failed":    true,
	"channel_event_task_completed": true,
}

// validNotifValues is the set of allowed preference values for a group not
// listed in notifValuesByGroup.
var validNotifValues = map[string]bool{
	"all":   true,
	"muted": true,
}

// notifValuesByGroup lists the groups whose values are not on/off.
// `others_issues` is how much of someone else's issue a passive subscriber
// receives: absent or "key_updates" for mentions, thread replies and handoffs,
// "all" for every event. Muting it would only hide what still needs you.
// `after_auto_archive` is absent or "stay" to keep the archived notification
// open, "next" to open the next one.
var notifValuesByGroup = map[string]map[string]bool{
	"others_issues":      {"all": true, "key_updates": true},
	"after_auto_archive": {"stay": true, "next": true},
}

// autoArchivePromptKey marks a workspace where the member has not yet answered
// the one-time question of which actions archive their notifications. Migration
// 718 writes it for members who archived notifications before replying,
// changing status and reassigning did so by default. It is stored with the
// preferences but answered as the response's own `auto_archive_prompt` flag,
// so no client ever echoes it back in a write.
const autoArchivePromptKey = "auto_archive_prompt"

func validNotifValue(group, value string) bool {
	if values, ok := notifValuesByGroup[group]; ok {
		return values[value]
	}
	return validNotifValues[value]
}

var emailAddressPattern = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)
var reportTimePattern = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)
var uuidPattern = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func validNotificationPreference(key, value string) bool {
	if validNotifGroups[key] {
		return validNotifValue(key, value)
	}
	if !validDeliveryPreferenceKeys[key] {
		return false
	}
	switch key {
	case "email_enabled", "telegram_enabled", "daily_report_enabled",
		"channel_event_assigned", "channel_event_mentioned", "channel_event_mention_all",
		"channel_event_status_changed", "channel_event_task_failed", "channel_event_task_completed":
		return value == "true" || value == "false"
	case "email_address":
		return value == "" || (len(value) <= 320 && emailAddressPattern.MatchString(value))
	case "digest_minutes":
		return value == "15" || value == "30" || value == "60" || value == "240"
	case "daily_report_time":
		return reportTimePattern.MatchString(value)
	case "daily_report_timezone":
		if len(value) > 100 || strings.TrimSpace(value) != value {
			return false
		}
		_, err := time.LoadLocation(value)
		return err == nil
	case "daily_report_agent_id":
		return value == "" || uuidPattern.MatchString(value)
	case "daily_report_format":
		return value == "dev" || value == "standard"
	case "daily_report_instructions":
		return utf8.RuneCountInString(value) <= 4000
	default:
		return false
	}
}

func (h *Handler) GetNotificationPreferences(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := ctxWorkspaceID(r.Context())
	statusRows, statusErr := h.Queries.ListNotificationDeliveryStatus(r.Context(), db.ListNotificationDeliveryStatusParams{
		WorkspaceID: parseUUID(workspaceID), UserID: parseUUID(userID),
	})
	if statusErr != nil {
		slog.Warn("ListNotificationDeliveryStatus failed", append(logger.RequestAttrs(r), "error", statusErr)...)
	}
	deliveryStatus := map[string]any{}
	for _, row := range statusRows {
		deliveryStatus[row.ChannelType] = map[string]any{
			"failed_count": row.FailedCount,
		}
	}
	telegramConnected := h.notificationTelegramConnected(r, workspaceID, userID)

	pref, err := h.Queries.GetNotificationPreference(r.Context(), db.GetNotificationPreferenceParams{
		WorkspaceID: parseUUID(workspaceID),
		UserID:      parseUUID(userID),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusOK, map[string]any{
				"workspace_id":        workspaceID,
				"preferences":         inbox.WithDefaults(nil),
				"auto_archive_prompt": false,
				"telegram_connected":  telegramConnected,
				"delivery_status":     deliveryStatus,
			})
			return
		}
		slog.Warn("GetNotificationPreference failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to get notification preferences")
		return
	}
	writeNotificationPreferenceResponseWithExtras(w, workspaceID, pref, map[string]any{
		"telegram_connected": telegramConnected,
		"delivery_status":    deliveryStatus,
	})
}

func (h *Handler) notificationTelegramConnected(r *http.Request, workspaceID, userID string) bool {
	_, err := h.Queries.FindChannelBindingForMember(r.Context(), db.FindChannelBindingForMemberParams{
		WorkspaceID:   parseUUID(workspaceID),
		MulticaUserID: parseUUID(userID),
		ChannelType:   "telegram",
	})
	return err == nil
}

type updateNotifPrefRequest struct {
	Preferences map[string]string `json:"preferences"`
}

// decodeNotificationPreferenceRequest reads and validates the preferences in
// a write request.
func decodeNotificationPreferenceRequest(w http.ResponseWriter, r *http.Request) (map[string]string, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, 16*1024)
	var req updateNotifPrefRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return nil, false
	}

	if req.Preferences == nil {
		writeError(w, http.StatusBadRequest, "preferences field is required")
		return nil, false
	}

	for k, v := range req.Preferences {
		if k == "email_address" {
			v = strings.ToLower(strings.TrimSpace(v))
			req.Preferences[k] = v
		}
		if !validNotifGroups[k] && !validDeliveryPreferenceKeys[k] {
			writeError(w, http.StatusBadRequest, "invalid preference group: "+k)
			return nil, false
		}
		if !validNotificationPreference(k, v) {
			writeError(w, http.StatusBadRequest, "invalid preference value: "+v)
			return nil, false
		}
	}

	return req.Preferences, true
}

func marshalNotificationPreferences(w http.ResponseWriter, prefs map[string]string) ([]byte, bool) {
	prefsJSON, err := json.Marshal(prefs)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to marshal preferences")
		return nil, false
	}
	return prefsJSON, true
}

func (h *Handler) validateDailyReportAgent(w http.ResponseWriter, r *http.Request, workspaceID, userID string, incoming []byte) bool {
	var patch map[string]string
	if err := json.Unmarshal(incoming, &patch); err != nil {
		writeError(w, http.StatusBadRequest, "invalid preferences")
		return false
	}
	_, reportSettingChanged := patch["daily_report_enabled"]
	_, reportAgentChanged := patch["daily_report_agent_id"]
	_, emailSettingChanged := patch["email_enabled"]
	_, emailAddressChanged := patch["email_address"]
	if !reportSettingChanged && !reportAgentChanged && !emailSettingChanged && !emailAddressChanged {
		return true
	}

	current := map[string]string{}
	pref, err := h.Queries.GetNotificationPreference(r.Context(), db.GetNotificationPreferenceParams{
		WorkspaceID: parseUUID(workspaceID),
		UserID:      parseUUID(userID),
	})
	if err == nil {
		_ = json.Unmarshal(pref.Preferences, &current)
	} else if !errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusInternalServerError, "failed to read notification preferences")
		return false
	}
	for key, value := range patch {
		current[key] = value
	}
	if current["email_enabled"] == "true" && !emailAddressPattern.MatchString(current["email_address"]) {
		writeError(w, http.StatusBadRequest, "add a valid email address before enabling email notifications")
		return false
	}
	if !reportSettingChanged && !reportAgentChanged {
		return true
	}
	if current["daily_report_enabled"] != "true" {
		return true
	}
	if current["daily_report_agent_id"] == "" {
		writeError(w, http.StatusBadRequest, "choose an active agent before enabling the daily report")
		return false
	}
	agentID, ok := parseUUIDOrBadRequest(w, current["daily_report_agent_id"], "daily_report_agent_id")
	if !ok {
		return false
	}
	agent, err := h.Queries.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{
		ID: agentID, WorkspaceID: parseUUID(workspaceID),
	})
	if err != nil || agent.ArchivedAt.Valid {
		writeError(w, http.StatusBadRequest, "daily_report_agent_id must identify an active agent in this workspace")
		return false
	}
	return true
}

func (h *Handler) cancelDisabledNotificationChannels(r *http.Request, workspaceID, userID string, prefsJSON []byte) error {
	var changed map[string]string
	if err := json.Unmarshal(prefsJSON, &changed); err != nil {
		return err
	}
	for key, channelType := range map[string]string{"email_enabled": "email", "telegram_enabled": "telegram"} {
		if changed[key] != "false" {
			continue
		}
		if _, err := h.Queries.CancelPendingNotificationDeliveries(r.Context(), db.CancelPendingNotificationDeliveriesParams{
			WorkspaceID: parseUUID(workspaceID), UserID: parseUUID(userID), ChannelType: channelType,
		}); err != nil {
			return fmt.Errorf("cancel pending %s notifications: %w", channelType, err)
		}
	}
	return nil
}

// writeNotificationPreferenceResponse answers with the stored preferences plus
// the groups muted by default (inbox.WithDefaults), so every client renders
// those switches as off whichever defaults it was built with.
func writeNotificationPreferenceResponse(
	w http.ResponseWriter,
	workspaceID string,
	pref db.NotificationPreference,
) {
	writeNotificationPreferenceResponseWithExtras(w, workspaceID, pref, nil)
}

// writeNotificationPreferenceResponseWithExtras adds the Careli delivery
// fields (telegram_connected, delivery_status) to the GET response.
func writeNotificationPreferenceResponseWithExtras(
	w http.ResponseWriter,
	workspaceID string,
	pref db.NotificationPreference,
	extras map[string]any,
) {
	var prefs map[string]string
	if err := json.Unmarshal(pref.Preferences, &prefs); err != nil {
		prefs = map[string]string{}
	}
	_, prompt := prefs[autoArchivePromptKey]
	delete(prefs, autoArchivePromptKey)

	body := map[string]any{
		"workspace_id":        workspaceID,
		"preferences":         inbox.WithDefaults(prefs),
		"auto_archive_prompt": prompt,
	}
	for k, v := range extras {
		body[k] = v
	}
	writeJSON(w, http.StatusOK, body)
}

// UpdateNotificationPreferences keeps the older PUT endpoint additive (Careli).
// Older clients send only the event-group keys they know; replacing the JSON
// object would silently erase channel and daily-report settings saved by newer
// apps. Groups muted by default that such a client leaves out still mean "on"
// (inbox.WithLegacyDefaults), as upstream's replace-all path stores them.
func (h *Handler) UpdateNotificationPreferences(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := ctxWorkspaceID(r.Context())
	prefs, ok := decodeNotificationPreferenceRequest(w, r)
	if !ok {
		return
	}
	// Only clients built before PATCH existed send this full replacement, and
	// they leave a group out to mean "on".
	prefsJSON, ok := marshalNotificationPreferences(w, inbox.WithLegacyDefaults(prefs))
	if !ok {
		return
	}
	if !h.validateDailyReportAgent(w, r, workspaceID, userID, prefsJSON) {
		return
	}

	pref, err := h.Queries.PatchNotificationPreference(r.Context(), db.PatchNotificationPreferenceParams{
		WorkspaceID: parseUUID(workspaceID),
		UserID:      parseUUID(userID),
		Preferences: prefsJSON,
	})
	if err != nil {
		slog.Warn("UpsertNotificationPreference failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to update notification preferences")
		return
	}
	if err := h.cancelDisabledNotificationChannels(r, workspaceID, userID, prefsJSON); err != nil {
		slog.Warn("cancel disabled notification deliveries failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to update notification delivery state")
		return
	}
	writeNotificationPreferenceResponse(w, workspaceID, pref)
}

// PatchNotificationPreferences atomically merges only the supplied keys. This
// prevents stale tabs or devices from replacing unrelated mute settings.
func (h *Handler) PatchNotificationPreferences(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := ctxWorkspaceID(r.Context())
	prefs, ok := decodeNotificationPreferenceRequest(w, r)
	if !ok {
		return
	}
	prefsJSON, ok := marshalNotificationPreferences(w, prefs)
	if !ok {
		return
	}
	if !h.validateDailyReportAgent(w, r, workspaceID, userID, prefsJSON) {
		return
	}

	pref, err := h.Queries.PatchNotificationPreference(r.Context(), db.PatchNotificationPreferenceParams{
		WorkspaceID: parseUUID(workspaceID),
		UserID:      parseUUID(userID),
		Preferences: prefsJSON,
	})
	if err != nil {
		slog.Warn("PatchNotificationPreference failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to update notification preferences")
		return
	}
	if err := h.cancelDisabledNotificationChannels(r, workspaceID, userID, prefsJSON); err != nil {
		slog.Warn("cancel disabled notification deliveries failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to update notification delivery state")
		return
	}
	writeNotificationPreferenceResponse(w, workspaceID, pref)
}

// ResolveAutoArchivePrompt answers the one-time auto-archive question once for
// all of the caller's workspaces: the supplied `auto_archive_*` values are
// merged into every one of their preference rows still marked with
// auto_archive_prompt, and the marker is dropped. An empty map keeps what each
// workspace has. Answers with the current workspace's preferences.
func (h *Handler) ResolveAutoArchivePrompt(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	prefs, ok := decodeNotificationPreferenceRequest(w, r)
	if !ok {
		return
	}
	for k := range prefs {
		if !strings.HasPrefix(k, "auto_archive_") {
			writeError(w, http.StatusBadRequest, "invalid preference group: "+k)
			return
		}
	}
	prefsJSON, ok := marshalNotificationPreferences(w, prefs)
	if !ok {
		return
	}

	if err := h.Queries.ResolveAutoArchivePrompt(r.Context(), db.ResolveAutoArchivePromptParams{
		UserID:      parseUUID(userID),
		Preferences: prefsJSON,
	}); err != nil {
		slog.Warn("ResolveAutoArchivePrompt failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to update notification preferences")
		return
	}
	h.GetNotificationPreferences(w, r)
}

func (h *Handler) archiveActorInboxOnAgentTrigger(ctx context.Context, workspaceID, issueID, actorType, actorID string) {
	inbox.ArchiveActorIssueInbox(ctx, h.Queries, h.Bus, workspaceID, issueID, actorType, actorID, inbox.AutoArchiveAgentTriggerPref)
}

// issueRunTriggeredAgent reports whether an issue write actually handed work to
// an agent (queued a fresh run or coalesced into a pending one). Deferred
// (backlog parking, suppress_run, self-loop) and blocked outcomes do not count.
func issueRunTriggeredAgent(outcome *IssueRunOutcome) bool {
	return outcome != nil && (outcome.Status == DispatchQueued || outcome.Status == DispatchCoalesced)
}

// commentResultsTriggeredAgent reports whether a comment actually reached at
// least one agent (queued, coalesced into a pending run, deferred to an active
// run's completion reconcile, or steered into a running turn). Blocked targets
// do not count.
func commentResultsTriggeredAgent(results map[string]commentEnqueueResult) bool {
	for _, res := range results {
		switch res.status {
		case DispatchQueued, DispatchCoalesced, DispatchDeferred, DispatchSteered:
			return true
		}
	}
	return false
}

func (h *Handler) CreateDailyReportNow(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	if h.NotificationDelivery == nil {
		writeError(w, http.StatusServiceUnavailable, "daily reports are unavailable")
		return
	}
	result, err := h.NotificationDelivery.StartDailyReportNow(
		r.Context(), parseUUID(ctxWorkspaceID(r.Context())), parseUUID(userID), time.Now(),
	)
	if errors.Is(err, service.ErrDailyReportAlreadyRun) {
		writeError(w, http.StatusConflict, "a daily report already exists for today")
		return
	}
	if errors.Is(err, service.ErrDailyReportNotEnabled) {
		writeError(w, http.StatusConflict, "enable daily reports and choose an active agent first")
		return
	}
	if err != nil {
		slog.Warn("CreateDailyReportNow failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "could not start daily report")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"accepted": true, "report_date": result.ReportDate, "issue_id": result.IssueID,
	})
}
