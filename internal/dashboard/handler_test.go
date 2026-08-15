package dashboard

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeController struct {
	killed  bool
	resumed bool
	rotated bool
}

func (f *fakeController) Snapshot(context.Context) (Snapshot, error) {
	return Snapshot{State: "armed", Domain: "executor.example.com", MCPURL: "https://executor.example.com/mcp", Agent: "running", Broker: "running", Desktop: "available", Tunnel: "connected"}, nil
}
func (f *fakeController) Kill(context.Context) error   { f.killed = true; return nil }
func (f *fakeController) Resume(context.Context) error { f.resumed = true; return nil }
func (f *fakeController) Rotate(context.Context) error { f.rotated = true; return nil }

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
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "Executor") || !strings.Contains(page.Body.String(), "Kill Switch") {
		t.Fatalf("unexpected page: status=%d body=%q", page.Code, page.Body.String())
	}
	if got := page.Header().Get("Content-Security-Policy"); got == "" {
		t.Fatal("missing Content-Security-Policy")
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

func TestDashboardAllowsSameOriginKill(t *testing.T) {
	controller := &fakeController{}
	h := NewHandler(controller, "local-secret")
	req := httptest.NewRequest(http.MethodPost, "/api/kill", nil)
	req.Host = "127.0.0.1:8788"
	req.AddCookie(&http.Cookie{Name: cookieName, Value: "local-secret"})
	req.Header.Set("Origin", "http://127.0.0.1:8788")
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)
	if res.Code != http.StatusNoContent || !controller.killed {
		t.Fatalf("same-origin request status=%d killed=%v", res.Code, controller.killed)
	}
}
