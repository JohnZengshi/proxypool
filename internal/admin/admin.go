package admin

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/john/proxypool/internal/manager"
)

type Handler struct {
	manager *manager.Manager
}

func New(manager *manager.Manager) *Handler {
	return &Handler{manager: manager}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, dashboardHTML)
	case r.Method == http.MethodGet && r.URL.Path == "/status":
		writeJSON(w, http.StatusOK, h.manager.Snapshot())
	case r.Method == http.MethodGet && r.URL.Path == "/history":
		writeJSON(w, http.StatusOK, h.manager.History())
	case r.Method == http.MethodGet && r.URL.Path == "/healthz":
		h.health(w)
	case r.Method == http.MethodPost && r.URL.Path == "/probe":
		h.probe(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/reconnect":
		h.reconnect(w, r)
	default:
		http.NotFound(w, r)
	}
}

const dashboardHTML = `<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Proxy Pool</title>
<style>
:root{color-scheme:light;--bg:#f5f6f8;--panel:#fff;--border:#d7dbe2;--text:#1c2330;--muted:#6b7280;--ok:#137a3b;--warn:#b45309;--bad:#c03434;--accent:#2563eb;--accent2:#0f766e}
*{box-sizing:border-box}
body{margin:0;background:var(--bg);color:var(--text);font:14px/1.45 system-ui,-apple-system,"Segoe UI",sans-serif}
header{display:flex;align-items:center;justify-content:space-between;gap:16px;padding:14px 20px;background:var(--panel);border-bottom:1px solid var(--border)}
h1{font-size:18px;margin:0}
.header-actions{display:flex;align-items:center;gap:10px;flex-wrap:wrap}
button{border:1px solid var(--border);background:#fff;color:var(--text);border-radius:6px;padding:7px 11px;font:inherit;cursor:pointer}
button:hover{border-color:var(--accent);color:var(--accent)}
button:disabled{opacity:.55;cursor:default}
button.primary{background:var(--accent);border-color:var(--accent);color:#fff}
main{padding:16px 20px;max-width:1280px;margin:0 auto}
.toolbar{display:flex;align-items:center;justify-content:space-between;gap:12px;margin:0 0 12px;flex-wrap:wrap}
.summary{display:flex;gap:10px;flex-wrap:wrap}
.pill{background:var(--panel);border:1px solid var(--border);border-radius:999px;padding:4px 10px;color:var(--muted)}
.pill b{color:var(--text)}
.filters{display:flex;align-items:center;gap:8px;flex-wrap:wrap}
select{border:1px solid var(--border);border-radius:6px;background:#fff;padding:7px 9px;font:inherit}
.table-wrap{background:var(--panel);border:1px solid var(--border);border-radius:8px;overflow:auto}
table{width:100%;border-collapse:collapse;min-width:880px}
th,td{padding:10px 12px;text-align:left;border-bottom:1px solid #e5e8ed;white-space:nowrap}
th{background:#fafbfc;color:var(--muted);font-weight:600;position:sticky;top:0}
tr:last-child td{border-bottom:0}
td.proxy{font-family:ui-monospace,SFMono-Regular,Consolas,monospace;font-size:12px}
.status{display:inline-flex;align-items:center;gap:6px}
.dot{width:8px;height:8px;border-radius:50%;background:#d1d5db}
.ok .dot{background:var(--ok)}.warn .dot,.slow .dot{background:var(--warn)}.bad .dot{background:var(--bad)}
.latency{font-variant-numeric:tabular-nums}
.err{max-width:280px;overflow:hidden;text-overflow:ellipsis;color:var(--muted)}
svg{display:block}
.empty{padding:34px;text-align:center;color:var(--muted)}
.error{position:fixed;right:16px;bottom:16px;max-width:380px;background:#7f1d1d;color:#fff;border-radius:8px;padding:12px 14px;display:none;box-shadow:0 8px 24px rgb(0 0 0 / 20%)}
@media(max-width:640px){header{align-items:flex-start;flex-direction:column}main{padding:10px}}
</style>
</head>
<body>
<header>
  <h1>Proxy Pool</h1>
 <div class="header-actions">
    <span class="pill" id="updated">等待数据</span>
    <button class="primary" id="probe-all">探测全部</button>
    <button class="primary" id="reconnect-all">全部重连</button>
  </div>
</header>
<main>
  <div class="toolbar">
    <div class="summary">
      <span class="pill">节点 <b id="total">0</b></span>
      <span class="pill">在线 <b id="online">0</b></span>
      <span class="pill">慢 <b id="slow">0</b></span>
      <span class="pill">离线 <b id="offline">0</b></span>
    </div>
    <div class="filters">
      <label>标签
        <select id="tag-filter"><option value="">全部</option></select>
      </label>
    </div>
  </div>
  <div class="table-wrap">
    <table>
      <thead>
        <tr><th>端口</th><th>代理地址</th><th>出口 IP</th><th>状态</th><th>延迟</th><th>失败原因</th><th>标签</th><th>节点</th><th>延迟趋势</th><th>操作</th></tr>
      </thead>
      <tbody id="rows"></tbody>
    </table>
    <div class="empty" id="empty">暂无节点</div>
  </div>
</main>
<div class="error" id="error"></div>
<script>
const state={status:[],history:{},tag:""};
const el=(id)=>document.getElementById(id);
const statuses={ok:"在线",slow:"慢",bad:"离线"};
const SPARK_W=160,SPARK_H=30;
function esc(v){return String(v??"").replace(/[&<>"']/g,c=>({"&":"&amp;","<":"&lt;",">":"&gt;",'"':"&quot;","'":"&#39;"}[c]));}
function latencyClass(ms){return ms<300?"ok":ms<800?"warn":"bad";}
function latencyText(ms){return ms==null?"-":ms+" ms";}
function statusFor(n){return n.healthy?(n.latency_ms>=n.slow_latency_ms?"slow":"ok"):"bad";}
function spark(port){const samples=state.history[port]||[];if(samples.length<2)return '<span class="muted">-</span>';const vals=samples.map(s=>s.healthy?s.latency_ms:0);const max=Math.max(...vals,1);const points=vals.map((v,i)=>i*SPARK_W/(vals.length-1)+','+(SPARK_H-1-(v/max)*(SPARK_H-3))).join(" ");return '<svg width="'+SPARK_W+'" height="'+SPARK_H+'" viewBox="0 0 '+SPARK_W+' '+SPARK_H+'" role="img" aria-label="延迟趋势"><polyline points="'+points+'" fill="none" stroke="'+(samples.at(-1).healthy?"#2563eb":"#c03434")+'" stroke-width="2"/></svg>';}
function render(){
  const tags=new Set(state.status.map(n=>n.tag));
  const tagSel=el("tag-filter"),prev=tagSel.value;
  tagSel.innerHTML='<option value="">全部</option>'+[...tags].sort().map(t=>'<option value="'+esc(t)+'">'+esc(t)+'</option>').join("");
  tagSel.value=state.tag&&tags.has(state.tag)?state.tag:prev;
  const filtered=state.status.filter(n=>!state.tag||n.tag===state.tag);
  el("total").textContent=state.status.length;el("online").textContent=state.status.filter(n=>n.healthy).length;el("slow").textContent=state.status.filter(n=>statusFor(n)==="slow").length;el("offline").textContent=state.status.filter(n=>!n.healthy).length;
  el("empty").style.display=filtered.length?"none":"block";
  el("rows").innerHTML=filtered.map(n=>{
    const cls=statusFor(n);
    return '<tr data-port="'+n.port+'"><td>'+n.port+'</td><td class="proxy">http://127.0.0.1:'+n.port+'</td><td>'+esc(n.exit_ip||"-")+'</td><td><span class="status '+cls+'"><span class="dot"></span>'+statuses[cls]+'</span></td><td class="latency '+latencyClass(n.latency_ms)+'">'+latencyText(n.latency_ms)+'</td><td class="err" title="'+esc(n.last_error||"")+'">'+esc(n.last_error||"-")+'</td><td>'+esc(n.tag||"-")+'</td><td>'+esc(n.node_name||"-")+'</td><td>'+spark(n.port)+'</td><td><button data-probe="'+n.port+'">探测</button><button data-reconnect="'+n.port+'">重连</button></td></tr>';
  }).join("");
  el("updated").textContent="更新 "+new Date().toLocaleTimeString();
}
async function load(){
  try{
    const [s,h]=await Promise.all([fetch("/status").then(r=>r.json()),fetch("/history").then(r=>r.json())]);
    state.status=s;state.history=h;render();
    el("error").style.display="none";
  }catch(err){el("error").textContent="刷新失败："+err;el("error").style.display="block";}
}
async function doProbe(port){
  el("probe-all").disabled=port==null;
  const btn=port==null?el("probe-all"):document.querySelector('[data-probe="'+port+'"]');
  if(btn)btn.disabled=true;
  try{
    const r=await fetch(port==null?"/probe":"/probe?port="+port,{method:"POST"});
    if(!r.ok)throw new Error(await r.text());
    await load();
  }catch(err){el("error").textContent="探测失败："+err;el("error").style.display="block";}
  if(btn)btn.disabled=false;
}
async function doReconnect(port){
  const selector=port==null?"#reconnect-all":'[data-reconnect="'+port+'"]';
  const btn=document.querySelector(selector);
  if(btn)btn.disabled=true;
  try{
    const r=await fetch(port==null?"/reconnect":"/reconnect?port="+port,{method:"POST"});
    if(!r.ok)throw new Error(await r.text());
    await load();
  }catch(err){el("error").textContent="重连失败："+err;el("error").style.display="block";}
  if(btn)btn.disabled=false;
}
el("probe-all").addEventListener("click",()=>doProbe(null));
el("reconnect-all").addEventListener("click",()=>doReconnect(null));
el("tag-filter").addEventListener("change",e=>{state.tag=e.target.value;render();});
document.addEventListener("click",e=>{const b=e.target.closest("[data-probe]");if(b)doProbe(Number(b.dataset.probe));});
document.addEventListener("click",e=>{const b=e.target.closest("[data-reconnect]");if(b)doReconnect(Number(b.dataset.reconnect));});
load();setInterval(load,5000);
</script>
</body>
</html>`

func (h *Handler) health(w http.ResponseWriter) {
	for _, status := range h.manager.Snapshot() {
		if status.Healthy {
			w.WriteHeader(http.StatusOK)
			return
		}
	}
	http.Error(w, "no healthy nodes", http.StatusServiceUnavailable)
}

func (h *Handler) probe(w http.ResponseWriter, r *http.Request) {
	port, err := parsePort(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := h.manager.ProbeNow(r.Context(), port); err != nil {
		if errors.Is(err, manager.ErrPortNotFound) {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		http.Error(w, "probe failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) reconnect(w http.ResponseWriter, r *http.Request) {
	port, err := parsePort(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := h.manager.ReconnectNow(r.Context(), port); err != nil {
		if errors.Is(err, manager.ErrPortNotFound) {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		http.Error(w, "reconnect failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func parsePort(r *http.Request) (int, error) {
	value := r.URL.Query().Get("port")
	if value == "" {
		return 0, nil
	}
	port, err := strconv.Atoi(value)
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("invalid port")
	}
	return port, nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		return
	}
}
