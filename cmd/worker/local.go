package main

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"remoteops/internal/worker"
	"remoteops/pkg/protocol"
)

type localControl struct {
	origin, token string
	pairing       protocol.PairingBundle
	session       protocol.Session
	srv           *worker.Server
	stop          func()
}

func (c localControl) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; frame-ancestors 'none'; base-uri 'none'")
		if "http://"+r.Host != c.origin {
			http.Error(w, "forbidden", 403)
			return
		}
		if r.URL.Path == "/" && r.Method == "GET" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write([]byte(localPage))
			return
		}
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+c.token)) != 1 || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			http.Error(w, "forbidden", 403)
			return
		}
		if r.Method == "POST" && r.Header.Get("Origin") != c.origin {
			http.Error(w, "forbidden", 403)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/disconnect" && r.Method == "POST" {
			c.stop()
			json.NewEncoder(w).Encode(map[string]string{"status": "disconnected"})
			return
		}
		if r.URL.Path == "/api/status" && r.Method == "GET" {
			tasks := []protocol.Task{}
			if c.srv != nil {
				tasks = c.srv.LocalTasks()
			}
			json.NewEncoder(w).Encode(struct {
				Controller string           `json:"controller"`
				Session    protocol.Session `json:"session"`
				Tasks      []protocol.Task  `json:"tasks"`
			}{c.pairing.ControllerName, c.session, tasks})
			return
		}
		http.NotFound(w, r)
	})
}

const localPage = `<!doctype html><html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>RemoteOps local session</title><style>body{font:16px system-ui;max-width:800px;margin:40px auto;padding:24px;background:#f4f6f8;color:#152434}button{background:#b42318;color:white;border:0;padding:14px 24px;border-radius:8px;font-weight:bold}pre{white-space:pre-wrap;background:white;padding:20px;border-radius:10px}</style><h1>RemoteOps local session</h1><p id="state">Opening secure local status…</p><button id="stop">Disconnect immediately</button><h2>Authorized scope and tasks</h2><pre id="status"></pre><script>let token=location.hash.slice(1);history.replaceState(null,'',location.pathname);const headers={'Authorization':'Bearer '+token};async function refresh(){try{let r=await fetch('/api/status',{headers});if(!r.ok)throw Error();let s=await r.json();document.getElementById('state').textContent='Controller: '+s.controller+' | Ends: '+s.session.expires_at;document.getElementById('status').textContent=JSON.stringify({scope:s.session.scope,tasks:s.tasks.map(t=>({id:t.id,type:t.spec.type,package:t.spec.package,state:t.state,requires_admin:t.spec.requires_admin}))},null,2)}catch(e){document.getElementById('state').textContent='Disconnected or local token unavailable.'}}document.getElementById('stop').onclick=async()=>{await fetch('/api/disconnect',{method:'POST',headers});token='';document.getElementById('state').textContent='Disconnected';document.getElementById('stop').disabled=true};refresh();setInterval(refresh,2000)</script></html>`
