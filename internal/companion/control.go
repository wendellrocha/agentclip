package companion

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
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

// InboundFileContent is a verified local file that the Companion may serve
// through its private view URL. Its reader is always closed by Control.
type InboundFileContent struct {
	Name        string
	Size        int64
	Previewable bool
	Reader      io.ReadCloser
}

func StartControl(profile string, snapshot func() any, stop func(), inboundAction func(string, string) error, inboundContent func(string) (InboundFileContent, error)) (*ControlServer, error) {
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
		if strings.HasPrefix(r.URL.Path, prefix+"/api/inbound/") && r.Method == http.MethodGet {
			parts := strings.Split(strings.TrimPrefix(r.URL.Path, prefix+"/api/inbound/"), "/")
			if inboundContent == nil || len(parts) != 2 || parts[0] == "" || (parts[1] != "content" && parts[1] != "download") {
				http.NotFound(w, r)
				return
			}
			content, err := inboundContent(parts[0])
			if err != nil || content.Reader == nil || content.Size < 0 || (parts[1] == "content" && !content.Previewable) {
				http.Error(w, "received file is unavailable", http.StatusNotFound)
				return
			}
			defer content.Reader.Close()
			dispositionType, contentType := "attachment", "application/octet-stream"
			if parts[1] == "content" {
				dispositionType, contentType = "inline", "text/plain; charset=utf-8"
			}
			disposition := mime.FormatMediaType(dispositionType, map[string]string{"filename": content.Name})
			w.Header().Set("Content-Type", contentType)
			w.Header().Set("Content-Disposition", disposition)
			w.Header().Set("Content-Length", fmt.Sprintf("%d", content.Size))
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
			_, _ = io.Copy(w, io.LimitReader(content.Reader, content.Size))
			return
		}
		if strings.HasPrefix(r.URL.Path, prefix+"/api/inbound/") && r.Method == http.MethodPost {
			if inboundAction == nil {
				http.NotFound(w, r)
				return
			}
			parts := strings.Split(strings.TrimPrefix(r.URL.Path, prefix+"/api/inbound/"), "/")
			if len(parts) != 2 || parts[0] == "" || (parts[1] != "accept" && parts[1] != "reject") {
				http.NotFound(w, r)
				return
			}
			if err := inboundAction(parts[1], parts[0]); err != nil {
				http.Error(w, err.Error(), http.StatusConflict)
				return
			}
			writeJSON(w, map[string]bool{"updated": true})
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
*{box-sizing:border-box}body{font:16px -apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;background:#101214;color:#f3f4f6;margin:0;padding:32px;max-width:760px;min-height:100vh}h1{margin:0 0 4px}h2{margin:0 0 4px}h3{font-size:14px;margin:24px 0 4px}.muted{color:#a8b0bb}.card{background:#1b1f24;border:1px solid #30363d;border-radius:12px;padding:20px;margin-top:20px;min-width:0}.row{display:flex;justify-content:space-between;align-items:center;gap:24px;padding:12px 0;border-bottom:1px solid #30363d}.row:last-child{border:0}.row>*,.file-path{min-width:0;overflow-wrap:anywhere;word-break:break-word}.row>strong{text-align:right}.file-name{font-weight:650}.metadata{font-size:14px;line-height:1.55;margin-top:3px}.section-note{font-size:14px;margin:0}.ok{color:#5eead4}.bad{color:#fca5a5}.actions{display:flex;flex-wrap:wrap;justify-content:flex-end;gap:8px}button,.btn-view{background:#475569;color:#fff;border:0;border-radius:8px;padding:10px 14px;font:inherit;cursor:pointer;text-decoration:none}.btn-accept{background:#059669}.btn-reject,.btn-stop{background:#dc2626}.btn-view{background:#2563eb}button:hover,.btn-view:hover{filter:brightness(1.08)}code{font-family:ui-monospace,SFMono-Regular,monospace;overflow-wrap:anywhere}@media(max-width:560px){body{padding:16px}.row{align-items:flex-start;flex-direction:column;gap:8px}.row>strong{text-align:left}.actions{justify-content:flex-start}}</style>
<h1>AgentClip Companion</h1><p class="muted" id="updated">Carregando…</p><section class="card" id="status"></section><section class="card" id="inbound"></section><section class="card"><button id="stop">Parar Companion</button></section>
<script>
const base=location.pathname.replace(/\/$/,"");
const esc=v=>String(v??"—").replace(/[&<>]/g,c=>({"&":"&amp;","<":"&lt;",">":"&gt;"}[c]));
const escAttr=v=>esc(v).replace(/"/g,"&quot;").replace(/'/g,"&#39;");
const dateTime=new Intl.DateTimeFormat('pt-BR',{dateStyle:'medium',timeStyle:'short'});
function formatDateTime(value){const date=new Date(value);return Number.isNaN(date.getTime())?'—':dateTime.format(date)}
function formatBytes(bytes){if(!Number.isFinite(bytes)||bytes<0)return '—';const units=['B','KB','MB','GB'];let value=bytes,unit=0;while(value>=1024&&unit<units.length-1){value/=1024;unit++}return new Intl.NumberFormat('pt-BR',{maximumFractionDigits:value>=10?0:1}).format(value)+' '+units[unit]}
function row(k,v){return '<div class="row"><span class="muted">'+k+'</span><strong>'+v+'</strong></div>'}
async function copyPath(path){try{await navigator.clipboard.writeText(path)}catch(_){const input=document.createElement('textarea');input.value=path;document.body.append(input);input.select();document.execCommand('copy');input.remove()}alert('Caminho copiado.')}
async function inbound(id,action){const r=await fetch(base+'/api/inbound/'+encodeURIComponent(id)+'/'+action,{method:'POST'});if(!r.ok){alert(await r.text());return}await refresh()}
async function refresh(){try{const r=await fetch(base+"/api/status",{cache:"no-store"});if(!r.ok)throw new Error('status unavailable');const s=await r.json();const tunnel=s.tunnel?.connected?'<span class="ok">Conectado</span>':'<span class="bad">Desconectado</span>';const items=s.clipboard?.items||[];const clip=s.clipboard?.armed?(items.length?'<span class="ok">'+items.length+' item(ns): '+esc(items.map(i=>i.name||i.kind).join(', '))+'</span>':'<span class="muted">Sem itens</span>'):'<span class="muted">Sem clipboard armado</span>';const expiry=s.clipboard?.armed&&s.clipboard?.expires_at?esc(formatDateTime(s.clipboard.expires_at)):'—';document.querySelector('#status').innerHTML=row('Perfil',esc(s.profile))+row('Servidor','<code>'+esc(s.destination)+'</code>')+row('Túnel',tunnel)+row('Clipboard',clip)+row('Expira',expiry)+(s.tunnel?.last_error?row('Último erro','<span class="bad">'+esc(s.tunnel.last_error)+'</span>'):'');const offers=s.inbound?.offers||[];const received=s.inbound?.received||[];const pending=offers.length?offers.map(o=>'<div class="row"><span><span class="file-name">'+esc(o.name)+'</span><div class="muted metadata">Oferta recebida em '+esc(formatDateTime(o.created_at))+' · '+esc(formatBytes(o.size))+'<br>Expira em '+esc(formatDateTime(o.expires_at))+'</div></span><span class="actions"><button class="btn-accept" onclick="inbound(\''+esc(o.id)+'\',\'accept\')">Aceitar</button><button class="btn-reject" onclick="inbound(\''+esc(o.id)+'\',\'reject\')">Recusar</button></span></div>').join(''):'<p class="muted section-note">Nenhum arquivo aguardando aprovação.</p>';const done=received.length?received.map(o=>'<div class="row"><span><span class="file-name">'+esc(o.name)+'</span><div class="muted metadata">Recebido em '+esc(formatDateTime(o.delivered_at))+' · '+esc(formatBytes(o.size))+'</div><div class="muted metadata file-path">'+esc(o.path)+'</div></span><span class="actions">'+(o.previewable?'<a class="btn-view" target="_blank" rel="noopener" href="'+base+'/api/inbound/'+encodeURIComponent(o.id)+'/content">Abrir conteúdo</a>':'')+'<a class="btn-view" href="'+base+'/api/inbound/'+encodeURIComponent(o.id)+'/download">Baixar</a><button onclick="copyPath(this.dataset.path)" data-path="'+escAttr(o.path)+'">Copiar caminho</button></span></div>').join(''):'<p class="muted section-note">Nenhum arquivo recebido nesta sessão.</p>';document.querySelector('#inbound').innerHTML='<h2>Arquivos do servidor</h2><h3>Aguardando sua aprovação ('+offers.length+')</h3>'+pending+'<h3>Recebidos recentemente ('+received.length+')</h3>'+done;document.querySelector('#updated').textContent='Atualizado às '+formatDateTime(new Date())}catch(e){document.querySelector('#updated').textContent='Não foi possível consultar o Companion'}}
document.querySelector('#stop').onclick=async()=>{if(confirm('Parar o Companion?')){await fetch(base+'/api/stop',{method:'POST'});setTimeout(refresh,500)}};document.querySelector('#stop').className='btn-stop';refresh();setInterval(refresh,2000);
</script></html>`
