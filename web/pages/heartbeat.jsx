/* 心跳监控页。
 *
 * 数据真相对齐客户端 ResourceFlow.HeartbeatSender / SaveOverlayService.sendGameHeartbeat:
 *   - 在线资源下载、热更新：客户端自己跑下载 worker,每 5 秒上报 files[](每项
 *     name/status/percent/speed_bps)。整体进度/速度/当前文件由 files 聚合。
 *   - 游戏内：每 5 秒上报空 files[](仅为收取封禁/维护指令),无下载进度与速度。
 *   - 离线整包：由系统浏览器下载、客户端只做文件导入,全程不发心跳——因此
 *     **不会出现在本列表**,更不存在“离线包下载速度”。
 *
 * 列表每 5 秒轮询 /admin/heartbeats(暂停时停轮询)。initialHeartbeats 仅作首帧/
 * 离线演示种子,首个成功轮询后即被真实数据替换。
 */

// 三类心跳活动的展示元数据。type 由后端按 phase + 文件名推导(client.HBState.Kind)。
const HB_TYPE_META = {
  online:    { label: "在线下载", cls: "badge-info", icon: "Download" },
  hotupdate: { label: "热更新",   cls: "badge-warn", icon: "Zap" },
  game:      { label: "游戏中",   cls: "badge-ok",   icon: "Heart" },
};

function PageHeartbeat() {
  const { state, dispatch } = useApp();
  const toast = useToast();
  const { heartbeats } = state;
  const [paused, setPaused] = useState(false);
  const [expanded, setExpanded] = useState(null);
  const [confirmBan, setConfirmBan] = useState(null);
  const [confirmSwitch, setConfirmSwitch] = useState(null);

  // 每 5 秒拉取真实心跳;暂停时停轮询。拉取失败(离线演示无后端)保留当前列表。
  useEffect(() => {
    if (paused) return;
    let cancelled = false;
    const pull = () => {
      if (typeof fetchHeartbeats !== "function") return;
      fetchHeartbeats()
        .then(rows => { if (!cancelled) dispatch({ type: "heartbeats.set", value: rows }); })
        .catch(() => {});
    };
    pull();
    const t = setInterval(pull, 5000);
    return () => { cancelled = true; clearInterval(t); };
  }, [paused, dispatch]);

  const downloadCount = heartbeats.filter(h => h.type !== "game").length;
  const gameCount = heartbeats.length - downloadCount;

  const onForceSwitch = () => {
    toast(`设备 ${confirmSwitch.deviceId.slice(0, 8)}… 已被强制切换到下一个镜像`, "ok");
    dispatch({ type: "audit.add", entry: { type: "heartbeat.force_switch", target: confirmSwitch.deviceId.slice(0, 16), details: {} } });
    setConfirmSwitch(null);
  };

  const onBanFromHB = () => {
    const b = {
      id: `ban_${Date.now()}`,
      deviceId: confirmBan.deviceId,
      reason: "心跳监控中手工封禁",
      issuedAt: Date.now(),
      expireTime: Date.now() + 24 * 3600e3,
      issuedBy: "admin_homura",
      auto: false,
    };
    dispatch({ type: "bans.add", value: b });
    dispatch({ type: "heartbeat.remove", id: confirmBan.id });
    dispatch({ type: "audit.add", entry: { type: "device.ban", target: confirmBan.deviceId.slice(0, 16), details: { source: "heartbeat" } } });
    toast("设备已封禁并从心跳列表移除", "ok");
    setConfirmBan(null);
  };

  return (
    <div className="page" data-screen-label="08 心跳监控">
      <div className="page-head">
        <div>
          <div className="page-title">心跳监控</div>
          <div className="page-sub">客户端自行下载资源 / 热更新 / 在线游戏时上报的心跳 · 每 5 秒自动刷新</div>
        </div>
        <div className="actions">
          <span className="badge-pill badge-info" title="正在通过客户端下载资源或热更新的设备">
            <span className="dot"/>{downloadCount} 台下载中
          </span>
          <span className="badge-pill badge-ok" title="已进入游戏、仅上报在线心跳的设备">
            <span className="dot"/>{gameCount} 台游戏中
          </span>
          <button className={`btn ${paused ? "btn-primary" : ""}`} onClick={() => setPaused(p => !p)}>
            {paused ? <><I.Play size={13}/> 恢复刷新</> : <><I.Pause size={13}/> 暂停刷新</>}
          </button>
        </div>
      </div>

      {/* 离线整包不在心跳范围内的说明——避免误以为缺数据。 */}
      <div style={{
        display: "flex", alignItems: "flex-start", gap: 8,
        padding: "9px 12px", marginBottom: 12,
        background: "var(--bg-2)", border: "1px solid var(--border-1)", borderRadius: 8,
        color: "var(--text-3)", fontSize: 12, lineHeight: 1.5,
      }}>
        <I.Info size={13} style={{ marginTop: 2, flexShrink: 0, color: "var(--accent-600)" }}/>
        <span>
          仅<strong>客户端自行下载</strong>的场景（在线资源下载、热更新）会上报逐文件进度与速度。
          <strong>离线整包</strong>由系统浏览器下载、客户端只做文件导入，全程不经手、不上报心跳，
          因此<strong>不会出现在此列表，也没有“下载速度”</strong>。
        </span>
      </div>

      <Card tight>
        <div className="table-wrap">
        <table className="table">
          <thead>
            <tr>
              <th></th>
              <th>设备 ID</th>
              <th>类型</th>
              <th>整体进度</th>
              <th>当前文件</th>
              <th>速度</th>
              <th>最后心跳</th>
              <th className="actions-cell">操作</th>
            </tr>
          </thead>
          <tbody>
            {heartbeats.map(h => {
              const meta = HB_TYPE_META[h.type] || HB_TYPE_META.game;
              const TypeIcon = I[meta.icon];
              const isDownload = h.type !== "game";
              const files = h.files || [];
              const canExpand = files.length > 0;
              const open = expanded === h.id && canExpand;
              const isStale = (Date.now() - h.lastHeartbeat) > 15000;
              return (
                <React.Fragment key={h.id}>
                  <tr>
                    <td style={{ width: 28 }}>
                      {canExpand ? (
                        <button className="btn btn-ghost btn-icon btn-sm"
                          onClick={() => setExpanded(open ? null : h.id)}>
                          {open ? <I.ChevDown size={12}/> : <I.ChevRight size={12}/>}
                        </button>
                      ) : null}
                    </td>
                    <td>
                      <span style={{ display: "inline-flex", alignItems: "center", gap: 6, whiteSpace: "nowrap" }}>
                        <span className="mono" style={{ fontSize: 12 }}>
                          {h.deviceId.slice(0, 8)}…{h.deviceId.slice(-6)}
                        </span>
                        <CopyValue iconOnly value={h.deviceId} mono={false} label="设备 ID"/>
                      </span>
                    </td>
                    <td>
                      <span className={`badge-pill ${meta.cls}`} style={{ fontSize: 10.5 }}>
                        <TypeIcon size={11}/> {meta.label}
                      </span>
                    </td>
                    <td style={{ minWidth: 200 }}>
                      {isDownload ? (
                        <div style={{ display: "flex", alignItems: "center", gap: 10 }}>
                          <div className="progress" style={{ flex: 1 }}><div style={{ width: `${h.progress}%` }}/></div>
                          <span className="mono" style={{ fontSize: 11.5, minWidth: 42, color: "var(--text-2)" }}>{(h.progress || 0).toFixed(1)}%</span>
                        </div>
                      ) : (
                        <span style={{ fontSize: 12, color: "var(--text-3)" }}>游戏运行中 · 无下载任务</span>
                      )}
                    </td>
                    <td>
                      <span className="mono" style={{ fontSize: 11.5, color: isDownload ? "var(--text-2)" : "var(--text-disabled)" }}>
                        {isDownload ? (h.currentFile || "—") : "—"}
                      </span>
                    </td>
                    <td className="num">
                      {isDownload
                        ? <span className="mono" style={{ fontSize: 12 }}>{fmt.speed(h.speedBps)}</span>
                        : <span className="mono" style={{ fontSize: 12, color: "var(--text-disabled)" }}>—</span>}
                    </td>
                    <td>
                      <span style={{ color: isStale ? "var(--amber-400)" : "var(--text-3)", fontSize: 12, fontFamily: "var(--mono)" }}>
                        {isStale ? <><I.Alert size={11}/> {fmt.ago(h.lastHeartbeat)}</> : fmt.ago(h.lastHeartbeat)}
                      </span>
                    </td>
                    <td className="actions-cell">
                      <div style={{ display: "inline-flex", gap: 4 }}>
                        {isDownload && (
                          <button className="btn btn-sm" title="强制切换镜像" onClick={() => setConfirmSwitch(h)}>
                            <I.Refresh size={11}/> 切换镜像
                          </button>
                        )}
                        <button className="btn btn-sm btn-danger" title="立即封禁设备" onClick={() => setConfirmBan(h)}>
                          <I.Ban size={11}/> 封禁
                        </button>
                      </div>
                    </td>
                  </tr>
                  {open && (
                    <tr className="expand-row fade-in">
                      <td colSpan={8} style={{ padding: 0 }}>
                        <div className="expand-inner">
                          <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", marginBottom: 10 }}>
                            <div className="kv-label">
                              {h.type === "hotupdate" ? "热更新文件" : "下载文件列表"} · 共 {files.length} 项
                            </div>
                            <div style={{ display: "flex", gap: 8, fontSize: 11.5, color: "var(--text-3)" }}>
                              <span>完成 <span style={{ color: "var(--teal-300)" }}>{files.filter(f=>f.status==="done").length}</span></span>
                              <span>下载中 <span style={{ color: "var(--purple-300)" }}>{files.filter(f=>f.status==="downloading").length}</span></span>
                              <span>等待 <span style={{ color: "var(--text-2)" }}>{files.filter(f=>f.status==="pending").length}</span></span>
                              <span>失败 <span style={{ color: "var(--red-400)" }}>{files.filter(f=>f.status==="failed").length}</span></span>
                            </div>
                          </div>
                          <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: 6 }}>
                            {files.map((f, i) => (
                              <div key={i} style={{
                                display: "flex", alignItems: "center", gap: 8,
                                padding: "6px 10px",
                                background: "var(--bg-1)",
                                border: "1px solid var(--border-1)",
                                borderRadius: 4,
                                fontSize: 12,
                              }}>
                                <span className="mono" style={{ flex: 1, color: "var(--text-2)", overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>{f.name}</span>
                                {f.status === "downloading" && (
                                  <span className="mono" style={{ fontSize: 10.5, color: "var(--text-3)" }}>
                                    {(f.percent || 0).toFixed(0)}% · {fmt.speed(f.speedBps)}
                                  </span>
                                )}
                                <FileChip status={f.status}/>
                              </div>
                            ))}
                          </div>
                        </div>
                      </td>
                    </tr>
                  )}
                </React.Fragment>
              );
            })}
            {heartbeats.length === 0 && (
              <tr><td colSpan={8} style={{ textAlign: "center", padding: 40, color: "var(--text-3)" }}>当前没有在线客户端</td></tr>
            )}
          </tbody>
        </table>
        </div>
      </Card>

      <ConfirmDialog
        open={!!confirmBan}
        danger
        title={confirmBan && `封禁设备 ${confirmBan.deviceId.slice(0, 8)}…`}
        confirmText="立即封禁 24 小时"
        message={
          <>该设备会被加入活跃封禁列表,默认时长 24 小时,理由「心跳监控中手工封禁」。如需自定义,请到「设备管理」页面封禁。</>
        }
        onCancel={() => setConfirmBan(null)}
        onConfirm={onBanFromHB}
      />

      <ConfirmDialog
        open={!!confirmSwitch}
        danger={false}
        title={confirmSwitch && `强制切换镜像 · ${confirmSwitch.deviceId.slice(0,8)}…`}
        confirmText="强制切换"
        message={<>客户端会立即放弃当前镜像,从镜像列表的下一项重新拉取。剩余分块进度会保留。</>}
        onCancel={() => setConfirmSwitch(null)}
        onConfirm={onForceSwitch}
      />
    </div>
  );
}

function FileChip({ status }) {
  const map = {
    done: { cls: "badge-ok", label: "完成" },
    downloading: { cls: "badge-info", label: "下载中" },
    pending: { cls: "badge-muted", label: "等待" },
    failed: { cls: "badge-err", label: "失败" },
  };
  const m = map[status] || map.pending;
  return <span className={`badge-pill ${m.cls}`} style={{ fontSize: 10.5 }}><span className="dot"/>{m.label}</span>;
}

Object.assign(window, { PageHeartbeat });
