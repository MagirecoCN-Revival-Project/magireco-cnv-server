function PageResources() {
  const { state, dispatch } = useApp();
  const toast = useToast();
  const { mirrors, offlinePackage } = state;

  const [dragIdx, setDragIdx] = useState(null);
  const [overIdx, setOverIdx] = useState(null);
  // 新镜像表单:支持 http 与 s3 两种;s3 需要 bucket/region;files 可选(每行一个 key,或 key,size)
  const [newMirror, setNewMirror] = useState({
    kind: "http", url: "", group: "", bucket: "", region: "", filesText: "",
  });
  const [editFilesIdx, setEditFilesIdx] = useState(null); // 行内编辑 files 的索引
  const [filesDraft, setFilesDraft] = useState("");
  const [editLimitsIdx, setEditLimitsIdx] = useState(null); // 编辑限额的镜像索引
  const [limitsDraft, setLimitsDraft] = useState({ dailyGB: "", speedMbps: "" });
  const [showPublish, setShowPublish] = useState(false);

  // 每 5 秒拉取最新流量统计（带 stats 字段的镜像列表）
  useEffect(() => {
    let alive = true;
    const poll = async () => {
      if (!alive) return;
      try {
        const list = await fetchMirrorStats();
        if (alive) dispatch({ type: "mirrors.statsRefresh", mirrors: list });
      } catch (_) {}
    };
    poll();
    const t = setInterval(poll, 5000);
    return () => { alive = false; clearInterval(t); };
  }, []);
  // pubForm:发布离线包表单。min_version 是服务端要求客户端必须安装的最低版本号,
  // 与 version(此次发布的包版本)独立 —— 旧包仍兼容时 min_version 可不动。
  // 留空表示不下发版本门槛,客户端跳过版本检查。
  // 见 server-offline-pack-validation.md §3 / §5。
  const [pubForm, setPubForm] = useState({
    url: "", version: "", sha256: "", size: "", min_version: "",
  });

  // parseFilesText:把 textarea 的文本解析成 [{key,size}] 或 [string]。
  // 每行一个条目;允许 "key,size" 形式;空行忽略;# 开头注释行忽略。
  const parseFilesText = (txt) => {
    const out = [];
    for (const ln of (txt || "").split(/\r?\n/)) {
      const t = ln.trim();
      if (!t || t.startsWith("#")) continue;
      const comma = t.indexOf(",");
      if (comma < 0) {
        out.push(t);
      } else {
        const key = t.slice(0, comma).trim();
        const sizeStr = t.slice(comma + 1).trim();
        const size = parseInt(sizeStr, 10);
        if (!key) continue;
        out.push(Number.isFinite(size) ? { key, size } : key);
      }
    }
    return out;
  };
  const stringifyFiles = (files) => {
    if (!files || !files.length) return "";
    return files.map(f => {
      if (typeof f === "string") return f;
      if (f.size != null && f.size >= 0) return `${f.key},${f.size}`;
      return f.key;
    }).join("\n");
  };

  const addMirror = () => {
    const u = newMirror.url.trim();
    if (!u) return;
    if (!u.startsWith("http")) { toast("URL 必须以 http(s):// 开头", "err"); return; }
    const entry = { kind: newMirror.kind, url: u };
    if (newMirror.group.trim()) entry.group = newMirror.group.trim();
    if (newMirror.kind === "s3") {
      const b = newMirror.bucket.trim();
      if (!b) { toast("S3 镜像必须填写 bucket", "err"); return; }
      entry.bucket = b;
      if (newMirror.region.trim()) entry.region = newMirror.region.trim();
    }
    const files = parseFilesText(newMirror.filesText);
    if (files.length) entry.files = files;
    dispatch({ type: "mirrors.add", mirror: entry });
    setNewMirror({ kind: "http", url: "", group: "", bucket: "", region: "", filesText: "" });
    toast(`已添加 ${entry.kind === "s3" ? "S3" : "HTTP"} 镜像${files.length ? ` (含 ${files.length} 文件)` : ""}`, "ok");
  };

  const saveFilesEdit = () => {
    if (editFilesIdx == null) return;
    const files = parseFilesText(filesDraft);
    dispatch({ type: "mirrors.update", index: editFilesIdx, patch: { files: files.length ? files : undefined } });
    setEditFilesIdx(null);
    setFilesDraft("");
    toast(`已更新文件清单 (${files.length} 条)`, "ok");
  };

  const saveLimitsEdit = () => {
    if (editLimitsIdx == null) return;
    const m = mirrors[editLimitsIdx];
    if (!m) return;
    const dailyGB = parseFloat(limitsDraft.dailyGB) || 0;
    const speedMbps = parseFloat(limitsDraft.speedMbps) || 0;
    const dailyLimitBytes = dailyGB > 0 ? Math.round(dailyGB * 1024 * 1024 * 1024) : 0;
    const speedLimitBps = speedMbps > 0 ? Math.round(speedMbps * 1024 * 1024 / 8) : 0;
    dispatch({ type: "mirrors.setLimits", url: m.url, dailyLimitBytes, speedLimitBps });
    setEditLimitsIdx(null);
    toast("限额已保存", "ok");
  };

  const onDrop = (i) => {
    if (dragIdx == null || dragIdx === i) return;
    dispatch({ type: "mirrors.reorder", from: dragIdx, to: i });
    setDragIdx(null);
    setOverIdx(null);
    dispatch({ type: "audit.add", entry: { type: "mirror.reorder", target: "mirrors", details: {} } });
  };

  const submitPublish = () => {
    const { url, version, sha256, size, min_version } = pubForm;
    if (!url || !version || !sha256) { toast("请填写完整字段", "err"); return; }
    if (sha256.length !== 64) { toast("SHA-256 必须为 64 位十六进制", "err"); return; }
    const next = {
      url, version,
      sha256: sha256.toLowerCase(),
      size: Number(size) || 0,
      uploadedAt: Date.now(),
      // 始终把 min_version 字段带上(即便空串,服务端用空串代表"清除策略"),
      // 这样运维显式留空可以撤销之前设的版本门槛。
      min_version,
    };
    dispatch({ type: "offlinePackage.set", value: next });
    dispatch({ type: "audit.add", entry: { type: "offline.publish", target: version, details: { sha256: sha256.slice(0, 12) + "…", min_version } } });
    setShowPublish(false);
    setPubForm({ url: "", version: "", sha256: "", size: "" });
    toast(`离线包 ${version} 已发布`, "ok");
  };

  return (
    <div className="page" data-screen-label="04 资源管理">
      <div className="page-head">
        <div>
          <div className="page-title">资源管理</div>
          <div className="page-sub">在线下载镜像优先级 · S3 资源 token · 离线整包发布</div>
        </div>
      </div>

      <div style={{ alignItems: "start" }}>
        <Card title="在线下载镜像" subtitle={`${mirrors.length} 个 · 按组分线路 · 支持内联文件清单 · 按优先级顺序尝试`}
          actions={<span className="badge-pill"><I.Drag size={11}/> 拖拽排序</span>}>
          <div style={{ marginBottom: 10 }}>
            {mirrors.map((m, i) => {
              const fileCount = Array.isArray(m.files) ? m.files.length : 0;
              const groupName = m.group || "默认线路";
              const st = m.stats || {};
              const exceeded = !!st.exceeded;
              const speedLimited = !exceeded && st.speed_limit_bps > 0 && st.speed_bps > st.speed_limit_bps;
              const statusColor = exceeded ? "var(--red-500)" : speedLimited ? "var(--amber-500)" : "var(--teal-500)";
              const statusLabel = exceeded ? "超限" : speedLimited ? "限速" : "正常";
              const hasStats = st.speed_bps != null;
              return (
                <div key={i}
                  draggable
                  onDragStart={() => setDragIdx(i)}
                  onDragOver={(e) => { e.preventDefault(); setOverIdx(i); }}
                  onDragLeave={() => setOverIdx(null)}
                  onDrop={() => onDrop(i)}
                  onDragEnd={() => { setDragIdx(null); setOverIdx(null); }}
                  style={{ flexDirection: "column", gap: 0, paddingBottom: 8 }}
                  className={`reorder-row ${dragIdx === i ? "dragging" : ""} ${overIdx === i && dragIdx !== i ? "dragover" : ""}`}>
                  {/* 主行 */}
                  <div style={{ display: "flex", alignItems: "center", gap: 8, width: "100%" }}>
                    <span className="handle"><I.Drag size={14}/></span>
                    <span className="idx">#{i + 1}</span>
                    <span style={{
                      display: "inline-block", minWidth: 36, textAlign: "center",
                      padding: "1px 6px", borderRadius: 4, fontSize: 10, fontWeight: 600,
                      background: m.kind === "s3" ? "var(--amber-50)" : "var(--accent-50)",
                      color: m.kind === "s3" ? "var(--amber-600)" : "var(--accent-700)",
                      border: m.kind === "s3" ? "1px solid var(--amber-200)" : "1px solid var(--accent-200)",
                    }}>{m.kind === "s3" ? "S3" : "HTTP"}</span>
                    {/* 状态点 */}
                    {hasStats && (
                      <span title={statusLabel} style={{
                        width: 7, height: 7, borderRadius: "50%",
                        background: statusColor, flexShrink: 0,
                        boxShadow: `0 0 4px ${statusColor}`,
                      }}/>
                    )}
                    <div style={{ display: "flex", flexDirection: "column", minWidth: 0, flex: 1 }}>
                      <span className="mono" style={{ wordBreak: "break-all", fontSize: 12 }}>{m.url}</span>
                      <span className="mono" style={{ fontSize: 11, color: "var(--text-3)" }}>
                        组 = <span style={{ color: "var(--text-2)" }}>{groupName}</span>
                        {m.kind === "s3" && (
                          <>
                            {" · "}bucket={m.bucket || "—"}
                            {m.region ? ` · region=${m.region}` : ""}
                          </>
                        )}
                        {" · "}文件清单 = <span style={{
                          color: fileCount > 0 ? "var(--teal-600)" : "var(--text-3)",
                          fontWeight: fileCount > 0 ? 500 : 400,
                        }}>{fileCount > 0 ? `内联 ${fileCount} 条` : "走 S3 自发现"}</span>
                      </span>
                    </div>
                    <span style={{ display: "flex", gap: 4 }}>
                      <button className="btn btn-ghost btn-icon btn-sm" title="流量限额"
                        onClick={() => {
                          setEditLimitsIdx(i);
                          const lim = m.stats || {};
                          const dailyGB = lim.daily_limit_bytes > 0
                            ? (lim.daily_limit_bytes / 1024 / 1024 / 1024).toFixed(2)
                            : "";
                          const speedMbps = lim.speed_limit_bps > 0
                            ? (lim.speed_limit_bps * 8 / 1024 / 1024).toFixed(1)
                            : "";
                          setLimitsDraft({ dailyGB, speedMbps });
                        }}><I.Gauge size={12}/></button>
                      <button className="btn btn-ghost btn-icon btn-sm" title="编辑文件清单"
                        onClick={() => {
                          setEditFilesIdx(i);
                          setFilesDraft(stringifyFiles(m.files));
                        }}><I.Edit size={12}/></button>
                      <button className="btn btn-ghost btn-icon btn-sm" disabled={i === 0}
                        onClick={() => dispatch({ type: "mirrors.reorder", from: i, to: i - 1 })}><I.ArrowUp size={12}/></button>
                      <button className="btn btn-ghost btn-icon btn-sm" disabled={i === mirrors.length - 1}
                        onClick={() => dispatch({ type: "mirrors.reorder", from: i, to: i + 1 })}><I.ArrowDown size={12}/></button>
                      <button className="btn btn-ghost btn-icon btn-sm" onClick={() => {
                        dispatch({ type: "mirrors.remove", index: i });
                        toast("镜像已移除", "ok");
                      }}><I.Trash size={12}/></button>
                    </span>
                  </div>
                  {/* 流量统计行 */}
                  {hasStats && (
                    <div style={{
                      display: "flex", gap: 16, paddingLeft: 92, marginTop: 5,
                      fontSize: 11, fontFamily: "var(--mono)", color: "var(--text-3)",
                      flexWrap: "wrap",
                    }}>
                      <span title="当前速度">
                        <span style={{ color: "var(--accent-600)", fontWeight: 600 }}>
                          {fmt.speed(st.speed_bps || 0)}
                        </span>
                        {st.speed_limit_bps > 0 && (
                          <span style={{ color: "var(--text-disabled)" }}>
                            {" "}/ 限 {fmt.speed(st.speed_limit_bps)}
                          </span>
                        )}
                      </span>
                      <span title="今日用量">
                        今日 <span style={{ color: "var(--text-2)" }}>{fmt.bytes(st.today_bytes || 0)}</span>
                        {st.daily_limit_bytes > 0 && (
                          <span style={{ color: exceeded ? "var(--red-500)" : "var(--text-disabled)" }}>
                            {" "}/ 限 {fmt.bytes(st.daily_limit_bytes)}
                          </span>
                        )}
                      </span>
                      <span title="本月用量">本月 <span style={{ color: "var(--text-2)" }}>{fmt.bytes(st.month_bytes || 0)}</span></span>
                      <span title="累计用量">累计 <span style={{ color: "var(--text-2)" }}>{fmt.bytes(st.cum_bytes || 0)}</span></span>
                      {exceeded && (
                        <span style={{
                          color: "var(--red-500)", fontWeight: 600,
                          background: "var(--red-50)", padding: "0 5px", borderRadius: 4,
                        }}>日限额超出 · 已暂停派发</span>
                      )}
                      {speedLimited && !exceeded && (
                        <span style={{
                          color: "var(--amber-600)", fontWeight: 600,
                          background: "var(--amber-50)", padding: "0 5px", borderRadius: 4,
                        }}>速度超限 · 暂停新派发</span>
                      )}
                    </div>
                  )}
                </div>
              );
            })}
          </div>
          <div style={{ display: "flex", gap: 8, marginBottom: 8 }}>
            <select className="select input mono" style={{ width: 90 }}
              value={newMirror.kind}
              onChange={(e) => setNewMirror({ ...newMirror, kind: e.target.value })}>
              <option value="http">HTTP</option>
              <option value="s3">S3</option>
            </select>
            <input className="input mono" placeholder={newMirror.kind === "s3" ? "https://s3.ap-east-1.amazonaws.com" : "https://cdn3.example.com/res/"}
              value={newMirror.url}
              onChange={(e) => setNewMirror({ ...newMirror, url: e.target.value })}
              onKeyDown={(e) => e.key === "Enter" && addMirror()}/>
            <input className="input mono" placeholder="组名(留空=默认线路)" style={{ width: 180 }}
              value={newMirror.group}
              onChange={(e) => setNewMirror({ ...newMirror, group: e.target.value })}/>
            <button className="btn" onClick={addMirror}><I.Plus size={13}/> 添加</button>
          </div>
          {newMirror.kind === "s3" && (
            <div style={{ display: "flex", gap: 8, marginBottom: 8 }}>
              <input className="input mono" placeholder="bucket (例: magireco-res)" value={newMirror.bucket}
                onChange={(e) => setNewMirror({ ...newMirror, bucket: e.target.value })}/>
              <input className="input mono" placeholder="region (例: ap-east-1)" value={newMirror.region}
                onChange={(e) => setNewMirror({ ...newMirror, region: e.target.value })}/>
            </div>
          )}
          <details style={{ marginBottom: 4 }}>
            <summary style={{ cursor: "pointer", fontSize: 12, color: "var(--text-3)" }}>
              内联文件清单(可选,缺省时客户端会请求 S3 XML 自发现)
            </summary>
            <textarea className="input mono" rows={4}
              placeholder={"每行一个文件 key,可选用逗号附 size(字节)\n# 例:\ndata/master_2024.bin,1048576\ndata/scenario_001.zip"}
              style={{ marginTop: 6, width: "100%", resize: "vertical" }}
              value={newMirror.filesText}
              onChange={(e) => setNewMirror({ ...newMirror, filesText: e.target.value })}/>
          </details>
        </Card>
      </div>

      {/* 流量限额编辑 Modal */}
      <Modal
        open={editLimitsIdx != null}
        title={editLimitsIdx != null ? `流量限额 — ${mirrors[editLimitsIdx]?.url}` : ""}
        subtitle="0 或留空表示不限制；超限后该镜像不再被派发给新设备"
        onClose={() => setEditLimitsIdx(null)}
        footer={
          <>
            <button className="btn" onClick={() => setEditLimitsIdx(null)}>取消</button>
            <button className="btn btn-primary" onClick={saveLimitsEdit}>
              <I.Check size={13}/> 保存
            </button>
          </>
        }
      >
        <div className="field">
          <label className="field-label">每日流量限额 (GB)</label>
          <input className="input mono" type="number" min={0} step={0.1} placeholder="0 = 不限"
            value={limitsDraft.dailyGB}
            onChange={(e) => setLimitsDraft({ ...limitsDraft, dailyGB: e.target.value })}/>
          <div className="field-hint">
            超过此值后该镜像当天不再被派发；次日零点自动重置。
          </div>
        </div>
        <div className="field">
          <label className="field-label">总速度限制 (Mbps)</label>
          <input className="input mono" type="number" min={0} step={1} placeholder="0 = 不限"
            value={limitsDraft.speedMbps}
            onChange={(e) => setLimitsDraft({ ...limitsDraft, speedMbps: e.target.value })}/>
          <div className="field-hint">
            实时估算超速时暂停新请求派发；对 CDN/S3 等不可控节点仅控制调度，不限制已在下载的连接。
          </div>
        </div>
      </Modal>

      {/* 行内编辑文件清单的 Modal */}
      <Modal
        open={editFilesIdx != null}
        title={editFilesIdx != null ? `编辑文件清单 — ${mirrors[editFilesIdx]?.url}` : ""}
        subtitle="每行一个 key,可选附 size(逗号分隔)。留空则恢复为 S3 自发现"
        wide
        onClose={() => setEditFilesIdx(null)}
        footer={
          <>
            <button className="btn" onClick={() => setEditFilesIdx(null)}>取消</button>
            <button className="btn btn-primary" onClick={saveFilesEdit}>
              <I.Check size={13}/> 保存
            </button>
          </>
        }
      >
        <textarea className="input mono" rows={14}
          placeholder={"# 每行格式:\n#   key            (size 未知 → 客户端自查)\n#   key,bytes      (内联 size,客户端跳过 HEAD)\ndata/master_2024.bin,1048576\nimages/cards/100.png"}
          style={{ width: "100%", resize: "vertical" }}
          value={filesDraft}
          onChange={(e) => setFilesDraft(e.target.value)}/>
      </Modal>

      <PipelineCard/>

      <AutoPackageCard/>

      <Card title="离线整包" subtitle="客户端可绕过镜像列表,直接下载完整 zip"
        actions={<button className="btn btn-primary" onClick={() => {
          // 打开时把当前生效的 min_version 预填进表单 —— 运维一目了然现在有没有
          // 版本门槛,留着不动就是"沿用现策略",清空就是"撤销策略"。
          setPubForm({
            url: "", version: "", sha256: "", size: "",
            min_version: offlinePackage.min_version || "",
          });
          setShowPublish(true);
        }}><I.Plus size={13}/> 发布新包</button>}
        style={{ marginTop: 14 }}>
        <div className="grid" style={{ gridTemplateColumns: "1fr 1fr 1fr 1fr", gap: 18 }}>
          <div className="kvpair">
            <div className="kv-label">版本</div>
            <div className="kv-value mono">{offlinePackage.version}</div>
          </div>
          <div className="kvpair">
            <div className="kv-label">文件大小</div>
            <div className="kv-value mono">{fmt.bytes(offlinePackage.size)}</div>
          </div>
          <div className="kvpair">
            <div className="kv-label">上传时间</div>
            <div className="kv-value">{fmt.dt(offlinePackage.uploadedAt)}</div>
          </div>
          <div className="kvpair">
            <div className="kv-label">下载 URL</div>
            <div className="kv-value mono" style={{ display: "flex", alignItems: "center", gap: 6 }}>
              <a href="#" onClick={(e) => e.preventDefault()} style={{ color: "var(--purple-300)", textDecoration: "none", fontSize: 12 }}>
                {fmt.short(offlinePackage.url, 36)}
              </a>
              <CopyValue iconOnly value={offlinePackage.url} label="URL" mono={false}/>
            </div>
          </div>
        </div>
        <div className="divider"/>
        <div className="kvpair">
          <div className="kv-label">SHA-256</div>
          <div className="kv-value mono" style={{ fontSize: 12 }}>
            {offlinePackage.sha256} <CopyValue iconOnly value={offlinePackage.sha256} label="哈希" mono={false}/>
          </div>
        </div>
        <div className="divider"/>
        <div className="kvpair">
          <div className="kv-label" style={{ display: "flex", alignItems: "center", gap: 6 }}>
            版本门槛 (min_version)
            <span style={{ fontSize: 10, color: "var(--text-3)", fontFamily: "var(--mono)" }}>
              /client/init → offline_pack
            </span>
          </div>
          <div className="kv-value mono" style={{ fontSize: 12 }}>
            {offlinePackage.min_version
              ? <><span style={{ color: "var(--amber-500)" }}>{offlinePackage.min_version}</span>
                  <span style={{ color: "var(--text-3)", marginLeft: 8, fontSize: 11 }}>
                    客户端低于此版本会弹版本过低提示
                  </span></>
              : <span style={{ color: "var(--text-3)" }}>未设置 · 客户端跳过版本检查</span>}
          </div>
        </div>
      </Card>

      {/* Publish offline package */}
      <Modal
        open={showPublish}
        title="发布新离线包"
        subtitle="发布后旧版本仍保留链接,但 /init 只返回最新条目"
        wide
        onClose={() => setShowPublish(false)}
        footer={
          <>
            <button className="btn" onClick={() => setShowPublish(false)}>取消</button>
            <button className="btn btn-primary" onClick={submitPublish}><I.Send size={13}/> 发布</button>
          </>
        }
      >
        <div className="field">
          <label className="field-label">下载 URL</label>
          <input className="input mono" placeholder="https://offline.example.cn/pkg/full_2.4.1.zip"
            value={pubForm.url} onChange={(e) => setPubForm({ ...pubForm, url: e.target.value })}/>
        </div>
        <div className="field-row">
          <div className="field">
            <label className="field-label">版本号</label>
            <input className="input mono" placeholder="2.4.1" value={pubForm.version}
              onChange={(e) => setPubForm({ ...pubForm, version: e.target.value })}/>
          </div>
          <div className="field">
            <label className="field-label">文件大小 (bytes)</label>
            <input className="input mono" placeholder="1923584923" value={pubForm.size}
              onChange={(e) => setPubForm({ ...pubForm, size: e.target.value.replace(/[^0-9]/g, "") })}/>
          </div>
        </div>
        <div className="field">
          <label className="field-label">SHA-256 <span style={{ color: "var(--text-3)", fontWeight: 400 }}>· 64 位十六进制</span></label>
          <input className="input mono" placeholder="9f8e7d6c..." value={pubForm.sha256}
            onChange={(e) => setPubForm({ ...pubForm, sha256: e.target.value })}/>
        </div>
        <div className="field">
          <label className="field-label">
            最低版本门槛 (min_version)
            <span style={{ color: "var(--text-3)", fontWeight: 400 }}>
              · 可选 · 见 server-offline-pack-validation.md §3
            </span>
          </label>
          <input className="input mono" placeholder="留空 = 不下发版本门槛(此版本向下兼容时)"
            value={pubForm.min_version}
            onChange={(e) => setPubForm({ ...pubForm, min_version: e.target.value })}/>
          <div className="field-hint">
            服务端要求客户端必须安装的最低离线包版本号。**只在本次发布是破坏性变更
            (旧包不兼容)时才填**;留空表示沿用现有策略。客户端启动时若已装版本
            低于此值会弹"离线包版本过低",可选择重新注入 / 在线下载 / 暂时忽略。
            常见格式:`20250501`(日期)、`1.2.3`(语义版本)。
          </div>
        </div>
      </Modal>
    </div>
  );
}
Object.assign(window, { PageResources });

/* ====================== Auto-package offline zip ====================== */
function AutoPackageCard() {
  const { state, dispatch } = useApp();
  const toast = useToast();
  const { autoPackage } = state;
  const [confirmRun, setConfirmRun] = useState(false);
  const [, force] = useState(0);
  useEffect(() => { const t = setInterval(() => force(n => n + 1), 5000); return () => clearInterval(t); }, []);

  const nextRunAt = autoPackage.lastRunAt + autoPackage.intervalSec * 1000;
  const overdue = autoPackage.enabled && Date.now() > nextRunAt;

  const triggerNow = async () => {
    setConfirmRun(false);
    // 乐观地把 inProgress 立刻置 true,服务端最终也会回写,但 UI 不等
    dispatch({ type: "autoPackage.set", patch: { inProgress: true } });
    dispatch({ type: "audit.add", entry: { type: "offline.auto_package_run", target: "manual", details: {} } });
    toast("打包任务已提交,完成后会自动发布到客户端", "ok");
    try {
      // 真打包,服务端同步等到完成才回包 —— 大资源目录下可能要几十秒
      const res = await Api.post("/admin/auto-package/run", {});
      // 把元数据同步进本地状态,离线整包卡片立即显示新 sha256/版本
      dispatch({ type: "offlinePackage.set", value: {
        url: res.download_url,
        version: res.version,
        sha256: res.sha256,
        size: res.size,
        uploadedAt: Date.now(),
        min_version: offlinePackage.min_version || "",
      }});
      dispatch({ type: "autoPackage.set", patch: {
        inProgress: false,
        lastRunAt: Date.now(),
        lastResult: "ok",
      }});
      toast(`离线包 ${res.version} 打包完成(${(res.size/1024/1024).toFixed(1)} MB)`, "ok");
    } catch (err) {
      dispatch({ type: "autoPackage.set", patch: {
        inProgress: false,
        lastRunAt: Date.now(),
        lastResult: "fail",
      }});
      toast("打包失败: " + (err.message || err.code || "未知错误"), "err");
    }
  };

  return (
    <Card
      title="离线包自动打包"
      subtitle="根据当前已同步的资源自动打 zip,可定时或在新 release 同步完成后触发"
      actions={
        <>
          <span className="badge-pill" style={{ color: autoPackage.enabled ? "var(--teal-700)" : "var(--text-3)" }}>
            <span className="dot" style={{ background: autoPackage.enabled ? "var(--teal-500)" : "var(--text-disabled)" }}/>
            {autoPackage.enabled ? "已启用" : "已暂停"}
          </span>
          <Switch value={autoPackage.enabled} onChange={(v) => {
            dispatch({ type: "autoPackage.set", patch: { enabled: v } });
            dispatch({ type: "audit.add", entry: { type: "offline.auto_package.toggle", target: v ? "on" : "off", details: {} } });
            toast(`离线包自动打包已${v ? "启用" : "暂停"}`, "ok");
          }}/>
          <button className="btn btn-primary btn-sm" onClick={() => setConfirmRun(true)} disabled={autoPackage.inProgress}>
            {autoPackage.inProgress
              ? <><span style={{ width: 11, height: 11, border: "2px solid #fff", borderTopColor: "transparent", borderRadius: "50%", display: "inline-block", animation: "spin 0.6s linear infinite" }}/> 打包中…</>
              : <><I.Package size={12}/> 立即打包</>}
          </button>
        </>
      }
      style={{ marginTop: 14 }}
    >
      <div className="grid" style={{ gridTemplateColumns: "repeat(4, minmax(0, 1fr))", gap: 14, marginBottom: 14 }}>
        <div className="field">
          <label className="field-label"><I.Clock size={12}/> 间隔 (秒)</label>
          <input className="input mono" type="number" min={300} max={604800} step={60}
            value={autoPackage.intervalSec}
            onChange={(e) => dispatch({ type: "autoPackage.set", patch: { intervalSec: Math.max(300, Number(e.target.value) || 300) } })}/>
          <div className="field-hint">{Math.round(autoPackage.intervalSec / 60)} 分钟 · {(autoPackage.intervalSec / 3600).toFixed(1)} 小时</div>
        </div>
        <div className="field">
          <label className="field-label"><I.Zap size={12}/> 触发条件</label>
          <select className="select input" value={autoPackage.triggerOn}
            onChange={(e) => dispatch({ type: "autoPackage.set", patch: { triggerOn: e.target.value } })}>
            <option value="interval">仅按间隔</option>
            <option value="new-release">仅在新 release 同步完成</option>
            <option value="both">间隔 + 新 release</option>
          </select>
          <div className="field-hint">推荐「间隔 + 新 release」组合,既兜底又及时。</div>
        </div>
        <div className="field">
          <label className="field-label"><I.HardDrive size={12}/> 保留历史版本</label>
          <input className="input mono" type="number" min={1} max={20}
            value={autoPackage.retainVersions}
            onChange={(e) => dispatch({ type: "autoPackage.set", patch: { retainVersions: Math.max(1, Number(e.target.value) || 1) } })}/>
          <div className="field-hint">超出数量的旧包会被自动从 S3 删除。</div>
        </div>
        <div className="field">
          <label className="field-label"><I.Package size={12}/> 压缩算法</label>
          <select className="select input mono" value={autoPackage.compress}
            onChange={(e) => dispatch({ type: "autoPackage.set", patch: { compress: e.target.value } })}>
            <option value="zstd">zstd · 推荐</option>
            <option value="zip">zip · 兼容性</option>
            <option value="tar.gz">tar.gz</option>
          </select>
          <div className="field-hint">{autoPackage.compress === "zstd" ? "压缩比高、解压快" : autoPackage.compress === "zip" ? "客户端开箱即用" : "Unix 友好"}</div>
        </div>
      </div>

      <div style={{
        padding: 12, borderRadius: 8,
        background: overdue ? "var(--amber-50)" : autoPackage.inProgress ? "var(--accent-50)" : "var(--bg-2)",
        border: `1px solid ${overdue ? "rgba(217,119,6,0.3)" : autoPackage.inProgress ? "var(--accent-200)" : "var(--border-1)"}`,
        display: "flex", alignItems: "center", gap: 12, flexWrap: "wrap",
      }}>
        <span style={{
          width: 28, height: 28, borderRadius: 7,
          background: "var(--bg-1-solid)",
          border: `1px solid ${overdue ? "rgba(217,119,6,0.3)" : "var(--border-1)"}`,
          display: "grid", placeItems: "center", flexShrink: 0,
          color: autoPackage.inProgress ? "var(--accent-700)" : overdue ? "var(--amber-500)" : autoPackage.lastResult === "ok" ? "var(--teal-700)" : "var(--red-500)",
        }}>
          {autoPackage.inProgress
            ? <span style={{ width: 12, height: 12, border: "2px solid var(--accent-300)", borderTopColor: "var(--accent-600)", borderRadius: "50%", display: "inline-block", animation: "spin 0.7s linear infinite" }}/>
            : overdue ? <I.Alert size={14}/>
            : autoPackage.lastResult === "ok" ? <I.Check size={14}/> : <I.Alert size={14}/>}
        </span>
        <div style={{ flex: 1, minWidth: 0 }}>
          <div style={{ fontSize: 12.5, fontWeight: 500 }}>
            {autoPackage.inProgress
              ? "正在打包……" 
              : `上次执行 · ${fmt.ago(autoPackage.lastRunAt)}`}
            {autoPackage.lastResult === "ok" && !autoPackage.inProgress && (
              <StatusBadge status="ok">成功</StatusBadge>
            )}
          </div>
          <div className="mono" style={{ fontSize: 11.5, color: "var(--text-3)", marginTop: 2 }}>
            {autoPackage.enabled
              ? overdue
                ? <span style={{ color: "var(--amber-500)" }}>下次执行已逾期 · 等待主节点 tick</span>
                : <>下次执行 · {fmt.countdown(nextRunAt)}</>
              : "自动打包已暂停 · 客户端使用现有离线包"}
          </div>
        </div>
        <div style={{ fontSize: 11, color: "var(--text-3)", fontFamily: "var(--mono)" }}>
          algo {autoPackage.compress} · keep {autoPackage.retainVersions}
        </div>
      </div>

      <ConfirmDialog
        open={confirmRun}
        danger={false}
        title="立即打包离线包"
        confirmText="开始打包"
        message={
          <>
            将立即按当前镜像 / S3 内容打一个全量 zip 包,完成后自动替换「离线整包」卡片中的链接,并向客户端下发。
            预计耗时 1–3 分钟,期间已下载到一半的客户端不受影响。
          </>
        }
        onCancel={() => setConfirmRun(false)}
        onConfirm={triggerNow}
      />
    </Card>
  );
}

/* ====================== Pipeline Card ====================== */
const PIPELINE_STAGES = [
  { id: "detect",   label: "检测",       icon: "Eye" },
  { id: "download", label: "下载",       icon: "Download" },
  { id: "s3",       label: "上传 S3",    icon: "HardDrive" },
  { id: "cdn",      label: "刷新 CDN",   icon: "Cloud" },
];

function PipelineCard() {
  const { state, dispatch } = useApp();
  const toast = useToast();
  const { pipeline } = state;
  const [syncing, setSyncing] = useState(false);
  const [confirmSync, setConfirmSync] = useState(false);
  const [showCfg, setShowCfg] = useState(false);
  const [, force] = useState(0);
  useEffect(() => { const t = setInterval(() => force(n => n + 1), 5000); return () => clearInterval(t); }, []);

  const recent = pipeline.releases && pipeline.releases[0];
  const ghRepo = [pipeline.github_owner, pipeline.github_repo].filter(Boolean).join("/") || "—";

  const triggerSync = () => {
    setSyncing(true);
    setConfirmSync(false);
    const now = Date.now();
    const fakeTag = `v2.4.${2 + Math.floor(Math.random() * 9)}`;
    const fakeRelease = {
      tag: fakeTag,
      title: `${fakeTag} · 手动触发同步`,
      releasedAt: now,
      assetCount: 15 + Math.floor(Math.random() * 30),
      totalBytes: 100_000_000 + Math.floor(Math.random() * 1_500_000_000),
      stages: [
        { id: "detect",   label: "检测新版本",        state: "done",    at: now },
        { id: "download", label: "下载 Release 资源", state: "running", at: now },
        { id: "s3",       label: "上传至 S3",         state: "pending", at: null },
        { id: "cdn",      label: "刷新 CDN 缓存",     state: "pending", at: null },
      ],
    };
    dispatch({ type: "pipeline.triggerSync", release: fakeRelease });
    dispatch({ type: "audit.add", entry: { type: "pipeline.manual_sync", target: fakeTag, details: { trigger: "manual" } } });
    dispatch({ type: "events.push", entry: { kind: "publish", text: `资源同步管道已启动 · ${fakeTag}`, actor: "admin_homura", icon: "Cloud", color: "purple" } });
    setTimeout(() => setSyncing(false), 800);
    toast(`已触发同步 · ${fakeTag}`, "ok");
  };

  return (
    <Card
      title="资源同步管道"
      subtitle="GitHub Release → 下载 → S3 → CDN 缓存刷新 · 全自动"
      actions={
        <>
          <span className="badge-pill" style={{ color: pipeline.auto_sync ? "var(--teal-700)" : "var(--text-3)" }}>
            <span className="dot" style={{ background: pipeline.auto_sync ? "var(--teal-500)" : "var(--text-disabled)" }}/>
            {pipeline.auto_sync ? "自动同步" : "已暂停"}
          </span>
          <span style={{ display: "inline-flex", alignItems: "center", gap: 4 }}>
            <input className="input mono" type="number" min={30} max={86400}
              style={{ width: 80, padding: "3px 6px", fontSize: 11.5, textAlign: "right" }}
              value={pipeline.poll_interval_sec ?? 300}
              onChange={(e) => dispatch({ type: "pipeline.set", patch: { poll_interval_sec: Math.max(30, Number(e.target.value) || 60) } })}/>
            <span style={{ color: "var(--text-3)", fontSize: 11.5, fontFamily: "var(--mono)" }}>秒</span>
          </span>
          <Switch value={!!pipeline.auto_sync} onChange={(v) => {
            dispatch({ type: "pipeline.set", patch: { auto_sync: v } });
            dispatch({ type: "audit.add", entry: { type: "pipeline.toggle", target: v ? "on" : "off", details: {} } });
            toast(`自动同步已${v ? "开启" : "暂停"}`, "ok");
          }}/>
          <button className="btn btn-sm" style={{ background: "var(--bg-2)", border: "1px solid var(--border-2)" }} onClick={() => setShowCfg(true)}>
            <I.Settings size={12}/> 配置
          </button>
          <button className="btn btn-primary btn-sm" onClick={() => setConfirmSync(true)} disabled={syncing || pipeline.in_progress}>
            <I.Refresh size={12}/> 立即同步
          </button>
        </>
      }
      style={{ marginTop: 14 }}
    >
      {/* config summary row */}
      <div style={{
        display: "grid",
        gridTemplateColumns: "repeat(4, minmax(0, 1fr))",
        gap: 14,
        paddingBottom: 14,
        marginBottom: 14,
        borderBottom: "1px solid var(--border-1)",
      }}>
        <div className="kvpair">
          <div className="kv-label">GitHub 仓库</div>
          <div className="kv-value mono" style={{ fontSize: 12.5, display: "flex", alignItems: "center", gap: 6 }}>
            <I.ExternalLink size={12} style={{ color: "var(--accent-600)", flexShrink: 0 }}/>
            <span style={{ color: "var(--accent-700)" }}>{ghRepo}</span>
          </div>
        </div>
        <div className="kvpair">
          <div className="kv-label">监听 tag</div>
          <div className="kv-value mono">{pipeline.github_tag_pattern || "v*"}</div>
        </div>
        <div className="kvpair">
          <div className="kv-label">S3 桶</div>
          <div className="kv-value mono" style={{ fontSize: 12.5 }}>
            {pipeline.s3_enabled ? (pipeline.s3_bucket || "—") : <span style={{ color: "var(--text-disabled)" }}>未启用</span>}
          </div>
        </div>
        <div className="kvpair">
          <div className="kv-label">CDN</div>
          <div className="kv-value">
            {pipeline.cdn_enabled
              ? <><span style={{ marginRight: 6, textTransform: "capitalize" }}>{pipeline.cdn_provider || "—"}</span>
                  <span className="mono" style={{ color: "var(--text-3)", fontSize: 11.5 }}>· 轮询 {pipeline.poll_interval_sec ?? 300}s</span></>
              : <span style={{ color: "var(--text-disabled)" }}>未启用</span>
            }
          </div>
        </div>
      </div>

      {/* in-progress banner */}
      {pipeline.in_progress && (
        <div style={{ display: "flex", alignItems: "center", gap: 8, padding: "8px 12px", marginBottom: 12,
          background: "var(--accent-50)", border: "1px solid var(--accent-200)", borderRadius: 6,
          color: "var(--accent-700)", fontSize: 12.5 }}>
          <span style={{ display: "inline-block", width: 8, height: 8, borderRadius: "50%", background: "var(--accent-500)", animation: "pulse 1s infinite" }}/>
          同步正在后台运行中…
        </div>
      )}

      {/* latest release card */}
      {recent && <div style={{
        background: "var(--bg-2)",
        border: "1px solid var(--border-1)",
        borderRadius: 8,
        padding: 14,
        marginBottom: 14,
      }}>
        <div style={{ display: "flex", alignItems: "center", gap: 10, marginBottom: 12, flexWrap: "wrap" }}>
          <span className="badge-pill badge-info">
            <I.Tag size={11}/> 最新 release
          </span>
          <span style={{ fontWeight: 600, fontSize: 14 }} className="mono">{recent.tag}</span>
          <span style={{ color: "var(--text-2)", fontSize: 12.5 }}>{recent.title.replace(`${recent.tag} · `, "")}</span>
          <span style={{ marginLeft: "auto", color: "var(--text-3)", fontSize: 11.5, fontFamily: "var(--mono)" }}>
            {recent.assetCount} assets · {fmt.bytes(recent.totalBytes)} · GitHub 发布于 {fmt.ago(recent.releasedAt)}
          </span>
        </div>
        <PipelineStages stages={recent.stages}/>
      </div>}

      {/* history */}
      <div>
        <div className="kv-label" style={{ marginBottom: 8 }}>历史同步</div>
        {(pipeline.releases || []).slice(1).map(rel => (
          <div key={rel.tag} style={{
            display: "grid",
            gridTemplateColumns: "auto 1fr auto",
            gap: 12,
            alignItems: "center",
            padding: "8px 12px",
            border: "1px solid var(--border-1)",
            borderRadius: 6,
            marginBottom: 6,
            background: "var(--bg-1-solid)",
          }}>
            <span className="badge-pill mono" style={{ fontSize: 11.5 }}>{rel.tag}</span>
            <span style={{ color: "var(--text-2)", fontSize: 12.5 }}>{rel.title.replace(`${rel.tag} · `, "")}</span>
            <span style={{ display: "flex", alignItems: "center", gap: 12, color: "var(--text-3)", fontSize: 11.5 }}>
              <span className="mono">{rel.assetCount} assets · {fmt.bytes(rel.totalBytes)}</span>
              <StatusBadge status="ok">已完成</StatusBadge>
              <span className="mono">{fmt.ago(rel.releasedAt)}</span>
            </span>
          </div>
        ))}
      </div>

      <div className="field-hint" style={{ marginTop: 12, display: "flex", gap: 8, alignItems: "flex-start" }}>
        <I.Info size={12} style={{ marginTop: 2, flexShrink: 0 }}/>
        <span>
          流程: 主节点每 {pipeline.poll_interval_sec ?? 300} 秒轮询 GitHub Release，发现匹配 <span className="mono" style={{ color: "var(--text-2)" }}>{pipeline.github_tag_pattern || "v*"}</span> 的新 tag 后，
          自动下载 release 全部资产、上传至 S3，然后通过 CDN API 刷新对应路径的缓存。S3/CDN 的密钥通过环境变量传入，不写入数据库。
        </span>
      </div>

      <ConfirmDialog
        open={confirmSync}
        danger={false}
        title="立即触发资源同步"
        confirmText="开始同步"
        message={
          <>
            将立即检查 <span className="mono" style={{ color: "var(--text-1)" }}>{ghRepo}</span> 的最新 release，
            若版本号高于已同步的 <span className="mono" style={{ color: "var(--text-1)" }}>{recent?.tag ?? "—"}</span> 则启动下载 → S3 → CDN 全流程。
            此操作不会跳过自动同步队列。
          </>
        }
        onCancel={() => setConfirmSync(false)}
        onConfirm={triggerSync}
      />

      <PipelineConfigModal
        open={showCfg}
        pipeline={pipeline}
        dispatch={dispatch}
        toast={toast}
        onClose={() => setShowCfg(false)}
      />
    </Card>
  );
}

/* ── 管道配置弹窗 ──────────────────────────────────────────────── */
function PipelineConfigModal({ open, pipeline, dispatch, toast, onClose }) {
  const [form, setForm] = useState({});

  useEffect(() => {
    if (open) setForm({ ...pipeline });
  }, [open]);

  const set = (k, v) => setForm(f => ({ ...f, [k]: v }));

  const save = () => {
    // releases 和运行时状态不随配置保存
    const { releases, in_progress, last_sync_at, last_sync_result, ...cfg } = form;
    dispatch({ type: "pipeline.set", patch: cfg });
    dispatch({ type: "audit.add", entry: { type: "pipeline.config.update", target: "", details: {
      github_repo: (cfg.github_owner || "") + "/" + (cfg.github_repo || ""),
      s3_enabled: cfg.s3_enabled,
      cdn_enabled: cfg.cdn_enabled,
    }}});
    toast("管道配置已保存", "ok");
    onClose();
  };

  const sectionTitle = (t) => (
    <div style={{ fontWeight: 700, fontSize: 12, color: "var(--text-2)", textTransform: "uppercase",
      letterSpacing: "0.07em", borderBottom: "1px solid var(--border-1)", paddingBottom: 8, marginBottom: 14 }}>
      {t}
    </div>
  );

  const row = (label, hint, children) => (
    <div style={{ display: "grid", gridTemplateColumns: "160px 1fr", gap: 12, alignItems: "flex-start", marginBottom: 12 }}>
      <div>
        <div style={{ fontSize: 13, fontWeight: 600 }}>{label}</div>
        {hint && <div style={{ fontSize: 11, color: "var(--text-3)", marginTop: 2, lineHeight: 1.4 }}>{hint}</div>}
      </div>
      <div>{children}</div>
    </div>
  );

  const envHint = "填 env 变量名（如 GH_TOKEN），服务端启动时从该变量读取，不存储密钥本身";
  const inp = (k, placeholder) => (
    <input className="input mono" style={{ width: "100%" }}
      placeholder={placeholder}
      value={form[k] ?? ""}
      onChange={e => set(k, e.target.value)}/>
  );

  return (
    <Modal open={open} title="资源同步管道配置" onClose={onClose} wide>
      <div style={{ padding: "4px 0", maxHeight: "70vh", overflowY: "auto" }}>

        {/* ── GitHub 数据源 ─────────────────────────────────── */}
        {sectionTitle("GitHub 数据源")}
        {row("仓库 Owner", null, inp("github_owner", "magireco-revival"))}
        {row("仓库名", null, inp("github_repo", "client-assets"))}
        {row("Tag 匹配规则", "支持后缀通配符，如 v* 匹配所有 v 开头的 tag", inp("github_tag_pattern", "v*"))}
        {row("轮询间隔（秒）", "最小 30 秒", (
          <input className="input mono" type="number" min={30} max={86400}
            style={{ width: 120 }}
            value={form.poll_interval_sec ?? 300}
            onChange={e => set("poll_interval_sec", Math.max(30, Number(e.target.value) || 300))}/>
        ))}
        {row("GitHub Token 变量名", envHint, inp("github_token_env", "GH_TOKEN"))}
        {row("自动同步", null, (
          <label style={{ display: "flex", alignItems: "center", gap: 8, cursor: "pointer" }}>
            <input type="checkbox" checked={!!form.auto_sync} onChange={e => set("auto_sync", e.target.checked)}
              style={{ width: 15, height: 15, accentColor: "var(--accent-600)" }}/>
            <span style={{ fontSize: 13, color: form.auto_sync ? "var(--teal-600)" : "var(--text-3)" }}>
              {form.auto_sync ? "已启用" : "已关闭"}
            </span>
          </label>
        ))}

        <div style={{ marginBottom: 20 }}/>

        {/* ── S3 资源上传 ──────────────────────────────────── */}
        <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between",
          borderBottom: "1px solid var(--border-1)", paddingBottom: 8, marginBottom: 14 }}>
          <span style={{ fontWeight: 700, fontSize: 12, color: "var(--text-2)", textTransform: "uppercase", letterSpacing: "0.07em" }}>
            S3 资源上传
          </span>
          <label style={{ display: "flex", alignItems: "center", gap: 6, cursor: "pointer" }}>
            <input type="checkbox" checked={!!form.s3_enabled} onChange={e => set("s3_enabled", e.target.checked)}
              style={{ width: 14, height: 14, accentColor: "var(--accent-600)" }}/>
            <span style={{ fontSize: 12, color: form.s3_enabled ? "var(--teal-600)" : "var(--text-3)", fontWeight: 500 }}>
              {form.s3_enabled ? "已启用" : "已关闭"}
            </span>
          </label>
        </div>
        <div style={{ opacity: form.s3_enabled ? 1 : 0.45, pointerEvents: form.s3_enabled ? "auto" : "none" }}>
          {row("Endpoint", "留空使用 AWS 默认端点（兼容 MinIO/R2 等）", inp("s3_endpoint", "https://s3.ap-east-1.amazonaws.com"))}
          {row("Bucket", null, inp("s3_bucket", "magireco-assets-ap-east"))}
          {row("Region", null, inp("s3_region", "ap-east-1"))}
          {row("Access Key ID", "公开部分，可安全存储", inp("s3_key_id", "AKIAXXX"))}
          {row("Secret Key 变量名", envHint, inp("s3_secret_env", "CNV_S3_SECRET"))}
        </div>

        <div style={{ marginBottom: 20 }}/>

        {/* ── CDN 缓存刷新 ─────────────────────────────────── */}
        <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between",
          borderBottom: "1px solid var(--border-1)", paddingBottom: 8, marginBottom: 14 }}>
          <span style={{ fontWeight: 700, fontSize: 12, color: "var(--text-2)", textTransform: "uppercase", letterSpacing: "0.07em" }}>
            CDN 缓存刷新
          </span>
          <label style={{ display: "flex", alignItems: "center", gap: 6, cursor: "pointer" }}>
            <input type="checkbox" checked={!!form.cdn_enabled} onChange={e => set("cdn_enabled", e.target.checked)}
              style={{ width: 14, height: 14, accentColor: "var(--accent-600)" }}/>
            <span style={{ fontSize: 12, color: form.cdn_enabled ? "var(--teal-600)" : "var(--text-3)", fontWeight: 500 }}>
              {form.cdn_enabled ? "已启用" : "已关闭"}
            </span>
          </label>
        </div>
        <div style={{ opacity: form.cdn_enabled ? 1 : 0.45, pointerEvents: form.cdn_enabled ? "auto" : "none" }}>
          {row("CDN 服务商", null, (
            <select className="input" value={form.cdn_provider ?? "cloudflare"} onChange={e => set("cdn_provider", e.target.value)}>
              <option value="cloudflare">Cloudflare</option>
              <option value="custom">自定义 HTTP 端点</option>
            </select>
          ))}
          {(form.cdn_provider === "cloudflare" || !form.cdn_provider) && (
            row("Cloudflare Zone ID", "在 Cloudflare 控制台 → 你的域 → 概述页面右侧", inp("cdn_zone", "abc123..."))
          )}
          {form.cdn_provider === "custom" && (
            row("Purge 端点 URL", "POST 请求，带 Authorization: Bearer <token> 头", inp("cdn_purge_url", "https://cdn.example.com/api/purge"))
          )}
          {row("Auth Token 变量名", envHint, inp("cdn_auth_env", "CNV_CDN_TOKEN"))}
        </div>

        <div style={{ marginBottom: 20 }}/>

        {/* ── 离线包上传（独立 S3）────────────────────────── */}
        <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between",
          borderBottom: "1px solid var(--border-1)", paddingBottom: 8, marginBottom: 14 }}>
          <span style={{ fontWeight: 700, fontSize: 12, color: "var(--text-2)", textTransform: "uppercase", letterSpacing: "0.07em" }}>
            离线整包 S3 上传（独立配置）
          </span>
          <label style={{ display: "flex", alignItems: "center", gap: 6, cursor: "pointer" }}>
            <input type="checkbox" checked={!!form.offline_upload_enabled} onChange={e => set("offline_upload_enabled", e.target.checked)}
              style={{ width: 14, height: 14, accentColor: "var(--accent-600)" }}/>
            <span style={{ fontSize: 12, color: form.offline_upload_enabled ? "var(--teal-600)" : "var(--text-3)", fontWeight: 500 }}>
              {form.offline_upload_enabled ? "已启用" : "已关闭"}
            </span>
          </label>
        </div>
        <div style={{ opacity: form.offline_upload_enabled ? 1 : 0.45, pointerEvents: form.offline_upload_enabled ? "auto" : "none" }}>
          <div className="field-hint" style={{ marginBottom: 12 }}>
            <I.Info size={11}/>
            离线包打包完成后自动上传到此 S3 桶，download_url 将替换为 S3 公开 URL。
            与资源上传使用独立的 bucket 和密钥。
          </div>
          {row("Endpoint", "留空使用 AWS 默认", inp("offline_s3_endpoint", "https://s3.ap-east-1.amazonaws.com"))}
          {row("Bucket", null, inp("offline_s3_bucket", "magireco-offline-pkg"))}
          {row("Region", null, inp("offline_s3_region", "ap-east-1"))}
          {row("Access Key ID", "公开部分，可安全存储", inp("offline_s3_key_id", "AKIAXXX"))}
          {row("Secret Key 变量名", envHint, inp("offline_s3_secret_env", "CNV_OFFLINE_S3_SECRET"))}
        </div>

      </div>

      <div style={{ display: "flex", justifyContent: "flex-end", gap: 8, paddingTop: 16, borderTop: "1px solid var(--border-1)", marginTop: 8 }}>
        <button className="btn btn-sm" onClick={onClose}>取消</button>
        <button className="btn btn-primary btn-sm" onClick={save}>保存配置</button>
      </div>
    </Modal>
  );
}

function PipelineStages({ stages }) {
  if (!stages) return null;
  if (stages === "all-done") {
    return (
      <div style={{ display: "flex", gap: 8, color: "var(--text-3)", fontSize: 12 }}>
        <StatusBadge status="ok">全部完成</StatusBadge>
      </div>
    );
  }
  return (
    <div style={{ display: "grid", gridTemplateColumns: "repeat(4, minmax(0, 1fr))", gap: 10 }}>
      {stages.map((s, i) => {
        const meta = PIPELINE_STAGES[i];
        const Icon = I[meta.icon];
        const stateColor = s.state === "done" ? "var(--teal-600)"
          : s.state === "running" ? "var(--accent-600)"
          : s.state === "failed" ? "var(--red-500)"
          : "var(--text-disabled)";
        const stateBg = s.state === "done" ? "var(--teal-50)"
          : s.state === "running" ? "var(--accent-50)"
          : s.state === "failed" ? "var(--red-50)"
          : "var(--bg-3)";
        const stateBorder = s.state === "done" ? "rgba(20,184,166,0.3)"
          : s.state === "running" ? "var(--accent-300)"
          : s.state === "failed" ? "rgba(220,38,38,0.3)"
          : "var(--border-1)";
        const stateLabel = s.state === "done" ? "完成"
          : s.state === "running" ? "进行中"
          : s.state === "failed" ? "失败"
          : "等待";
        return (
          <div key={s.id} style={{
            position: "relative",
            background: stateBg,
            border: `1px solid ${stateBorder}`,
            borderRadius: 8,
            padding: "10px 12px",
          }}>
            <div style={{ display: "flex", alignItems: "center", gap: 8, marginBottom: 6 }}>
              <span style={{
                width: 22, height: 22, borderRadius: 6,
                background: "var(--bg-1-solid)",
                border: `1px solid ${stateBorder}`,
                color: stateColor,
                display: "grid", placeItems: "center",
                flexShrink: 0,
              }}>
                <Icon size={12}/>
              </span>
              <div style={{ flex: 1, minWidth: 0 }}>
                <div style={{ fontSize: 11.5, fontWeight: 600 }}>{s.label}</div>
                <div style={{ fontSize: 10.5, color: stateColor, fontFamily: "var(--mono)", textTransform: "uppercase", letterSpacing: "0.06em" }}>
                  {s.state === "running" && <span style={{ display: "inline-block", width: 5, height: 5, borderRadius: "50%", background: stateColor, marginRight: 4, animation: "pulse 1s infinite" }}/>}
                  {stateLabel}
                </div>
              </div>
            </div>
            <div style={{ fontSize: 11, color: "var(--text-3)", lineHeight: 1.45 }}>
              {s.note}
              {s.durationMs != null && (
                <span style={{ marginLeft: 4, fontFamily: "var(--mono)" }}>· {(s.durationMs / 1000).toFixed(1)}s</span>
              )}
            </div>
            {/* connector */}
            {i < 3 && (
              <span style={{
                position: "absolute",
                right: -7, top: "50%",
                width: 14, height: 1,
                background: stateBorder,
                zIndex: 1,
              }}/>
            )}
          </div>
        );
      })}
    </div>
  );
}
