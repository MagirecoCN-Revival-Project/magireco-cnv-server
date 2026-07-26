function PageDashboard() {
  const { state, dispatch } = useApp();
  const toast = useToast();
  const { server, accounts, activeBans, jsBundle, scenarioBundle, events, heartbeats, nodes } = state;

  const activeAccountCount = accounts.filter(a => a.status === "active").length;
  const liveDownloadCount = heartbeats.length;
  const sessions = MOCK.sessions24h;
  const total24h = sessions.reduce((a, b) => a + b, 0);

  // ticking clock
  const [, force] = useState(0);
  useEffect(() => { const t = setInterval(() => force(n => n + 1), 5000); return () => clearInterval(t); }, []);

  const onStatusToggle = (next) => {
    dispatch({ type: "server.set", patch: { status: next } });
    dispatch({ type: "audit.add", entry: { type: "server.status.change", target: next, details: { note: "从仪表盘切换" } } });
    toast(`服务器状态已切换为「${next === "ok" ? "正常" : next === "warn" ? "维护中" : "异常"}」`, "ok");
  };

  return (
    <div className="page" data-screen-label="01 仪表盘">
      <div className="page-head">
        <div>
          <div className="page-title">仪表盘 · 实时概览</div>
          <div className="page-sub">最近 24 小时服务端运行概况 · 自动刷新 5s</div>
        </div>
        <div className="actions">
          <div className="segmented">
            <button className={server.status === "ok" ? "active" : ""} onClick={() => onStatusToggle("ok")}>正常</button>
            <button className={server.status === "warn" ? "active" : ""} onClick={() => onStatusToggle("warn")}>维护</button>
            <button className={server.status === "err" ? "active" : ""} onClick={() => onStatusToggle("err")}>异常</button>
          </div>
        </div>
      </div>

      {/* Status banner */}
      <div className={`status-banner ${server.status}`} style={{ marginBottom: 18 }}>
        <div className="pulse"/>
        <div style={{ flex: 1 }}>
          <div style={{ fontSize: 13, fontWeight: 600 }}>
            {server.status === "ok" && "服务正常运行"}
            {server.status === "warn" && "服务处于维护模式"}
            {server.status === "err" && "服务报告异常"}
          </div>
          <div style={{ color: "var(--text-3)", fontSize: 12, marginTop: 3 }}>
            {server.status === "ok"
              ? `客户端正常握手 · /init 接口响应中位 23ms · ${liveDownloadCount} 台设备正在下载`
              : server.status === "warn"
                ? server.maintenanceMessage
                : "客户端 /init 失败率 > 30%,请检查 cap-worker 与数据库连接"}
          </div>
        </div>
        <div style={{ fontFamily: "var(--mono)", fontSize: 11.5, color: "var(--text-3)" }}>
          uptime <span style={{ color: "var(--text-1)" }}>71d 04h 22m</span>
        </div>
      </div>

      {/* KPI row */}
      <div className="grid grid-4" style={{ marginBottom: 14 }}>
        <Card tight>
          <div className="stat">
            <div className="stat-label">24h 活跃会话</div>
            <div className="stat-value">{fmt.num(total24h)}</div>
            <div className="stat-foot"><span className="stat-delta-up">↑ 8.4%</span> 较昨日</div>
            <div style={{ marginTop: 10 }}>
              <Sparkline data={sessions} />
            </div>
          </div>
        </Card>
        <Card tight>
          <div className="stat">
            <div className="stat-label">正在下载</div>
            <div className="stat-value" style={{ color: "var(--teal-300)" }}>{liveDownloadCount}</div>
            <div className="stat-foot">
              <span style={{ color: "var(--teal-300)" }}>●</span> 实时心跳
              <span className="dot-sep">·</span>
              在线下载 {heartbeats.filter(h => h.type === "online").length} · 热更新 {heartbeats.filter(h => h.type === "hotupdate").length} · 游戏中 {heartbeats.filter(h => h.type === "game").length}
            </div>
          </div>
        </Card>
        <Card tight>
          <div className="stat">
            <div className="stat-label">注册账号</div>
            <div className="stat-value">{fmt.num(accounts.length)}</div>
            <div className="stat-foot">
              活跃 <span style={{ color: "var(--text-1)" }}>{activeAccountCount}</span>
              <span className="dot-sep">·</span>
              停用 <span style={{ color: "var(--text-1)" }}>{accounts.length - activeAccountCount}</span>
            </div>
          </div>
        </Card>
        <Card tight>
          <div className="stat">
            <div className="stat-label">封禁设备</div>
            <div className="stat-value" style={{ color: "var(--red-400)" }}>{fmt.num(activeBans.length)}</div>
            <div className="stat-foot">
              永封 <span style={{ color: "var(--text-1)" }}>{activeBans.filter(b => !b.expireTime).length}</span>
              <span className="dot-sep">·</span>
              系统判定 <span style={{ color: "var(--text-1)" }}>{activeBans.filter(b => b.auto).length}</span>
            </div>
          </div>
        </Card>
      </div>

      <div className="grid grid-2" style={{ marginBottom: 14 }}>
        <NodeCard
          which="primary"
          node={nodes.primary}
          onToggleMaint={(v) => dispatch({ type: "nodes.set", which: "primary", patch: { maintenance: v } })}
        />
        <NodeCard
          which="secondary"
          node={nodes.secondary}
          onToggleMaint={(v) => dispatch({ type: "nodes.set", which: "secondary", patch: { maintenance: v } })}
        />
      </div>

      <div className="grid grid-2" style={{ gridTemplateColumns: "1.4fr 1fr" }}>
        <Card title="最近事件" subtitle="登录 · 封禁 · 版本推送 (近 10 条)"
          actions={<button className="btn btn-ghost btn-sm">查看全部 <I.ChevRight size={11}/></button>}
          tight>
          <div>
            {events.slice(0, 10).map(e => (
              <div className="event-row" key={e.id}>
                <span className="when">{fmt.ago(e.ts)}</span>
                <span className={`icon-pill ${e.color}`}>
                  {React.createElement(I[e.icon], { size: 12 })}
                </span>
                <span>{e.text}</span>
                <span className="who">{e.actor}</span>
              </div>
            ))}
          </div>
        </Card>

        <div style={{ display: "flex", flexDirection: "column", gap: 14 }}>
          <Card title="热更新版本" subtitle="当前发布到客户端的最新版本">
            <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: 14 }}>
              <div>
                <div className="kv-label">JS Bundle</div>
                <div style={{ fontSize: 22, fontWeight: 600, fontFamily: "var(--mono)", marginTop: 4 }}>v{jsBundle.version}</div>
                <div style={{ color: "var(--text-3)", fontSize: 11.5, marginTop: 2 }}>
                  {fmt.bytes(jsBundle.size)} · {fmt.ago(jsBundle.publishedAt)}
                </div>
              </div>
              <div>
                <div className="kv-label">Scenario Bundle</div>
                <div style={{ fontSize: 22, fontWeight: 600, fontFamily: "var(--mono)", marginTop: 4 }}>v{scenarioBundle.version}</div>
                <div style={{ color: "var(--text-3)", fontSize: 11.5, marginTop: 2 }}>
                  {fmt.bytes(scenarioBundle.size)} · {fmt.ago(scenarioBundle.publishedAt)}
                </div>
              </div>
            </div>
          </Card>

          <Card title="功能开关" subtitle="客户端 /init 返回值">
            <div style={{ display: "flex", flexDirection: "column", gap: 10 }}>
              <div style={{ display: "flex", alignItems: "center", gap: 10 }}>
                <I.Cloud size={14} style={{ color: "var(--text-3)" }}/>
                <span style={{ flex: 1, fontSize: 13 }}>在线下载</span>
                <Switch value={server.onlineDownloadEnabled} onChange={(v) => dispatch({ type: "server.set", patch: { onlineDownloadEnabled: v } })}/>
              </div>
              <div style={{ display: "flex", alignItems: "center", gap: 10 }}>
                <I.HardDrive size={14} style={{ color: "var(--text-3)" }}/>
                <span style={{ flex: 1, fontSize: 13 }}>离线整包</span>
                <Switch value={server.offlinePackageEnabled} onChange={(v) => dispatch({ type: "server.set", patch: { offlinePackageEnabled: v } })}/>
              </div>
            </div>
          </Card>
        </div>
      </div>
    </div>
  );
}
Object.assign(window, { PageDashboard });

/* ====================== NodeCard ====================== */
function NodeCard({ which, node, onToggleMaint }) {
  const isPrimary = which === "primary";
  const status = node.maintenance ? "warn" : node.status;
  const statusLabel = node.maintenance ? "维护中" : (status === "ok" ? "正常" : status === "warn" ? "警告" : "异常");

  return (
    <div className="card">
      <div className="card-head" style={{ alignItems: "flex-start" }}>
        <div style={{ display: "flex", alignItems: "center", gap: 10, flex: 1, minWidth: 0 }}>
          <span style={{
            width: 32, height: 32,
            borderRadius: 8,
            background: isPrimary ? "var(--accent-50)" : "var(--teal-50)",
            border: `1px solid ${isPrimary ? "var(--accent-200)" : "rgba(20,184,166,0.3)"}`,
            color: isPrimary ? "var(--accent-700)" : "var(--teal-700)",
            display: "grid", placeItems: "center",
            flexShrink: 0,
          }}>
            {isPrimary ? <I.Server size={14}/> : <I.Cloud size={14}/>}
          </span>
          <div style={{ minWidth: 0 }}>
            <div style={{ display: "flex", alignItems: "center", gap: 8, flexWrap: "wrap" }}>
              <span className="card-title mono">{node.id}</span>
              <StatusBadge status={status === "ok" ? "ok" : status === "warn" ? "warn" : "err"}>{statusLabel}</StatusBadge>
              {!isPrimary && node.forwardingDataRequests && (
                <span className="badge-pill badge-info"><span className="dot"/>转发主节点</span>
              )}
            </div>
            <div className="card-sub">{node.roleLabel}</div>
          </div>
        </div>
      </div>
      <div className="card-body">
        <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: 12, marginBottom: 12 }}>
          <div className="kvpair">
            <div className="kv-label">区域</div>
            <div className="kv-value">
              <span style={{ marginRight: 6 }}>{node.regionLabel}</span>
              <span className="mono" style={{ color: "var(--text-3)", fontSize: 11.5 }}>{node.region}</span>
            </div>
          </div>
          <div className="kvpair">
            <div className="kv-label">公网 IP</div>
            <div className="kv-value mono">{node.publicIP}</div>
          </div>
          <div className="kvpair">
            <div className="kv-label">在线时长</div>
            <div className="kv-value mono" style={{ fontSize: 12 }}>{fmt.uptime(node.uptimeSec)}</div>
          </div>
          <div className="kvpair">
            <div className="kv-label">CPU · 内存</div>
            <div className="kv-value mono" style={{ fontSize: 12 }}>{node.cpuPct}% · {node.memPct}%</div>
          </div>
        </div>

        {isPrimary ? (
          <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr 1fr", gap: 10 }}>
            <NodeStat label="QPS" value={fmt.num(node.qps)}/>
            <NodeStat label="活跃连接" value={fmt.num(node.activeConns)}/>
            <NodeStat label="服务" value={
              <span style={{ display: "inline-flex", gap: 4, flexWrap: "wrap" }}>
                {node.services.map(s => <span key={s} className="badge-pill" style={{ fontSize: 10.5 }}>{s}</span>)}
              </span>
            }/>
          </div>
        ) : (
          <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr 1fr", gap: 10 }}>
            <NodeStat label="正在下载" value={<span style={{ color: "var(--teal-700)" }}>{fmt.num(node.activeDownloads)}</span>}/>
            <NodeStat label="出口带宽" value={<span className="mono">{fmt.speed(node.egressBps)}</span>}/>
            <NodeStat label="缓存命中" value={<span className="mono">{fmt.pct(node.cacheHitRate)}</span>}/>
          </div>
        )}

        {!isPrimary && (
          <div style={{
            display: "flex", alignItems: "center", gap: 10,
            marginTop: 12, paddingTop: 12,
            borderTop: "1px solid var(--border-1)",
            fontSize: 11.5, color: "var(--text-3)",
          }}>
            <I.ArrowUp size={12} style={{ color: "var(--accent-600)" }}/>
            上行至主节点 · 延迟 <span className="mono" style={{ color: "var(--text-2)" }}>{node.upstreamLatencyMs}ms</span>
            <span style={{ marginLeft: "auto", display: "flex", alignItems: "center", gap: 6 }}>
              维护模式
              <Switch value={node.maintenance} onChange={onToggleMaint}/>
            </span>
          </div>
        )}
        {isPrimary && (
          <div style={{
            display: "flex", alignItems: "center", gap: 10,
            marginTop: 12, paddingTop: 12,
            borderTop: "1px solid var(--border-1)",
            fontSize: 11.5, color: "var(--text-3)",
          }}>
            <I.Info size={12}/>
            面板与数据处理均在此节点 · 所有写入与鉴权都不会下放副节点
          </div>
        )}
      </div>
    </div>
  );
}

function NodeStat({ label, value }) {
  return (
    <div>
      <div className="kv-label">{label}</div>
      <div style={{ fontSize: 14, fontWeight: 600, fontFamily: "var(--mono)", marginTop: 4, letterSpacing: "-0.01em" }}>{value}</div>
    </div>
  );
}
