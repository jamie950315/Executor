package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	permissionmodel "github.com/jamie950315/executor/internal/permissions"
)

type fakeController struct {
	killed             bool
	resumed            bool
	rotated            bool
	permissionRequests []bool
	killErr            error
}

func (f *fakeController) Snapshot(context.Context) (Snapshot, error) {
	return Snapshot{State: "armed", Domain: "executor.example.com", MCPURL: "https://executor.example.com/mcp", Agent: "running", Broker: "running", Desktop: "available", Tunnel: "connected"}, nil
}
func (f *fakeController) Kill(context.Context) (KillResult, error) {
	f.killed = true
	return KillResult{RecoveryKey: "new-recovery-key", URLSecret: "new-url-secret", Dashboard: "http://127.0.0.1:8788/?token=new-dashboard-key"}, f.killErr
}
func (f *fakeController) Resume(context.Context) error { f.resumed = true; return nil }
func (f *fakeController) Rotate(context.Context) error { f.rotated = true; return nil }
func (f *fakeController) Permissions(_ context.Context, request bool) (permissionmodel.Report, error) {
	f.permissionRequests = append(f.permissionRequests, request)
	return permissionmodel.NewReport("darwin", request, []permissionmodel.Item{{
		ID: "screen_recording", Label: "Screen Recording", State: permissionmodel.StatePending, Required: true,
	}}), nil
}

func TestDashboardRequiresBootstrapTokenThenUsesStrictCookie(t *testing.T) {
	controller := &fakeController{}
	h := NewHandler(controller, "local-secret")

	unauthorized := httptest.NewRecorder()
	h.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", unauthorized.Code)
	}

	bootstrap := httptest.NewRecorder()
	h.ServeHTTP(bootstrap, httptest.NewRequest(http.MethodGet, "/?token=local-secret", nil))
	if bootstrap.Code != http.StatusSeeOther {
		t.Fatalf("bootstrap status = %d, want 303", bootstrap.Code)
	}
	cookies := bootstrap.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatalf("unexpected cookie: %#v", cookies)
	}

	pageReq := httptest.NewRequest(http.MethodGet, "/", nil)
	pageReq.AddCookie(cookies[0])
	page := httptest.NewRecorder()
	h.ServeHTTP(page, pageReq)
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "Executor") || !strings.Contains(page.Body.String(), "Kill Switch") || !strings.Contains(page.Body.String(), "Permission Setup") {
		t.Fatalf("unexpected page: status=%d body=%q", page.Code, page.Body.String())
	}
	if strings.Contains(page.Body.String(), "local-secret") || strings.Contains(page.Body.String(), "new-recovery-key") || strings.Contains(page.Body.String(), "new-url-secret") {
		t.Fatal("dashboard page exposed secret material before an action")
	}
	if got := page.Header().Get("Content-Security-Policy"); got == "" {
		t.Fatal("missing Content-Security-Policy")
	}
}

func TestDashboardPermissionStatusAndRequestAllUseAuthenticatedDesktopFlow(t *testing.T) {
	t.Parallel()

	controller := &fakeController{}
	h := NewHandler(controller, "local-secret")

	statusRequest := httptest.NewRequest(http.MethodGet, "/api/permissions/status", nil)
	statusRequest.AddCookie(&http.Cookie{Name: cookieName, Value: "local-secret"})
	statusResponse := httptest.NewRecorder()
	h.ServeHTTP(statusResponse, statusRequest)
	if statusResponse.Code != http.StatusOK {
		t.Fatalf("permission status = %d, want 200", statusResponse.Code)
	}
	var statusReport permissionmodel.Report
	if err := json.NewDecoder(statusResponse.Body).Decode(&statusReport); err != nil {
		t.Fatalf("decode permission status: %v", err)
	}
	if statusReport.Platform != "darwin" || statusReport.Requested || statusReport.Ready {
		t.Fatalf("permission status report = %#v", statusReport)
	}

	requestAll := httptest.NewRequest(http.MethodPost, "/api/permissions/request-all", nil)
	requestAll.Host = "127.0.0.1:8788"
	requestAll.AddCookie(&http.Cookie{Name: cookieName, Value: "local-secret"})
	requestAll.Header.Set("Origin", "http://127.0.0.1:8788")
	requestResponse := httptest.NewRecorder()
	h.ServeHTTP(requestResponse, requestAll)
	if requestResponse.Code != http.StatusOK {
		t.Fatalf("permission request-all = %d, want 200", requestResponse.Code)
	}
	var requestReport permissionmodel.Report
	if err := json.NewDecoder(requestResponse.Body).Decode(&requestReport); err != nil {
		t.Fatalf("decode permission request-all: %v", err)
	}
	if !requestReport.Requested || requestReport.Ready {
		t.Fatalf("permission request-all report = %#v", requestReport)
	}
	if len(controller.permissionRequests) != 2 || controller.permissionRequests[0] || !controller.permissionRequests[1] {
		t.Fatalf("permission request flags = %#v", controller.permissionRequests)
	}
}

func TestDashboardPermissionPanelExposesDetailsSettingsAndRestartGuidance(t *testing.T) {
	t.Parallel()

	h := NewHandler(&fakeController{}, "local-secret")
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.AddCookie(&http.Cookie{Name: cookieName, Value: "local-secret"})
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	body := response.Body.String()
	for _, visibleContract := range []string{"aria-live=\"polite\"", "Open settings", "restart the Executor Desktop helper", "Dependencies changed — restart"} {
		if !strings.Contains(body, visibleContract) {
			t.Fatalf("permission panel missing %q", visibleContract)
		}
	}
}

func TestDashboardPermissionRequestAllRejectsCrossOrigin(t *testing.T) {
	t.Parallel()

	controller := &fakeController{}
	h := NewHandler(controller, "local-secret")
	request := httptest.NewRequest(http.MethodPost, "/api/permissions/request-all", nil)
	request.AddCookie(&http.Cookie{Name: cookieName, Value: "local-secret"})
	request.Header.Set("Origin", "https://attacker.example")
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || len(controller.permissionRequests) != 0 {
		t.Fatalf("cross-origin permission request status=%d calls=%#v", response.Code, controller.permissionRequests)
	}
}

func TestDashboardHealthRequiresCurrentKeyAndReturnsIdentityMarker(t *testing.T) {
	h := NewHandler(&fakeController{}, "local-secret")

	unauthorized := httptest.NewRecorder()
	h.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/.executor/health", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized health status = %d, want 401", unauthorized.Code)
	}

	request := httptest.NewRequest(http.MethodGet, "/.executor/health", nil)
	request.Header.Set("X-Executor-Health-Key", "local-secret")
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent || response.Header().Get("X-Executor-Health") != "ok" {
		t.Fatalf("authenticated health response = status %d marker %q", response.Code, response.Header().Get("X-Executor-Health"))
	}
}

func TestDashboardTokenProviderImmediatelyTracksExternalRotation(t *testing.T) {
	controller := &fakeController{}
	current := "old-local-secret"
	h := NewHandlerWithTokenProvider(controller, func() (string, error) { return current, nil })

	bootstrap := httptest.NewRecorder()
	h.ServeHTTP(bootstrap, httptest.NewRequest(http.MethodGet, "/?token=old-local-secret", nil))
	if bootstrap.Code != http.StatusSeeOther {
		t.Fatalf("initial bootstrap status = %d", bootstrap.Code)
	}
	oldCookie := bootstrap.Result().Cookies()[0]

	current = "new-local-secret"
	oldRequest := httptest.NewRequest(http.MethodGet, "/", nil)
	oldRequest.AddCookie(oldCookie)
	oldResponse := httptest.NewRecorder()
	h.ServeHTTP(oldResponse, oldRequest)
	if oldResponse.Code != http.StatusUnauthorized {
		t.Fatalf("old cookie status after rotation = %d, want 401", oldResponse.Code)
	}

	newBootstrap := httptest.NewRecorder()
	h.ServeHTTP(newBootstrap, httptest.NewRequest(http.MethodGet, "/?token=new-local-secret", nil))
	if newBootstrap.Code != http.StatusSeeOther {
		t.Fatalf("new bootstrap status after rotation = %d, want 303", newBootstrap.Code)
	}
}

func TestDashboardRejectsCrossOriginMutation(t *testing.T) {
	controller := &fakeController{}
	h := NewHandler(controller, "local-secret")
	req := httptest.NewRequest(http.MethodPost, "/api/kill", nil)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: "local-secret"})
	req.Header.Set("Origin", "https://attacker.example")
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)
	if res.Code != http.StatusForbidden || controller.killed {
		t.Fatalf("cross-origin request status=%d killed=%v", res.Code, controller.killed)
	}
}

func TestDashboardSameOriginKillReturnsOneTimeMaterial(t *testing.T) {
	controller := &fakeController{}
	h := NewHandler(controller, "local-secret")
	req := httptest.NewRequest(http.MethodPost, "/api/kill", nil)
	req.Host = "127.0.0.1:8788"
	req.AddCookie(&http.Cookie{Name: cookieName, Value: "local-secret"})
	req.Header.Set("Origin", "http://127.0.0.1:8788")
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)
	if res.Code != http.StatusOK || !controller.killed {
		t.Fatalf("same-origin request status=%d killed=%v", res.Code, controller.killed)
	}
	if got := res.Body.String(); !strings.Contains(got, "new-recovery-key") || !strings.Contains(got, "new-url-secret") || !strings.Contains(got, "new-dashboard-key") {
		t.Fatalf("Kill response lost new one-time material: %q", got)
	}
}

func TestDashboardStatusDoesNotExposeBootstrapOrRecoveryMaterial(t *testing.T) {
	controller := &fakeController{}
	h := NewHandler(controller, "local-secret")
	req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: "local-secret"})
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.Code)
	}
	if got := res.Body.String(); strings.Contains(got, "local-secret") || strings.Contains(got, "new-recovery-key") || strings.Contains(got, "new-url-secret") {
		t.Fatalf("status exposed secret material: %q", got)
	}
}

func TestDashboardKillReturnsRecoveryMaterialAfterPartialControlFailure(t *testing.T) {
	controller := &fakeController{killErr: errors.New("agent stop failed")}
	h := NewHandler(controller, "local-secret")
	req := httptest.NewRequest(http.MethodPost, "/api/kill", nil)
	req.Host = "127.0.0.1:8788"
	req.AddCookie(&http.Cookie{Name: cookieName, Value: "local-secret"})
	req.Header.Set("Origin", "http://127.0.0.1:8788")
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)
	if res.Code != http.StatusMultiStatus {
		t.Fatalf("partial Kill status = %d, want %d", res.Code, http.StatusMultiStatus)
	}
	if got := res.Body.String(); !strings.Contains(got, "new-recovery-key") || !strings.Contains(got, "new-url-secret") || !strings.Contains(got, "new-dashboard-key") {
		t.Fatalf("partial Kill response lost one-time material: %q", got)
	}
}
