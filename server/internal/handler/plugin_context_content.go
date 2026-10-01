package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/plugincontract"
	"github.com/multica-ai/multica/server/pkg/protocol"
	publicapiv1 "github.com/multica-ai/multica/server/pkg/publicapi/v1"
)

func (h *Handler) pluginProjectForUser(w http.ResponseWriter, r *http.Request, caller service.PluginActionCaller, id string) (db.Project, bool) {
	parsed, err := util.ParseUUID(id)
	if err != nil {
		publicapiv1.WriteProblem(w, r, 400, "invalid_request", "invalid project id")
		return db.Project{}, false
	}
	if caller.IssueScope.Valid || caller.ProjectScope.Valid && caller.ProjectScope != parsed {
		publicapiv1.WriteProblem(w, r, 404, "not_found", "project not found")
		return db.Project{}, false
	}
	project, err := h.Queries.GetProjectInWorkspace(r.Context(), db.GetProjectInWorkspaceParams{ID: parsed, WorkspaceID: caller.WorkspaceID})
	if err != nil {
		publicapiv1.WriteProblem(w, r, 404, "not_found", "project not found")
		return db.Project{}, false
	}
	return project, true
}
func pluginContextCanWrite(actor pluginActor, workspace bool) bool {
	return actor.isMember() && (!workspace || actor.Member.Role == "owner" || actor.Member.Role == "admin")
}
func readPluginContextPatch(w http.ResponseWriter, r *http.Request) (publicapiv1.PatchContextRequest, bool) {
	var req publicapiv1.PatchContextRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil || req.Content == nil || req.ExpectedContent == nil || len(*req.Content) > 100000 || len(*req.ExpectedContent) > 100000 || strings.ContainsRune(*req.Content, 0) {
		publicapiv1.WriteProblem(w, r, 400, "invalid_request", "content and expected_content are required (maximum 100000 bytes)")
		return req, false
	}
	return req, true
}
func pluginContextWriteError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, pgx.ErrNoRows) {
		publicapiv1.WriteProblem(w, r, 409, "context_conflict", "context changed; reload and review a new proposal")
		return
	}
	publicapiv1.WriteProblem(w, r, 500, "internal_error", "context could not be saved")
}
func (h *Handler) GetPluginProjectContext(w http.ResponseWriter, r *http.Request) {
	caller, actor, ok := h.pluginCaller(w, r, plugincontract.ScopeProjectsRead)
	if !ok {
		return
	}
	project, ok := h.pluginProjectForUser(w, r, caller, chi.URLParam(r, "project_id"))
	if !ok {
		return
	}
	writeJSON(w, 200, publicapiv1.ContextContent{ID: uuidToString(project.ID), WorkspaceID: uuidToString(project.WorkspaceID), Title: project.Title, Content: project.Description.String, CanWrite: pluginContextCanWrite(actor, false)})
}
func (h *Handler) PatchPluginProjectContext(w http.ResponseWriter, r *http.Request) {
	caller, actor, ok := h.pluginCaller(w, r, plugincontract.ScopeProjectsWrite)
	if !ok {
		return
	}
	if !actor.requireMember(w, r) {
		return
	}
	project, ok := h.pluginProjectForUser(w, r, caller, chi.URLParam(r, "project_id"))
	if !ok {
		return
	}
	req, ok := readPluginContextPatch(w, r)
	if !ok {
		return
	}
	updated, err := h.Queries.CompareAndSwapProjectContext(r.Context(), db.CompareAndSwapProjectContextParams{ID: project.ID, WorkspaceID: caller.WorkspaceID, ExpectedContent: *req.ExpectedContent, Content: *req.Content})
	if err != nil {
		pluginContextWriteError(w, r, err)
		return
	}
	resp := projectToResponse(updated)
	resp.IssueCount, resp.DoneCount = h.loadProjectIssueStats(r.Context(), caller.WorkspaceID, project.ID)
	resp.ResourceCount = h.loadProjectResourceCount(r.Context(), project.ID)
	h.publish(protocol.EventProjectUpdated, uuidToString(caller.WorkspaceID), "member", uuidToString(actor.Member.UserID), map[string]any{"project": resp, "via_plugin_id": uuidToString(caller.Installation.ID)})
	writeJSON(w, 200, publicapiv1.ContextContent{ID: uuidToString(project.ID), WorkspaceID: uuidToString(caller.WorkspaceID), Title: updated.Title, Content: updated.Description.String, CanWrite: true})
}
func (h *Handler) GetPluginWorkspaceContext(w http.ResponseWriter, r *http.Request) {
	caller, actor, ok := h.pluginCaller(w, r, plugincontract.ScopeWorkspaceRead)
	if !ok {
		return
	}
	if caller.IssueScope.Valid || caller.ProjectScope.Valid {
		publicapiv1.WriteProblem(w, r, 403, "scope_denied", "callback is bound to another resource")
		return
	}
	workspace, err := h.Queries.GetWorkspace(r.Context(), caller.WorkspaceID)
	if err != nil {
		publicapiv1.WriteProblem(w, r, 404, "not_found", "workspace not found")
		return
	}
	writeJSON(w, 200, publicapiv1.ContextContent{ID: uuidToString(workspace.ID), WorkspaceID: uuidToString(workspace.ID), Title: workspace.Name, Content: workspace.Context.String, CanWrite: pluginContextCanWrite(actor, true)})
}
func (h *Handler) PatchPluginWorkspaceContext(w http.ResponseWriter, r *http.Request) {
	caller, actor, ok := h.pluginCaller(w, r, plugincontract.ScopeWorkspaceWrite)
	if !ok {
		return
	}
	if caller.IssueScope.Valid || caller.ProjectScope.Valid || !pluginContextCanWrite(actor, true) {
		publicapiv1.WriteProblem(w, r, 403, "forbidden", "workspace context requires an owner or admin")
		return
	}
	req, ok := readPluginContextPatch(w, r)
	if !ok {
		return
	}
	workspace, err := h.Queries.CompareAndSwapWorkspaceContext(r.Context(), db.CompareAndSwapWorkspaceContextParams{ID: caller.WorkspaceID, ExpectedContent: *req.ExpectedContent, Content: *req.Content})
	if err != nil {
		pluginContextWriteError(w, r, err)
		return
	}
	h.publish(protocol.EventWorkspaceUpdated, uuidToString(workspace.ID), "member", uuidToString(actor.Member.UserID), map[string]any{"workspace": h.workspaceToResponse(workspace), "via_plugin_id": uuidToString(caller.Installation.ID)})
	writeJSON(w, 200, publicapiv1.ContextContent{ID: uuidToString(workspace.ID), WorkspaceID: uuidToString(workspace.ID), Title: workspace.Name, Content: workspace.Context.String, CanWrite: true})
}
func (h *Handler) ListPluginProjectIssues(w http.ResponseWriter, r *http.Request) {
	caller, _, ok := h.pluginCaller(w, r, plugincontract.ScopeIssuesRead)
	if !ok {
		return
	}
	project, ok := h.pluginProjectForUser(w, r, caller, chi.URLParam(r, "project_id"))
	if !ok {
		return
	}
	rows, err := h.Queries.ListPluginProjectIssueChoices(r.Context(), db.ListPluginProjectIssueChoicesParams{WorkspaceID: caller.WorkspaceID, ProjectID: project.ID})
	if err != nil {
		publicapiv1.WriteProblem(w, r, 500, "internal_error", "issues could not be loaded")
		return
	}
	if rows == nil {
		rows = []db.ListPluginProjectIssueChoicesRow{}
	}
	writeJSON(w, 200, map[string]any{"issues": rows, "limit": 200})
}
