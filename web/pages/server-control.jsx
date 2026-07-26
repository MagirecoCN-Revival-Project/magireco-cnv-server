function PageServerControl() {
  const { state, dispatch } = useApp();
  const toast = useToast();
  const { server } = state;

  const [draftMsg, setDraftMsg] = useState(server.maintenanceMessage);
  const [draftDisabled, setDraftDisabled] = useState(server.disabledMessage);
  const [endDate, setEndDate] = useState("");

  useEffect(() => { setDraftMsg(server.maintenanceMessage); }, [server.maintenanceMessage]);
  useEffect(() => { setDraftDisabled(server.disabledMessage); }, [server.disabledMessage]);

  const setStatus = (s) => {
    dispatch({ type: "server.set", patch: { status: s } });
    dispatch({ type: "audit.add", entry: { type: "server.status.change", target: s, details: {} } });
    toast(`服务状态已切换`, "ok");
  };

  const saveMaintenanceMessage = () => {
    dispatch({ type: "server.set", patch: {
      maintenanceMessage: draftMsg,
      estimatedEnd: endDate ? new Date(endDate).getTime() : null,
    }});
    toast("维护信息已保存", "ok");
  };

  return (
    <div className="page" data-screen-label="02 服务器控制">
      <div className="page-head">
        <div>
          <div className="page-title">服务器控制</div>
          <div className="page-sub">切换全局状态 · 维护提示 · 下载功能开关</div>
        </div>
      </div>

      <div className="grid grid-2" style={{ marginBottom: 14 }}>
        <Card title="运行状态" subtitle="客户端 /init 握手会读取此状态并据此响应">
          <div className="grid" style={{ gridTemplateColumns: "1fr 1fr 1fr", gap: 10 }}>
            {[
              { v: "ok",   label: "正常",     desc: "全部接口可用", cls: "ok" },
              { v: "warn", label: "维护中",   desc: "客户端将进入维护页面", cls: "warn" },
              { v: "err",  label: "异常",     desc: "拒绝所有握手请求", cls: "err" },
            ].map(s => (
              <button key={s.v} type="button"
                onClick={() => setStatus(s.v)}
                style={{
                  textAlign: "left",
                  cursor: "pointer",
                  padding: 14,
                  borderRadius: 8,
                  background: server.status === s.v ? "rgba(107,33,168,0.12)" : "var(--bg-2)",
                  border: `1px solid ${server.status === s.v ? "var(--purple-500)" : "var(--border-2)"}`,
                  color: "inherit",
                  fontFamily: "inherit",
                }}>
                <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", marginBottom: 8 }}>
                  <StatusBadge status={s.cls}>{s.label}</StatusBadge>
                  {server.status === s.v && <span style={{ color: "var(--purple-300)" }}><I.Check size={14}/></span>}
                </div>
                <div style={{ fontSize: 12, color: "var(--text-3)" }}>{s.desc}</div>
              </button>
            ))}
          </div>

          {server.status === "warn" && (
            <div className="fade-in" style={{ marginTop: 18, paddingTop: 18, borderTop: "1px solid var(--border-1)" }}>
              <div className="field" style={{ marginBottom: 12 }}>
                <label className="field-label"><I.Info size={12}/> 维护提示文案</label>
                <textarea className="textarea" rows={3} value={draftMsg} onChange={(e) => setDraftMsg(e.target.value)} />
                <div className="field-hint">显示在客户端启动界面。支持换行,不支持富文本。</div>
              </div>
              <div className="field" style={{ marginBottom: 12 }}>
                <label className="field-label"><I.Clock size={12}/> 预计结束时间(可选)</label>
                <input className="input" type="datetime-local" value={endDate} onChange={(e) => setEndDate(e.target.value)} />
                <div className="field-hint">若设置,客户端会显示倒计时;留空表示「时间待定」。</div>
              </div>
              <button className="btn btn-primary" onClick={saveMaintenanceMessage}><I.Check size={13}/> 保存维护信息</button>
            </div>
          )}

          {server.status === "err" && (
            <div className="fade-in" style={{ marginTop: 18, paddingTop: 18, borderTop: "1px solid rgba(239,68,68,0.25)" }}>
              <div style={{ display: "flex", gap: 10, color: "var(--red-400)", fontSize: 12.5 }}>
                <I.Alert size={16}/>
                <div>
                  异常状态会让所有 <span className="mono">/init</span> 请求返回 503。请确认已经处理服务端依赖后再切换回正常。
                </div>
              </div>
            </div>
          )}
        </Card>

        <Card title="下载功能开关" subtitle="控制资源分发渠道是否对客户端开放">
          <div style={{ display: "flex", flexDirection: "column", gap: 12 }}>
            <div style={{
              padding: 14, borderRadius: 8,
              border: `1px solid ${server.onlineDownloadEnabled ? "rgba(94,234,212,0.3)" : "var(--border-2)"}`,
              background: server.onlineDownloadEnabled ? "rgba(13,148,136,0.06)" : "var(--bg-2)",
            }}>
              <div style={{ display: "flex", alignItems: "center", gap: 12 }}>
                <I.Cloud size={18} style={{ color: server.onlineDownloadEnabled ? "var(--teal-300)" : "var(--text-3)" }}/>
                <div style={{ flex: 1 }}>
                  <div style={{ fontWeight: 600, fontSize: 13 }}>在线下载</div>
                  <div style={{ color: "var(--text-3)", fontSize: 12, marginTop: 2 }}>从镜像列表分块拉取资源</div>
                </div>
                <Switch value={server.onlineDownloadEnabled} onChange={(v) => {
                  dispatch({ type: "server.set", patch: { onlineDownloadEnabled: v } });
                  dispatch({ type: "audit.add", entry: { type: "feature.toggle", target: "online_download", details: { to: v } } });
                  toast(`在线下载已${v ? "开启" : "关闭"}`, "ok");
                }}/>
              </div>
            </div>

            <div style={{
              padding: 14, borderRadius: 8,
              border: `1px solid ${server.offlinePackageEnabled ? "rgba(94,234,212,0.3)" : "var(--border-2)"}`,
              background: server.offlinePackageEnabled ? "rgba(13,148,136,0.06)" : "var(--bg-2)",
            }}>
              <div style={{ display: "flex", alignItems: "center", gap: 12 }}>
                <I.HardDrive size={18} style={{ color: server.offlinePackageEnabled ? "var(--teal-300)" : "var(--text-3)" }}/>
                <div style={{ flex: 1 }}>
                  <div style={{ fontWeight: 600, fontSize: 13 }}>离线整包</div>
                  <div style={{ color: "var(--text-3)", fontSize: 12, marginTop: 2 }}>客户端直接下载完整 zip 包</div>
                </div>
                <Switch value={server.offlinePackageEnabled} onChange={(v) => {
                  dispatch({ type: "server.set", patch: { offlinePackageEnabled: v } });
                  dispatch({ type: "audit.add", entry: { type: "feature.toggle", target: "offline_package", details: { to: v } } });
                  toast(`离线整包已${v ? "开启" : "关闭"}`, "ok");
                }}/>
              </div>
            </div>

            {!server.onlineDownloadEnabled && !server.offlinePackageEnabled && (
              <div className="fade-in" style={{
                padding: 14, borderRadius: 8,
                background: "rgba(217,119,6,0.08)",
                border: "1px solid rgba(251,191,36,0.3)",
              }}>
                <div style={{ display: "flex", gap: 10, color: "var(--amber-400)", marginBottom: 8 }}>
                  <I.Alert size={16}/>
                  <span style={{ fontSize: 12.5, fontWeight: 600 }}>所有下载渠道均已关闭,客户端将收到此提示:</span>
                </div>
                <textarea className="textarea" rows={2} value={draftDisabled} onChange={(e) => setDraftDisabled(e.target.value)}/>
                <button className="btn btn-sm btn-primary" style={{ marginTop: 8 }} onClick={() => {
                  dispatch({ type: "server.set", patch: { disabledMessage: draftDisabled } });
                  toast("禁用提示已保存", "ok");
                }}>保存</button>
              </div>
            )}
          </div>
        </Card>
      </div>

      <ServicesCard/>

      <LimitsCard/>

      <Card title="客户端最终下发预览" subtitle="发送至 /init 接口的响应片段(只读)">
        <pre className="code-block">{JSON.stringify({
          status: server.status,
          maintenance: server.status === "warn" ? {
            message: server.maintenanceMessage,
            estimated_end: server.estimatedEnd,
          } : null,
          download: {
            online_enabled: server.onlineDownloadEnabled,
            offline_enabled: server.offlinePackageEnabled,
            disabled_message: (!server.onlineDownloadEnabled && !server.offlinePackageEnabled) ? server.disabledMessage : null,
          },
        }, null, 2)}</pre>
      </Card>
    </div>
  );
}

/* ====================== Services 运行时地址下发 ====================== */
// /client/init 响应里的 services 对象,由握手时一次性下发给客户端。
// 三字段全部可选;未设置时服务端会省略 key。详见客户端 API 变动说明 §1。
function ServicesCard() {
  const { state, dispatch } = useApp();
  const toast = useToast();
  const svc = state.services || {};
  const [capUrl,  setCapUrl]  = useState(svc.cap_worker_url || "");
  const [gameHost, setGameHost] = useState(svc.game_server_host || "");
  // proxy_backends 编辑用 textarea,每行一条
  const [proxyText, setProxyText] = useState((svc.proxy_backends || []).join("\n"));
  useEffect(() => { setCapUrl(svc.cap_worker_url || ""); }, [svc.cap_worker_url]);
  useEffect(() => { setGameHost(svc.game_server_host || ""); }, [svc.game_server_host]);
  useEffect(() => { setProxyText((svc.proxy_backends || []).join("\n")); }, [svc.proxy_backends]);

  const save = () => {
    const proxies = proxyText.split(/\r?\n/).map(s => s.trim()).filter(Boolean);
    // cap_worker_url / proxy_backends:完整 URL,要求 http(s)://
    for (const u of [capUrl, ...proxies]) {
      if (u && !/^https?:\/\//.test(u)) {
        toast(`URL 必须以 http(s):// 开头: ${u}`, "err");
        return;
      }
    }
    // game_server_host:**纯 host**,不带 scheme(2026-05-29 客户端代理回退修复)
    const host = gameHost.trim();
    if (host) {
      if (/^https?:\/\//.test(host)) {
        toast("game_server_host 必须是纯 host,不带 https://(客户端会自动拼)", "err");
        return;
      }
      if (/[/?#]/.test(host)) {
        toast("game_server_host 不能含路径/查询/片段", "err");
        return;
      }
      if (!/^[a-zA-Z0-9](?:[a-zA-Z0-9.-]*[a-zA-Z0-9])?(?::\d{1,5})?$/.test(host)) {
        toast(`game_server_host 格式不合法: ${host}`, "err");
        return;
      }
    }
    dispatch({ type: "services.set", patch: {
      cap_worker_url:   capUrl.trim(),
      proxy_backends:   proxies,
      game_server_host: host,
    }});
    toast("services 下发配置已保存,下次客户端握手生效", "ok");
  };

  // 当配了代理但 game_server_host 为空时显眼警示 ——
  // 客户端代理耗尽后会静默放弃重定向,落到死域名。
  const proxiesNonEmpty = proxyText.split(/\r?\n/).some(s => s.trim());
  const missingFallback = proxiesNonEmpty && !gameHost.trim();

  return (
    <Card title="握手期下发的运行时地址(services)"
      subtitle="/client/init 响应里 services 对象;cap_worker_url 与 proxy_backends 可选,game_server_host 强烈建议配置"
      style={{ marginTop: 14 }}>
      <div className="field" style={{ marginBottom: 12 }}>
        <label className="field-label"><I.Shield size={12}/> cap-worker 地址</label>
        <input className="input mono" placeholder="https://captcha.magireco.top"
          value={capUrl} onChange={(e) => setCapUrl(e.target.value)}/>
        <div className="field-hint">人机验证服务地址(完整 URL)。留空 → 客户端回退到 APK 内置的 CNV_CAP_WORKER_URL。</div>
      </div>
      <div className="field" style={{ marginBottom: 12 }}>
        <label className="field-label"><I.Tag size={12}/> 私服后端 host (game_server_host)</label>
        <input className="input mono" placeholder="totentanz-9b.magica-us.com"
          value={gameHost} onChange={(e) => setGameHost(e.target.value)}/>
        <div className="field-hint">
          <strong>纯 host,不含 <span className="mono">https://</span></strong> ——
          客户端会自己拼。同时承担两个作用:① 精确匹配仅重写发往该 host 的请求;
          ② 代理列表全部失败时回退到 <span className="mono">https://&lt;host&gt;</span>。
          <span style={{ color: "var(--red-400)" }}>留空时代理耗尽会静默放弃,请求落到死域名。</span>
        </div>
      </div>
      <div className="field" style={{ marginBottom: 12 }}>
        <label className="field-label"><I.Cloud size={12}/> 游戏代理后端 (proxy_backends)</label>
        <textarea className="input mono" rows={4}
          placeholder={"每行一个反向代理 URL,客户端按从上到下顺序尝试\nhttps://proxy1.example.com\nhttps://proxy2.example.com"}
          style={{ width: "100%", resize: "vertical" }}
          value={proxyText} onChange={(e) => setProxyText(e.target.value)}/>
        <div className="field-hint">每行一条;全部失败时回退到上面的 game_server_host(若也为空 → 连接死域名失败)。代理必须透传 path/query/body。</div>
      </div>
      {missingFallback && (
        <div style={{
          padding: "10px 12px", marginBottom: 12, borderRadius: 6,
          background: "rgba(239,68,68,0.08)", border: "1px solid rgba(239,68,68,0.3)",
          color: "var(--red-400)", fontSize: 12.5, display: "flex", gap: 8, alignItems: "flex-start",
        }}>
          <I.Alert size={14} style={{ marginTop: 2, flexShrink: 0 }}/>
          <span>
            配置了代理列表却没填 <span className="mono">game_server_host</span> ——
            代理全部失败后客户端会落回 native 内置的已停服域名,游戏无法继续。
            请补上私服后端 host。
          </span>
        </div>
      )}
      <button className="btn btn-primary" onClick={save}><I.Check size={13}/> 保存并下发</button>
    </Card>
  );
}
// LimitsCard 运行时可调的两个大小上限:全局请求体上限 + 热更新包上传/下载上限。
// 改动经 /admin/limits 落库并即时生效(全局上限接到中间件 atomic,热更新由其 handler 读取)。
function LimitsCard() {
  const { state, dispatch } = useApp();
  const toast = useToast();
  const limits = state.limits || { globalBodyMB: 8, hotUpdateMB: 1024 };
  const [gBody, setGBody] = useState(String(limits.globalBodyMB));
  const [hot, setHot] = useState(String(limits.hotUpdateMB));
  useEffect(() => { setGBody(String(limits.globalBodyMB)); }, [limits.globalBodyMB]);
  useEffect(() => { setHot(String(limits.hotUpdateMB)); }, [limits.hotUpdateMB]);

  const clamp = (v, lo, hi, def) => {
    let n = parseInt(v, 10);
    if (isNaN(n)) n = def;
    return Math.min(hi, Math.max(lo, n));
  };

  const save = () => {
    const g = clamp(gBody, 1, 4096, 8);
    const h = clamp(hot, 1, 8192, 1024);
    setGBody(String(g)); setHot(String(h));
    dispatch({ type: "limits.set", value: { globalBodyMB: g, hotUpdateMB: h } });
    dispatch({ type: "audit.add", entry: { type: "limits.update", target: "", details: { global_body_mb: g, hotupdate_mb: h } } });
    toast("大小上限已保存并即时生效", "ok");
  };

  const dirty = String(limits.globalBodyMB) !== gBody.trim() || String(limits.hotUpdateMB) !== hot.trim();

  return (
    <Card title="请求体与上传上限" subtitle="运行时可调 · 保存后即时生效,无需重启节点" style={{ marginTop: 14 }}>
      <div style={{ padding: "14px 16px", display: "flex", flexDirection: "column", gap: 16 }}>
        <div style={{ display: "grid", gridTemplateColumns: "1fr auto", gap: 14, alignItems: "center" }}>
          <div>
            <div style={{ fontWeight: 600, fontSize: 13 }}>全局请求体上限</div>
            <div style={{ color: "var(--text-3)", fontSize: 12, marginTop: 2 }}>
              所有 API 请求体的统一上限(热更新上传单独走下面的上限)。允许范围 1–4096 MiB。过小会影响云存档上传等。
            </div>
          </div>
          <div style={{ display: "flex", alignItems: "center", gap: 6 }}>
            <input type="number" className="input mono" style={{ width: 110, textAlign: "right" }}
              value={gBody} min={1} max={4096}
              onChange={(e) => setGBody(e.target.value)}/>
            <span style={{ color: "var(--text-3)", fontSize: 12, fontFamily: "var(--mono)" }}>MiB</span>
          </div>
        </div>

        <div style={{ borderTop: "1px solid var(--border-1)" }}/>

        <div style={{ display: "grid", gridTemplateColumns: "1fr auto", gap: 14, alignItems: "center" }}>
          <div>
            <div style={{ fontWeight: 600, fontSize: 13 }}>热更新包上限</div>
            <div style={{ color: "var(--text-3)", fontSize: 12, marginTop: 2 }}>
              「热更新」页直链下载 / 上传文件(cn_js_update.zip、cn_scenario_update.zip)的单包上限。允许范围 1–8192 MiB。
            </div>
          </div>
          <div style={{ display: "flex", alignItems: "center", gap: 6 }}>
            <input type="number" className="input mono" style={{ width: 110, textAlign: "right" }}
              value={hot} min={1} max={8192}
              onChange={(e) => setHot(e.target.value)}/>
            <span style={{ color: "var(--text-3)", fontSize: 12, fontFamily: "var(--mono)" }}>MiB</span>
          </div>
        </div>

        <div style={{ display: "flex", justifyContent: "flex-end" }}>
          <button className="btn btn-primary" onClick={save} disabled={!dirty}>
            <I.Check size={13}/> 保存上限
          </button>
        </div>
      </div>
    </Card>
  );
}

Object.assign(window, { PageServerControl });
