package handler

import (
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
	"github.com/multica-ai/multica/server/internal/logger"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// validNotifGroups is the set of notification preference group keys that the
// API accepts. Keys not in this set are rejected. `system_notifications` is
// not an inbox event group — it's a delivery-channel toggle controlling
// whether native OS notification banners fire — but it shares the same
// preferences map so a single endpoint covers all user notification
// preferences.
var validNotifGroups = map[string]bool{
	"assignments":          true,
	"status_changes":       true,
	"comments":             true,
	"mentions":             true,
	"updates":              true,
	"agent_activity":       true,
	"system_notifications": true,
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

// validNotifValues is the set of allowed preference values per group.
var validNotifValues = map[string]bool{
	"all":   true,
	"muted": true,
}

var emailAddressPattern = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)
var reportTimePattern = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)
var uuidPattern = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func validNotificationPreference(key, value string) bool {
	if validNotifGroups[key] {
		return validNotifValues[value]
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

	pref, err := h.Queries.GetNotificationPreference(r.Context(), db.GetNotificationPreferenceParams{
		WorkspaceID: parseUUID(workspaceID),
		UserID:      parseUUID(userID),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusOK, map[string]any{
				"workspace_id":       workspaceID,
				"preferences":        map[string]any{},
				"telegram_connected": h.notificationTelegramConnected(r, workspaceID, userID),
				"delivery_status":    deliveryStatus,
			})
			return
		}
		slog.Warn("GetNotificationPreference failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to get notification preferences")
		return
	}

	var prefs map[string]string
	if err := json.Unmarshal(pref.Preferences, &prefs); err != nil {
		prefs = map[string]string{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"workspace_id":       workspaceID,
		"preferences":        prefs,
		"telegram_connected": h.notificationTelegramConnected(r, workspaceID, userID),
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

func decodeNotificationPreferenceRequest(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
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
		if !validNotificationPreference(k, v) {
			writeError(w, http.StatusBadRequest, "invalid preference group: "+k)
			return nil, false
		}
	}

	prefsJSON, err := json.Marshal(req.Preferences)
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

func writeNotificationPreferenceResponse(
	w http.ResponseWriter,
	workspaceID string,
	pref db.NotificationPreference,
) {
	var prefs map[string]string
	if err := json.Unmarshal(pref.Preferences, &prefs); err != nil {
		prefs = map[string]string{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"workspace_id": workspaceID,
		"preferences":  prefs,
	})
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

// UpdateNotificationPreferences keeps the older PUT endpoint additive. Older
// clients send only the event-group keys they know; replacing the JSON object
// would silently erase channel and daily-report settings saved by newer apps.
func (h *Handler) UpdateNotificationPreferences(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := ctxWorkspaceID(r.Context())
	prefsJSON, ok := decodeNotificationPreferenceRequest(w, r)
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
	prefsJSON, ok := decodeNotificationPreferenceRequest(w, r)
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
