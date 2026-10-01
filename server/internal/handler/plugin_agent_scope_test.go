package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/plugincontract"
)

// An agent hook is bound to what the agent's task is about. The model chooses the
// tool input, so the only thing standing between "the project this task belongs
// to" and "any project the plugin's own policy happens to list" is this scope.
func TestAgentHookScopeFollowsTheTaskIssue(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	project := dbfx.Project(t, "Agent scope project")
	inProject := dbfx.Issue(t, "Agent scope issue in project", testutil.Cols{"project_id": project})
	bare := dbfx.Issue(t, "Agent scope issue without project")
	foreignWorkspace := dbfx.Workspace(t, "Agent scope foreign", "agent-scope-foreign")
	foreignIssue := dbfx.Issue(t, "Agent scope foreign issue", testutil.Cols{"workspace_id": foreignWorkspace})

	cases := []struct {
		name  string
		task  db.AgentTaskQueue
		want  service.AgentHookScope
		fails bool
	}{
		{"issue in a project binds to the project", db.AgentTaskQueue{IssueID: parseUUID(inProject)},
			service.AgentHookScope{ProjectID: parseUUID(project)}, false},
		{"issue without a project binds to the issue", db.AgentTaskQueue{IssueID: parseUUID(bare)},
			service.AgentHookScope{IssueID: parseUUID(bare)}, false},
		{"task without an issue stays unscoped", db.AgentTaskQueue{}, service.AgentHookScope{}, false},
		{"issue outside the task workspace is refused", db.AgentTaskQueue{IssueID: parseUUID(foreignIssue)},
			service.AgentHookScope{}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := testHandler.agentHookScope(t.Context(), tc.task, testWorkspaceID)
			if tc.fails {
				if err == nil {
					t.Fatalf("expected an error, got scope %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("agentHookScope: %v", err)
			}
			if got != tc.want {
				t.Fatalf("scope = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// What the scope buys, seen from the plugin's side of the callback API: inside
// the task's project everything the plugin was consented to still works, outside
// it the same token gets nothing — and the workspace context stays readable (the
// plugin's own grant decides) but never writable.
func TestAgentBoundCallbackStaysInsideTheTaskProject(t *testing.T) {
	id := installPluginForAction(t, []string{"projects:read", "workspace:read", "workspace:write", "issues:read"})
	withCallbackTokens(t)
	project := dbfx.Project(t, "Agent bound project")
	other := dbfx.Project(t, "Agent other project")
	inside := dbfx.Issue(t, "Agent bound inside", testutil.Cols{"project_id": project})
	outside := dbfx.Issue(t, "Agent bound outside", testutil.Cols{"project_id": other})

	token := agentCallbackToken(t, id, service.AgentHookScope{ProjectID: parseUUID(project)})

	status := func(call func(w *httptest.ResponseRecorder)) int {
		w := httptest.NewRecorder()
		call(w)
		return w.Code
	}
	readProject := func(projectID string) int {
		return status(func(w *httptest.ResponseRecorder) {
			testHandler.GetPluginProjectContext(w, callbackRequest(token, http.MethodGet, "/projects/x/context", nil,
				map[string]string{"project_id": projectID}))
		})
	}
	readIssue := func(issueID string) int {
		return status(func(w *httptest.ResponseRecorder) {
			testHandler.GetPluginIssue(w, callbackRequest(token, http.MethodGet, "/v1/issues/"+issueID, nil,
				map[string]string{"issue_ref": issueID}))
		})
	}

	for name, check := range map[string]struct{ got, want int }{
		"own project":      {readProject(project), http.StatusOK},
		"other project":    {readProject(other), http.StatusNotFound},
		"issue in project": {readIssue(inside), http.StatusOK},
		"issue elsewhere":  {readIssue(outside), http.StatusNotFound},
		"workspace context": {status(func(w *httptest.ResponseRecorder) {
			testHandler.GetPluginWorkspaceContext(w, callbackRequest(token, http.MethodGet, "/workspace/context", nil, nil))
		}), http.StatusOK},
		"workspace write": {status(func(w *httptest.ResponseRecorder) {
			testHandler.PatchPluginWorkspaceContext(w, callbackRequest(token, http.MethodPatch, "/workspace/context",
				map[string]any{"expected_content": "", "content": "x"}, nil))
		}), http.StatusForbidden},
	} {
		if check.got != check.want {
			t.Errorf("%s: status %d, want %d", name, check.got, check.want)
		}
	}
}

// A task whose issue has no project can only reach that one issue.
func TestAgentBoundCallbackWithoutProjectReachesOnlyItsIssue(t *testing.T) {
	id := installPluginForAction(t, []string{"projects:read", "workspace:read", "issues:read"})
	withCallbackTokens(t)
	own := dbfx.Issue(t, "Agent bound own issue")
	sibling := dbfx.Issue(t, "Agent bound sibling issue")
	project := dbfx.Project(t, "Agent bound foreign project")

	token := agentCallbackToken(t, id, service.AgentHookScope{IssueID: parseUUID(own)})

	for name, check := range map[string]struct {
		issue string
		want  int
	}{"own issue": {own, http.StatusOK}, "sibling issue": {sibling, http.StatusNotFound}} {
		w := httptest.NewRecorder()
		testHandler.GetPluginIssue(w, callbackRequest(token, http.MethodGet, "/v1/issues/"+check.issue, nil,
			map[string]string{"issue_ref": check.issue}))
		if w.Code != check.want {
			t.Errorf("%s: status %d, want %d", name, w.Code, check.want)
		}
	}
	w := httptest.NewRecorder()
	testHandler.GetPluginProjectContext(w, callbackRequest(token, http.MethodGet, "/projects/x/context", nil,
		map[string]string{"project_id": project}))
	if w.Code != http.StatusNotFound {
		t.Errorf("project context: status %d, want 404", w.Code)
	}
}

func agentCallbackToken(t *testing.T, installationID string, scope service.AgentHookScope) string {
	t.Helper()
	installation, err := testHandler.PluginService.InstallationForWorkspace(t.Context(), parseUUID(testWorkspaceID), installationID)
	if err != nil {
		t.Fatalf("load installation: %v", err)
	}
	token, err := testHandler.PluginService.Callbacks.Issue(t.Context(), service.HookInvocation{
		Installation: installation,
		Hook:         plugincontract.Hook{Key: "summarize"},
		Trigger:      plugincontract.TriggerAgent,
		Actor:        service.HookActor{Type: "agent", ID: parseUUID(testUserID)},
		IssueID:      scope.IssueID,
		ProjectID:    scope.ProjectID,
	})
	if err != nil {
		t.Fatalf("issue callback token: %v", err)
	}
	return token
}
