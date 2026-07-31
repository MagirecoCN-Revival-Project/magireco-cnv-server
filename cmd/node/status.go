package main

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"magirecocn-revival/api-server/internal/control"
)

// statusSnapshot 是只读状态页对外暴露的快照。
//
// 安全(§5):仅含运行时指标与节点身份,绝不含数据库串、密钥、令牌等敏感信息。
type statusSnapshot struct {
	NodeID     string  `json:"node_id"`
	Role       string  `json:"role"`
	Version    string  `json:"version"`
	StartedAt  int64   `json:"started_at"` // Unix 毫秒
	UptimeSec  int64   `json:"uptime_sec"`
	MemPct     float64 `json:"mem_pct"`
	Goroutines int     `json:"goroutines"`
	Now        int64   `json:"now"` // 服务端当前时间(Unix 毫秒),便于前端对时
}

// mountStatus 给任意角色的节点在根目录挂一个**只读**实时状态页 + JSON 端点。
//
//   - GET /            人类可读的状态页(自带轻量 JS,每 5s 拉一次 /status.json 刷新)
//   - GET /status.json 机器可读快照
//
// 无鉴权、纯只读:节点对外暴露的仅是自身运行指标,不接受任何变更操作。
func mountStatus(r chi.Router, nodeID, version string, started time.Time, tel func() control.Telemetry) {
	snapshot := func() statusSnapshot {
		t := tel()
		return statusSnapshot{
			NodeID:     nodeID,
			Role:       t.NodeRole,
			Version:    version,
			StartedAt:  started.UnixMilli(),
			UptimeSec:  t.UptimeSec,
			MemPct:     t.MemPct,
			Goroutines: t.Goroutines,
			Now:        time.Now().UnixMilli(),
		}
	}

	r.Get("/status.json", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(snapshot())
	})

	r.Get("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(statusHTML)
	})
}

// statusHTML 是内置的只读状态页。所有动态数据由页面内 JS 拉 /status.json 填充,
// 不内联任何敏感信息;脚本仅用 'unsafe-inline'(与全站 CSP 一致)。
var statusHTML = []byte(`<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8" />
<meta name="viewport" content="width=device-width, initial-scale=1" />
<title>节点状态 · 魔法纪录复兴计划</title>
<style>
  :root { color-scheme: light dark; }
  body { margin:0; font-family: system-ui, -apple-system, "Segoe UI", "PingFang SC", "Microsoft YaHei", sans-serif;
    background: linear-gradient(135deg,#efe6ff 0%,#fde9f3 45%,#fff7e6 100%); background-attachment: fixed;
    min-height:100vh; display:flex; align-items:center; justify-content:center; padding:2rem; box-sizing:border-box; }
  .card { background:rgba(255,255,255,0.86); backdrop-filter: blur(8px);
    border:1px solid rgba(0,0,0,0.06); border-radius:16px; padding:28px 32px; width:min(520px,100%);
    box-shadow:0 12px 40px rgba(80,40,120,0.12); }
  .brand { display:flex; align-items:center; gap:12px; margin-bottom:18px; }
  .mark { width:40px; height:40px; border-radius:10px; object-fit:cover; display:block;
    box-shadow:0 0 0 1px rgba(168,85,247,0.25), 0 4px 10px rgba(0,0,0,0.08); }
  .brand-text { font-weight:600; font-size:15px; color:#3b2a52; }
  .brand-sub { font-size:12px; color:#8a7aa0; }
  h1 { font-size:15px; margin:0 0 14px; color:#3b2a52; font-weight:600; }
  .grid { display:grid; grid-template-columns:auto 1fr; gap:10px 18px; font-size:13.5px; }
  .k { color:#8a7aa0; }
  .v { color:#2a1f3d; font-weight:500; font-variant-numeric: tabular-nums; word-break:break-all; }
  .pill { display:inline-flex; align-items:center; gap:6px; padding:2px 10px; border-radius:999px; font-size:12px; font-weight:600; }
  .pill.business { background:#ede9fe; color:#6d28d9; }
  .pill.edge { background:#dcfce7; color:#15803d; }
  .pill.unknown { background:#e5e7eb; color:#374151; }
  .dot { width:7px; height:7px; border-radius:50%; background:#22c55e; box-shadow:0 0 0 3px rgba(34,197,94,0.18); }
  .foot { margin-top:18px; font-size:11.5px; color:#9b8bb0; display:flex; justify-content:space-between; }
  .ro { display:inline-flex; align-items:center; gap:5px; }
</style>
</head>
<body>
<div class="card">
  <div class="brand">
    <img class="mark" src="data:image/png;base64,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" alt="魔法纪录复兴计划" />
    <div>
      <div class="brand-text">魔法纪录复兴计划</div>
      <div class="brand-sub">节点实时状态</div>
    </div>
  </div>
  <h1>本节点运行指标</h1>
  <div class="grid">
    <div class="k">节点角色</div><div class="v"><span id="role" class="pill unknown">—</span></div>
    <div class="k">节点 ID</div><div class="v" id="node_id">—</div>
    <div class="k">版本</div><div class="v" id="version">—</div>
    <div class="k">运行状态</div><div class="v"><span class="dot"></span> 在线</div>
    <div class="k">运行时长</div><div class="v" id="uptime">—</div>
    <div class="k">堆内存占用</div><div class="v" id="mem">—</div>
    <div class="k">Goroutine</div><div class="v" id="goroutines">—</div>
    <div class="k">启动时间</div><div class="v" id="started">—</div>
  </div>
  <div class="foot">
    <span class="ro">🔒 只读页面 · 不接受任何操作</span>
    <span id="tick">自动刷新中…</span>
  </div>
</div>
<script>
(function(){
  function fmtDur(s){
    s = Math.max(0, Math.floor(s));
    var d=Math.floor(s/86400); s%=86400;
    var h=Math.floor(s/3600); s%=3600;
    var m=Math.floor(s/60); var sec=s%60;
    var out=[];
    if(d) out.push(d+"天");
    if(h||d) out.push(h+"时");
    if(m||h||d) out.push(m+"分");
    out.push(sec+"秒");
    return out.join("");
  }
  function roleLabel(r){
    if(r==="business") return ["业务节点","business"];
    if(r==="edge") return ["边缘节点","edge"];
    return [r||"未知","unknown"];
  }
  function set(id,v){ var el=document.getElementById(id); if(el) el.textContent=v; }
  async function tick(){
    try{
      var resp = await fetch("/status.json", {cache:"no-store"});
      var s = await resp.json();
      var rl = roleLabel(s.role);
      var rel = document.getElementById("role");
      rel.textContent = rl[0]; rel.className = "pill "+rl[1];
      set("node_id", s.node_id||"—");
      set("version", s.version||"—");
      set("uptime", fmtDur(s.uptime_sec));
      set("mem", (s.mem_pct!=null? s.mem_pct.toFixed(1):"—")+"%");
      set("goroutines", s.goroutines!=null? String(s.goroutines):"—");
      set("started", s.started_at? new Date(s.started_at).toLocaleString("zh-CN"):"—");
      set("tick", "更新于 "+new Date().toLocaleTimeString("zh-CN"));
    }catch(e){
      set("tick", "刷新失败,重试中…");
    }
  }
  tick();
  setInterval(tick, 5000);
})();
</script>
</body>
</html>`)
