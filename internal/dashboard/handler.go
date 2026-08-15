package dashboard

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"html/template"
	"net/http"
	"net/url"
	"strings"
)

const cookieName = "executor_local"

type Snapshot struct {
	State   string `json:"state"`
	Domain  string `json:"domain"`
	MCPURL  string `json:"mcp_url"`
	Agent   string `json:"agent"`
	Broker  string `json:"broker"`
	Desktop string `json:"desktop"`
	Tunnel  string `json:"tunnel"`
}

type Controller interface {
	Snapshot(context.Context) (Snapshot, error)
	Kill(context.Context) error
	Resume(context.Context) error
	Rotate(context.Context) error
}

type handler struct {
	controller Controller
	token      string
}

func NewHandler(controller Controller, token string) http.Handler {
	return &handler{controller: controller, token: token}
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	setSecurityHeaders(w)
	if token := r.URL.Query().Get("token"); token != "" {
		if !same(token, h.token) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: cookieName, Value: h.token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if !h.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method == http.MethodPost && !sameOrigin(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/":
		h.render(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/api/status":
		h.status(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/api/kill":
		h.action(w, r, h.controller.Kill)
	case r.Method == http.MethodPost && r.URL.Path == "/api/resume":
		h.action(w, r, h.controller.Resume)
	case r.Method == http.MethodPost && r.URL.Path == "/api/rotate":
		h.action(w, r, h.controller.Rotate)
	default:
		http.NotFound(w, r)
	}
}

func (h *handler) authorized(r *http.Request) bool {
	cookie, err := r.Cookie(cookieName)
	return err == nil && same(cookie.Value, h.token)
}

func same(a, b string) bool {
	return len(a) == len(b) && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false
	}
	parsed, err := url.Parse(origin)
	return err == nil && strings.EqualFold(parsed.Host, r.Host) && (parsed.Scheme == "http" || parsed.Scheme == "https")
}

func setSecurityHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'; img-src 'self' data:; frame-ancestors 'none'; base-uri 'none'")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "no-store")
}

func (h *handler) render(w http.ResponseWriter, r *http.Request) {
	snapshot, err := h.controller.Snapshot(r.Context())
	if err != nil {
		http.Error(w, "status unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = pageTemplate.Execute(w, snapshot)
}

func (h *handler) status(w http.ResponseWriter, r *http.Request) {
	snapshot, err := h.controller.Snapshot(r.Context())
	if err != nil {
		http.Error(w, "status unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(snapshot)
}

func (h *handler) action(w http.ResponseWriter, r *http.Request, action func(context.Context) error) {
	if err := action(r.Context()); err != nil {
		http.Error(w, "action failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

var pageTemplate = template.Must(template.New("page").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Executor Control Room</title><style>
:root{--ink:#11120f;--paper:#e8e2d5;--acid:#d7ff45;--alarm:#ff4d2e;--muted:#77766d;--line:#292a24}*{box-sizing:border-box}
body{margin:0;background:var(--ink);color:var(--paper);font-family:"Avenir Next Condensed","Franklin Gothic Condensed",sans-serif;min-height:100vh;background-image:linear-gradient(rgba(255,255,255,.025) 1px,transparent 1px),linear-gradient(90deg,rgba(255,255,255,.025) 1px,transparent 1px);background-size:28px 28px}
main{max-width:1180px;margin:auto;padding:44px 28px 72px}.mast{display:flex;justify-content:space-between;align-items:end;border-bottom:2px solid var(--paper);padding-bottom:18px}.brand{font-size:clamp(54px,9vw,118px);font-weight:900;line-height:.78;letter-spacing:-.055em;text-transform:uppercase}.tag{font:600 12px ui-monospace,monospace;color:var(--acid);letter-spacing:.18em;text-transform:uppercase}.state{border:1px solid var(--acid);padding:10px 14px;color:var(--acid);font:700 13px ui-monospace,monospace;text-transform:uppercase}
.grid{display:grid;grid-template-columns:1.35fr .65fr;gap:18px;margin-top:24px}.panel{border:1px solid var(--line);background:rgba(17,18,15,.88);padding:22px}.kicker{font:700 11px ui-monospace,monospace;letter-spacing:.15em;color:var(--muted);text-transform:uppercase;margin-bottom:18px}.endpoint{font:600 clamp(18px,3vw,34px) ui-monospace,monospace;overflow-wrap:anywhere;color:var(--acid)}
.services{display:grid;grid-template-columns:repeat(2,1fr);gap:1px;background:var(--line);border:1px solid var(--line)}.service{background:var(--ink);padding:16px}.service b{display:block;font:800 14px ui-monospace,monospace;text-transform:uppercase}.service span{color:var(--acid);font-size:12px}
.danger{border-color:var(--alarm);display:flex;flex-direction:column;justify-content:space-between}.danger h2{font-size:40px;line-height:.9;text-transform:uppercase;margin:0;letter-spacing:-.03em}.danger p{color:#b7b3a9;line-height:1.5}.actions{display:flex;gap:10px;flex-wrap:wrap}button{border:1px solid var(--paper);background:transparent;color:var(--paper);padding:12px 16px;font:800 12px ui-monospace,monospace;text-transform:uppercase;cursor:pointer}button:hover{background:var(--paper);color:var(--ink)}button.kill{border-color:var(--alarm);background:var(--alarm);color:var(--ink);flex:1}button.kill:hover{filter:brightness(1.15)}
.foot{margin-top:22px;font:11px ui-monospace,monospace;color:var(--muted);display:flex;justify-content:space-between}@media(max-width:760px){.grid{grid-template-columns:1fr}.mast{align-items:start;gap:20px;flex-direction:column}.services{grid-template-columns:1fr}}
</style></head><body><main><header class="mast"><div><div class="tag">Sovereign machine control</div><div class="brand">Executor</div></div><div class="state">● {{.State}}</div></header>
<section class="grid"><div class="panel"><div class="kicker">Public MCP endpoint</div><div class="endpoint">{{.MCPURL}}</div><div class="kicker" style="margin-top:28px">Subsystem telemetry</div><div class="services"><div class="service"><b>Agent</b><span>{{.Agent}}</span></div><div class="service"><b>Broker</b><span>{{.Broker}}</span></div><div class="service"><b>Desktop</b><span>{{.Desktop}}</span></div><div class="service"><b>Tunnel</b><span>{{.Tunnel}}</span></div></div></div>
<aside class="panel danger"><div><div class="kicker">Emergency control</div><h2 aria-label="Kill Switch">Kill<br>Switch</h2><p>Terminates sessions, disconnects the tunnel, revokes tokens, and rotates credentials.</p></div><div class="actions"><button onclick="act('resume')">Resume</button><button onclick="act('rotate')">Rotate</button><button class="kill" onclick="act('kill')">Kill now</button></div></aside></section><div class="foot"><span>{{.Domain}}</span><span>LOCAL CONSOLE // 127.0.0.1</span></div></main>
<script>async function act(name){if(name==='kill'&&!confirm('Kill Executor and revoke every active credential?'))return;const r=await fetch('/api/'+name,{method:'POST',headers:{'Content-Type':'application/json'}});if(!r.ok)alert('Action failed');else location.reload()}</script></body></html>`))
