package dashboard

import (
	"context"
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
func (f *fakeController) Rotate(context.Context) (KillResult, error) {
	f.rotated = true
	return KillResult{RecoveryKey: "rotated-recovery-key", URLSecret: "rotated-url-secret", Dashboard: "http://127.0.0.1:8788/?token=rotated-dashboard-key"}, nil
}
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
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "Executor") || !strings.Contains(page.Body.String(), "Kill Switch") || !strings.Contains(page.Body.String(), "Emergency rescue only") {
		t.Fatalf("unexpected page: status=%d body=%q", page.Code, page.Body.String())
	}
	for _, centralizedPresentation := range []string{"Permission Setup", "Request all", "permission-list", "workspace", "FilesPanel"} {
		if strings.Contains(page.Body.String(), centralizedPresentation) {
			t.Fatalf("localhost rescue page retained centralized presentation %q", centralizedPresentation)
		}
	}
	if strings.Contains(page.Body.String(), "local-secret") || strings.Contains(page.Body.String(), "new-recovery-key") || strings.Contains(page.Body.String(), "new-url-secret") {
		t.Fatal("dashboard page exposed secret material before an action")
	}
	if got := page.Header().Get("Content-Security-Policy"); got == "" {
		t.Fatal("missing Content-Security-Policy")
	}
}

func TestDashboardRescueDoesNotExposePermissionManagementRoutes(t *testing.T) {
	t.Parallel()

	controller := &fakeController{}
	h := NewHandler(controller, "local-secret")

	statusRequest := httptest.NewRequest(http.MethodGet, "/api/permissions/status", nil)
	statusRequest.AddCookie(&http.Cookie{Name: cookieName, Value: "local-secret"})
	statusResponse := httptest.NewRecorder()
	h.ServeHTTP(statusResponse, statusRequest)
	if statusResponse.Code != http.StatusNotFound {
		t.Fatalf("permission status = %d, want 404", statusResponse.Code)
	}

	requestAll := httptest.NewRequest(http.MethodPost, "/api/permissions/request-all", nil)
	requestAll.Host = "127.0.0.1:8788"
	requestAll.AddCookie(&http.Cookie{Name: cookieName, Value: "local-secret"})
	requestAll.Header.Set("Origin", "http://127.0.0.1:8788")
	requestResponse := httptest.NewRecorder()
	h.ServeHTTP(requestResponse, requestAll)
	if requestResponse.Code != http.StatusNotFound {
		t.Fatalf("permission request-all = %d, want 404", requestResponse.Code)
	}
	if len(controller.permissionRequests) != 0 {
		t.Fatalf("rescue page invoked permission management: %#v", controller.permissionRequests)
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

func TestDashboardRelayStatusMachineEndpointIsAuthenticatedAndMetadataOnly(t *testing.T) {
	h := NewHandler(&fakeController{}, "local-secret")

	unauthorized := httptest.NewRecorder()
	h.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/.executor/relay-status", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized relay status = %d, want 401", unauthorized.Code)
	}

	request := httptest.NewRequest(http.MethodGet, "/.executor/relay-status", nil)
	request.Header.Set("X-Executor-Health-Key", "local-secret")
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"state":"disconnected"`) {
		t.Fatalf("relay status = %d %q, want disconnected metadata", response.Code, response.Body.String())
	}
	for _, forbidden := range []string{"grant", "token", "secret", "device_id"} {
		if strings.Contains(strings.ToLower(response.Body.String()), forbidden) {
			t.Fatalf("relay status exposed forbidden material %q: %s", forbidden, response.Body.String())
		}
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

func TestDashboardSameOriginRotateReturnsOneTimeMaterial(t *testing.T) {
	controller := &fakeController{}
	h := NewHandler(controller, "local-secret")
	req := httptest.NewRequest(http.MethodPost, "/api/rotate", nil)
	req.Host = "127.0.0.1:8788"
	req.AddCookie(&http.Cookie{Name: cookieName, Value: "local-secret"})
	req.Header.Set("Origin", "http://127.0.0.1:8788")
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)
	if res.Code != http.StatusOK || !controller.rotated {
		t.Fatalf("same-origin Rotate status=%d rotated=%v", res.Code, controller.rotated)
	}
	if got := res.Body.String(); !strings.Contains(got, "rotated-recovery-key") || !strings.Contains(got, "rotated-url-secret") || !strings.Contains(got, "rotated-dashboard-key") {
		t.Fatalf("Rotate response lost new one-time material: %q", got)
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
