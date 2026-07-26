function PageDevices() {
  const { state, dispatch } = useApp();
  const toast = useToast();
  const { activeBans, banHistory } = state;
  const [tab, setTab] = useState("active");
  const [q, setQ] = useState("");
  const [page, setPage] = useState(1);
  const pageSize = 12;

  const [banModal, setBanModal] = useState(false);
  const [banForm, setBanForm] = useState({ deviceId: "", reason: "", duration: "24h", custom: "" });
  const [confirmLift, setConfirmLift] = useState(null);
  const [, force] = useState(0);

  useEffect(() => { const t = setInterval(() => force(n => n + 1), 30000); return () => clearInterval(t); }, []);

  const filterFn = (b) => !q || b.deviceId.includes(q) || (b.reason || "").includes(q);

  const list = tab === "active" ? activeBans.filter(filterFn) : banHistory.filter(filterFn);
  const paged = list.slice((page - 1) * pageSize, page * pageSize);

  const durationToMs = (d, custom) => {
    if (d === "1h") return 3600e3;
    if (d === "24h") return 86400e3;
    if (d === "7d") return 7 * 86400e3;
    if (d === "30d") return 30 * 86400e3;
    if (d === "permanent") return null;
    if (d === "custom" && custom) return new Date(custom).getTime() - Date.now();
    return 86400e3;
  };

  const submitBan = () => {
    if (!banForm.deviceId || !banForm.reason) { toast("请填写设备 ID 与封禁理由", "err"); return; }
    const ms = durationToMs(banForm.duration, banForm.custom);
    const expire = ms === null ? null : Date.now() + ms;
    const entry = {
      id: `ban_${Date.now()}`,
      deviceId: banForm.deviceId.toLowerCase().replace(/[^a-f0-9]/g, ""),
      reason: banForm.reason,
      issuedAt: Date.now(),
      expireTime: expire,
      issuedBy: "admin_homura",
      auto: false,
    };
    if (entry.deviceId.length < 8) { toast("设备 ID 似乎不合法", "err"); return; }
    dispatch({ type: "bans.add", value: entry });
    dispatch({ type: "audit.add", entry: { type: "device.ban", target: entry.deviceId.slice(0, 16), details: { reason: entry.reason, expire } } });
    dispatch({ type: "events.push", entry: { kind: "ban", text: `设备 ${entry.deviceId.slice(0,4)}…${entry.deviceId.slice(-4)} 被管理员封禁`, actor: "admin_homura", icon: "Ban", color: "amber" } });
    setBanModal(false);
    setBanForm({ deviceId: "", reason: "", duration: "24h", custom: "" });
    toast(`设备已封禁`, "ok");
  };

  const liftBan = () => {
    const b = confirmLift;
    dispatch({ type: "bans.lift", id: b.id });
    dispatch({ type: "audit.add", entry: { type: "device.lift", target: b.deviceId.slice(0, 16), details: { reason: "管理员手工解封" } } });
    dispatch({ type: "events.push", entry: { kind: "lift", text: `设备 ${b.deviceId.slice(0,4)}…${b.deviceId.slice(-4)} 解除封禁`, actor: "admin_homura", icon: "Shield", color: "teal" } });
    setConfirmLift(null);
    toast("封禁已解除", "ok");
  };

  return (
    <div className="page" data-screen-label="07 设备封禁">
      <div className="page-head">
        <div>
          <div className="page-title">设备管理与封禁</div>
          <div className="page-sub">基于设备指纹的封禁列表 · 支持自动判定与手工操作</div>
        </div>
        <div className="actions">
          <button className="btn btn-danger" onClick={() => setBanModal(true)}><I.Ban size={13}/> 封禁设备</button>
        </div>
      </div>

      <Card tight>
        <Tabs
          value={tab}
          onChange={(v) => { setTab(v); setPage(1); }}
          tabs={[
            { value: "active", label: "活跃封禁", count: activeBans.length },
            { value: "history", label: "历史记录", count: banHistory.length },
          ]}
        />
        <div className="toolbar">
          <div className="search-input">
            <span className="ico"><I.Search size={14}/></span>
            <input className="input" placeholder="搜索设备 ID 或封禁理由" value={q} onChange={(e) => { setQ(e.target.value); setPage(1); }}/>
          </div>
          <div style={{ flex: 1 }}/>
          <span style={{ color: "var(--text-3)", fontSize: 12, fontFamily: "var(--mono)" }}>结果 {list.length}</span>
        </div>
        <div className="table-wrap">
        <table className="table">
          <thead>
            <tr>
              <th>设备 ID</th>
              <th>封禁理由</th>
              <th>{tab === "active" ? "到期时间" : "结束时间"}</th>
              <th>{tab === "active" ? "封禁时间" : "封禁时间"}</th>
              <th>{tab === "active" ? "来源" : "操作记录"}</th>
              <th className="actions-cell">操作</th>
            </tr>
          </thead>
          <tbody>
            {paged.map(b => (
              <tr key={b.id}>
                <td>
                  <span style={{ display: "inline-flex", alignItems: "center", gap: 6, whiteSpace: "nowrap" }}>
                    <span className="mono" style={{ fontSize: 12 }}>{b.deviceId.slice(0, 8)}…{b.deviceId.slice(-6)}</span>
                    <CopyValue iconOnly value={b.deviceId} mono={false} label="设备 ID"/>
                  </span>
                </td>
                <td><span style={{ color: "var(--text-2)" }}>{b.reason}</span></td>
                <td>
                  {tab === "active"
                    ? (b.expireTime === null
                      ? <StatusBadge status="err">永久</StatusBadge>
                      : <span className="mono" style={{ fontSize: 12 }}>{fmt.countdown(b.expireTime)}</span>)
                    : <span className="mono" style={{ color: "var(--text-3)", fontSize: 11.5 }}>{fmt.dt(b.expiredAt)}</span>
                  }
                </td>
                <td><span className="mono" style={{ color: "var(--text-3)", fontSize: 11.5 }}>{fmt.dt(b.issuedAt)}</span></td>
                <td>
                  {b.auto
                    ? <StatusBadge status="info">Auto · system</StatusBadge>
                    : <span className="mono" style={{ fontSize: 12, color: "var(--text-2)" }}>{b.issuedBy}</span>}
                  {tab === "history" && b.liftedBy && (
                    <span className="mono" style={{ fontSize: 11, color: "var(--teal-300)", marginLeft: 6 }}>
                      · 由 {b.liftedBy} 解除
                    </span>
                  )}
                </td>
                <td className="actions-cell">
                  {tab === "active" && (
                    <button className="btn btn-sm" onClick={() => setConfirmLift(b)}><I.Shield size={12}/> 解除封禁</button>
                  )}
                </td>
              </tr>
            ))}
            {paged.length === 0 && (
              <tr><td colSpan={6} style={{ textAlign: "center", padding: 40, color: "var(--text-3)" }}>暂无记录</td></tr>
            )}
          </tbody>
        </table>
        </div>
        <Pagination total={list.length} page={page} pageSize={pageSize} onPage={setPage}/>
      </Card>

      <AutoBanThresholds/>

      {/* Ban modal */}
      <Modal
        open={banModal}
        title="封禁设备"
        subtitle="封禁后该设备所有 /init 请求会被立即拒绝"
        onClose={() => setBanModal(false)}
        wide
        danger
        icon={<I.Ban size={18}/>}
        footer={
          <>
            <button className="btn" onClick={() => setBanModal(false)}>取消</button>
            <button className="btn btn-danger" onClick={submitBan}><I.Ban size={13}/> 确认封禁</button>
          </>
        }
      >
        <div className="field">
          <label className="field-label">设备 ID <span style={{ color: "var(--text-3)", fontWeight: 400 }}>· SHA-1 设备指纹</span></label>
          <input className="input mono" placeholder="40 位十六进制" value={banForm.deviceId}
            onChange={(e) => setBanForm({ ...banForm, deviceId: e.target.value })}/>
        </div>
        <div className="field">
          <label className="field-label">封禁理由</label>
          <textarea className="textarea" rows={3} placeholder="例如 多账号异常切换"
            value={banForm.reason}
            onChange={(e) => setBanForm({ ...banForm, reason: e.target.value })}/>
        </div>
        <div className="field">
          <label className="field-label">封禁时长</label>
          <div style={{ display: "flex", gap: 6, flexWrap: "wrap" }}>
            {[
              { v: "1h", l: "1 小时" },
              { v: "24h", l: "24 小时" },
              { v: "7d", l: "7 天" },
              { v: "30d", l: "30 天" },
              { v: "permanent", l: "永久" },
              { v: "custom", l: "自定义" },
            ].map(opt => (
              <button key={opt.v} type="button"
                className={`btn ${banForm.duration === opt.v ? "btn-primary" : ""}`}
                onClick={() => setBanForm({ ...banForm, duration: opt.v })}>{opt.l}</button>
            ))}
          </div>
          {banForm.duration === "custom" && (
            <input className="input" type="datetime-local" style={{ marginTop: 8 }}
              value={banForm.custom} onChange={(e) => setBanForm({ ...banForm, custom: e.target.value })}/>
          )}
        </div>
      </Modal>

      <ConfirmDialog
        open={!!confirmLift}
        title={confirmLift && `解除设备 ${confirmLift.deviceId.slice(0, 8)}… 的封禁`}
        confirmText="解除封禁"
        danger={false}
        message={confirmLift && <>
          解除封禁后,该设备可以立即重新调用 <span className="mono">/init</span>。<br/>
          原封禁理由: <span style={{ color: "var(--text-2)" }}>{confirmLift.reason}</span>
        </>}
        onCancel={() => setConfirmLift(null)}
        onConfirm={liftBan}
      />
    </div>
  );
}
/* ── 自动封禁阈值(运行时可调,后端 config 表 "autoban")─────────────────────
 * 5 路信号各有 触发次数(max)/ 统计窗口(window_sec)/ 封禁时长(ttl_sec,0=永久)。
 * 改完保存即 PUT /admin/autoban,判定器带 5s 缓存,无需重启即生效。 */
const AUTOBAN_SIGNALS = [
  { key: "tamper",        label: "客户端篡改检测",   note: "signature 中途变更即时封;init 签名/渠道白名单不过累计到此次数" },
  { key: "heartbeat",     label: "心跳包高频伪造",   note: "单设备窗口内心跳次数达到阈值" },
  { key: "resource",      label: "异常资源请求频率", note: "单设备窗口内 /client/online-download 次数达到阈值" },
  { key: "captcha",       label: "未通过 cap-worker", note: "登录验证码连续失败次数达到阈值" },
  { key: "multi_account", label: "多账号异常切换",   note: "单设备窗口内登录的不同账号数达到阈值" },
];

const AUTOBAN_DEFAULTS = {
  enabled: true,
  tamper:        { max: 3,  window_sec: 1800, ttl_sec: 0 },
  heartbeat:     { max: 90, window_sec: 60,   ttl_sec: 86400 },
  resource:      { max: 30, window_sec: 300,  ttl_sec: 86400 },
  captcha:       { max: 3,  window_sec: 900,  ttl_sec: 3600 },
  multi_account: { max: 5,  window_sec: 1800, ttl_sec: 86400 },
};

function autobanFmtDur(sec) {
  sec = Math.max(0, Math.floor(sec || 0));
  if (sec === 0) return "永久";
  if (sec % 86400 === 0) return (sec / 86400) + " 天";
  if (sec % 3600 === 0) return (sec / 3600) + " 小时";
  if (sec % 60 === 0) return (sec / 60) + " 分钟";
  return sec + " 秒";
}

function AutoBanThresholds() {
  const { state, dispatch } = useApp();
  const toast = useToast();
  const base = (state.autoban && state.autoban.tamper) ? state.autoban : AUTOBAN_DEFAULTS;
  const [ab, setAb] = useState(base);
  useEffect(() => {
    setAb((state.autoban && state.autoban.tamper) ? state.autoban : AUTOBAN_DEFAULTS);
  }, [state.autoban]);

  const dirty = JSON.stringify(ab) !== JSON.stringify(base);

  const setRule = (sig, field, val) => {
    const n = Math.max(0, parseInt(val, 10) || 0);
    setAb({ ...ab, [sig]: { ...ab[sig], [field]: n } });
  };

  const save = () => {
    for (const s of AUTOBAN_SIGNALS) {
      const r = ab[s.key] || {};
      if (r.max < 1 || r.window_sec < 1) { toast(`${s.label}:触发次数与统计窗口需 ≥ 1`, "err"); return; }
    }
    dispatch({ type: "autoban.set", value: ab });
    toast("自动封禁阈值已保存", "ok");
  };

  return (
    <Card
      title="自动封禁阈值"
      subtitle="命中即写入设备封禁(Auto · system)· 改完即时生效,无需重启"
      style={{ marginTop: 14 }}
      actions={
        <div style={{ display: "flex", gap: 10, alignItems: "center" }}>
          <label style={{ display: "inline-flex", alignItems: "center", gap: 6, fontSize: 12.5, color: "var(--text-2)" }}>
            <input type="checkbox" checked={!!ab.enabled} onChange={(e) => setAb({ ...ab, enabled: e.target.checked })}
              style={{ width: 14, height: 14, accentColor: "var(--accent-600)" }}/>
            启用自动封禁
          </label>
          <button className="btn btn-sm" onClick={() => setAb(JSON.parse(JSON.stringify(AUTOBAN_DEFAULTS)))}>恢复默认</button>
          <button className="btn btn-primary btn-sm" disabled={!dirty} onClick={save}><I.Check size={12}/> 保存</button>
        </div>
      }
    >
      {!ab.enabled && (
        <div style={{ marginBottom: 10, padding: "8px 12px", borderRadius: 6, background: "var(--amber-50)", border: "1px solid var(--amber-200)", color: "var(--amber-600)", fontSize: 12 }}>
          自动封禁当前已关闭 —— 仅手工封禁生效;已存在的封禁不受影响。
        </div>
      )}
      <div className="table-wrap">
        <table className="table">
          <thead>
            <tr>
              <th>信号</th>
              <th style={{ width: 110 }}>触发次数</th>
              <th style={{ width: 230 }}>统计窗口</th>
              <th style={{ width: 230 }}>封禁时长(0=永久)</th>
            </tr>
          </thead>
          <tbody>
            {AUTOBAN_SIGNALS.map(s => {
              const r = ab[s.key] || {};
              return (
                <tr key={s.key}>
                  <td>
                    <div style={{ fontSize: 12.5, color: "var(--text-1)", fontWeight: 500 }}>{s.label}</div>
                    <div style={{ fontSize: 11, color: "var(--text-3)" }}>{s.note}</div>
                  </td>
                  <td>
                    <input className="input mono" style={{ width: 84 }} type="number" min="1"
                      value={r.max} onChange={(e) => setRule(s.key, "max", e.target.value)}/>
                  </td>
                  <td>
                    <span style={{ display: "inline-flex", alignItems: "center", gap: 8 }}>
                      <input className="input mono" style={{ width: 110 }} type="number" min="1"
                        value={r.window_sec} onChange={(e) => setRule(s.key, "window_sec", e.target.value)}/>
                      <span style={{ fontSize: 11, color: "var(--text-3)" }}>秒 · {autobanFmtDur(r.window_sec)}</span>
                    </span>
                  </td>
                  <td>
                    <span style={{ display: "inline-flex", alignItems: "center", gap: 8 }}>
                      <input className="input mono" style={{ width: 110 }} type="number" min="0"
                        value={r.ttl_sec} onChange={(e) => setRule(s.key, "ttl_sec", e.target.value)}/>
                      <span style={{ fontSize: 11, color: "var(--text-3)" }}>秒 · {autobanFmtDur(r.ttl_sec)}</span>
                    </span>
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
    </Card>
  );
}

Object.assign(window, { PageDevices });
