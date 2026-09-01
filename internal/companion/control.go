package companion

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

// ControlServer exposes a private loopback dashboard and lifecycle API for a
// running Companion. The view URL contains an unguessable local capability.
type ControlServer struct {
	State    RuntimeState
	server   *http.Server
	listener net.Listener
}

func StartControl(profile string, snapshot func() any, stop func()) (*ControlServer, error) {
	if !validProfileName(profile) {
		return nil, fmt.Errorf("invalid companion profile %q", profile)
	}
	if snapshot == nil || stop == nil {
		return nil, fmt.Errorf("Companion control callbacks are required")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	controlToken, err := randomToken(32)
	if err != nil {
		listener.Close()
		return nil, err
	}
	viewToken, err := randomToken(24)
	if err != nil {
		listener.Close()
		return nil, err
	}
	control := &ControlServer{
		State: RuntimeState{
			Profile: profile, Address: listener.Addr().String(), ControlToken: controlToken,
			ViewToken: viewToken, PID: os.Getpid(), StartedAt: time.Now().UTC(),
		},
		listener: listener,
	}
	prefix := "/view/" + viewToken
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/healthz", control.requireControl(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	mux.HandleFunc("/v1/status", control.requireControl(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, snapshot())
	}))
	mux.HandleFunc("/v1/control/stop", control.requireControl(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, map[string]bool{"stopping": true})
		go stop()
	}))
	viewHandler := func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == prefix || r.URL.Path == prefix+"/" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; base-uri 'none'")
			_, _ = w.Write([]byte(dashboardHTML))
			return
		}
		if r.URL.Path == prefix+"/api/status" && r.Method == http.MethodGet {
			writeJSON(w, snapshot())
			return
		}
		if r.URL.Path == prefix+"/api/stop" && r.Method == http.MethodPost {
			writeJSON(w, map[string]bool{"stopping": true})
			go stop()
			return
		}
		http.NotFound(w, r)
	}
	mux.HandleFunc(prefix, viewHandler)
	mux.HandleFunc(prefix+"/", viewHandler)
	control.server = &http.Server{
		Handler:           noStore(mux),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      5 * time.Second,
	}
	if err := SaveRuntime(control.State); err != nil {
		listener.Close()
		return nil, err
	}
	go func() { _ = control.server.Serve(listener) }()
	return control, nil
}

func (c *ControlServer) Close() error {
	if c.server == nil {
		return nil
	}
	err := c.server.Shutdown(context.Background())
	RemoveRuntime(c.State)
	return err
}

func (c *ControlServer) requireControl(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Fields(r.Header.Get("Authorization"))
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || subtle.ConstantTimeCompare([]byte(parts[1]), []byte(c.State.ControlToken)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func randomToken(size int) (string, error) {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(value)
}

func noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

const dashboardHTML = `<!doctype html>
<html lang="pt-BR"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>AgentClip Companion</title><style>
body{font:16px -apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;background:#101214;color:#f3f4f6;margin:0;padding:32px;max-width:760px}h1{margin:0 0 4px}.muted{color:#a8b0bb}.card{background:#1b1f24;border:1px solid #30363d;border-radius:12px;padding:20px;margin-top:20px}.row{display:flex;justify-content:space-between;gap:24px;padding:9px 0;border-bottom:1px solid #30363d}.row:last-child{border:0}.ok{color:#5eead4}.bad{color:#fca5a5}button{background:#dc2626;color:#fff;border:0;border-radius:8px;padding:10px 14px;font:inherit;cursor:pointer}code{font-family:ui-monospace,SFMono-Regular,monospace}</style>
<h1>AgentClip Companion</h1><p class="muted" id="updated">Carregando…</p><section class="card" id="status"></section><section class="card"><button id="stop">Parar Companion</button></section>
<script>
const base=location.pathname.replace(/\/$/,"");
const esc=v=>String(v??"—").replace(/[&<>]/g,c=>({"&":"&amp;","<":"&lt;",">":"&gt;"}[c]));
function row(k,v){return '<div class="row"><span class="muted">'+k+'</span><strong>'+v+'</strong></div>'}
async function refresh(){try{const r=await fetch(base+"/api/status",{cache:"no-store"});const s=await r.json();const tunnel=s.tunnel?.connected?'<span class="ok">Conectado</span>':'<span class="bad">Desconectado</span>';const items=s.clipboard?.items||[];const clip=s.clipboard?.armed?(items.length?'<span class="ok">'+items.length+' item(ns): '+esc(items.map(i=>i.name||i.kind).join(', '))+'</span>':'<span class="muted">Sem itens</span>'):'<span class="muted">Sem clipboard armado</span>';document.querySelector('#status').innerHTML=row('Perfil',esc(s.profile))+row('Servidor','<code>'+esc(s.destination)+'</code>')+row('Túnel',tunnel)+row('Clipboard',clip)+row('Expira',esc(s.clipboard?.expires_at||'—'))+(s.tunnel?.last_error?row('Último erro','<span class="bad">'+esc(s.tunnel.last_error)+'</span>'):'');document.querySelector('#updated').textContent='Atualizado agora'}catch(e){document.querySelector('#updated').textContent='Não foi possível consultar o Companion'}}
document.querySelector('#stop').onclick=async()=>{if(confirm('Parar o Companion?')){await fetch(base+'/api/stop',{method:'POST'});setTimeout(refresh,500)}};refresh();setInterval(refresh,2000);
</script></html>`
