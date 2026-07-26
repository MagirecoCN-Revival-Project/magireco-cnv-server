/* 审计动作元数据:把机器类型(l.type)翻译成中文「操作名」,并标注「目标」是什么属性。
 * 让"发离线包"这类记录直接读成「发布离线包 · 离线包版本 2.4.0」,而非裸 type=offline.publish、
 * target=2.4.0。纯展示层:服务端已在 /admin/state 与 /admin/audit 下发 {type,target,details},
 * 这里不改任何 wire 契约,只做翻译与归类;未登记的 type 回落显示原始串。 */
const AUDIT_META = {
  "server.status.change":        { label: "切换服务状态",     targetK: "目标状态", targetFmt: v => ({ ok: "正常", maintenance: "维护中" }[v] || v),
                                   attrs: d => [d.message && { k: "维护文案", v: d.message }, d.estimated_end && { k: "预计恢复", v: fmt.dt(d.estimated_end) }] },
  "feature.toggle":              { label: "切换功能开关",     attrs: d => [
                                     ("online" in d)  && { k: "在线下载", v: d.online ? "开" : "关" },
                                     ("offline" in d) && { k: "离线包",   v: d.offline ? "开" : "关" },
                                     ("account" in d) && { k: "账号系统", v: d.account ? "开" : "关" }] },
  "version.allowed.add":         { label: "新增允许版本",     targetK: "版本" },
  "version.allowed.remove":      { label: "移除允许版本",     targetK: "版本" },
  "version.fake.update":         { label: "更新伪装版本" },
  "services.update":             { label: "更新服务配置",     attrs: d => [
                                     d.cap_worker_url   && { k: "cap-worker", v: d.cap_worker_url },
                                     d.game_server_host && { k: "游戏服",     v: d.game_server_host },
                                     ("proxy_backends" in d) && { k: "代理后端", v: d.proxy_backends + " 个" }] },
  "mirror.add":                  { label: "新增镜像",         targetK: "镜像" },
  "mirror.remove":               { label: "移除镜像",         targetK: "镜像" },
  "mirror.reorder":              { label: "调整镜像顺序",     attrs: d => [("count" in d) && { k: "镜像数", v: d.count }] },
  "offline.publish":             { label: "发布离线包",       targetK: "离线包版本",
                                   attrs: d => [d.sha256_prefix && { k: "SHA256", v: d.sha256_prefix + "…" }, ("size" in d) && { k: "大小", v: fmt.bytes(d.size) }] },
  "offline.auto_package.update": { label: "更新自动打包策略" },
  "offline.auto_package.fail":   { label: "自动打包失败",     attrs: d => [d.err && { k: "错误", v: d.err }] },
  "pipeline.manual_sync":        { label: "手动触发资源同步" },
  "hotupdate.js.publish":        { label: "发布 JS 热更新",   targetK: "JS 版本" },
  "hotupdate.scn.publish":       { label: "发布剧本热更新",   targetK: "剧本版本" },
  "account.create":              { label: "创建玩家账号",     targetK: "账号" },
  "account.disable":             { label: "停用玩家账号",     targetK: "账号" },
  "account.enable":              { label: "启用玩家账号",     targetK: "账号" },
  "account.reset_pwd":           { label: "重置玩家密码",     targetK: "账号" },
  "account.delete":              { label: "删除玩家账号",     targetK: "账号" },
  "device.ban":                  { label: "封禁设备",         targetK: "设备",
                                   attrs: d => [d.reason && { k: "原因", v: d.reason }, d.source && { k: "来源", v: d.source }] },
  "device.lift":                 { label: "解除设备封禁",     targetK: "设备" },
  "heartbeat.force_switch":      { label: "强制切换下载线路", targetK: "设备" },
  "task.interval.update":        { label: "调整定时任务间隔" },
  "captcha.config.update":       { label: "更新验证码配置",   attrs: d => [("enabled" in d) && { k: "启用", v: d.enabled ? "是" : "否" }, d.difficulty && { k: "难度", v: d.difficulty }] },
  "autoban.config.update":       { label: "更新自动封禁阈值", attrs: d => [("enabled" in d) && { k: "启用", v: d.enabled ? "是" : "否" }, d.captcha_max && { k: "验证码阈值", v: d.captcha_max }] },
  "captcha.test":                { label: "测试验证码",       attrs: d => [d.result && { k: "结果", v: d.result }] },
  "admin.create":                { label: "创建管理员",       targetK: "管理员", attrs: d => [d.role && { k: "角色", v: d.role }] },
  "admin.role.update":           { label: "调整管理员角色",   targetK: "管理员", attrs: d => [d.role && { k: "新角色", v: d.role }] },
  "admin.remove":                { label: "移除管理员",       targetK: "管理员" },
  "admin.reset_pwd":             { label: "重置管理员密码",   targetK: "管理员" },
};

// auditLabel 把机器 type 翻成中文操作名;未登记则回落原串。
function auditLabel(type) {
  const m = AUDIT_META[type];
  return (m && m.label) || type;
}

// auditAttrs 把一条记录的「目标 + 关键属性」拍平成 [{k,v}]:
// k 是属性名(如"离线包版本"),v 是值。target 优先,其后补 details 里的要点字段。
function auditAttrs(l) {
  const m = AUDIT_META[l.type] || {};
  const out = [];
  if (l.target) out.push({ k: m.targetK || "目标", v: m.targetFmt ? m.targetFmt(l.target) : l.target });
  const d = l.details;
  if (m.attrs && d && typeof d === "object") {
    for (const a of m.attrs(d)) {
      if (a && a.v !== undefined && a.v !== null && a.v !== "") out.push(a);
    }
  }
  return out;
}

function PageAudit() {
  const { state } = useApp();
  const { auditLog } = state;

  const [typeFilter, setTypeFilter] = useState("all");
  const [actorFilter, setActorFilter] = useState("all");
  const [days, setDays] = useState(7);
  const [page, setPage] = useState(1);
  const pageSize = 18;
  const [expanded, setExpanded] = useState(new Set());

  const actors = useMemo(() => Array.from(new Set(auditLog.map(l => l.actor))), [auditLog]);
  const types = useMemo(() => Array.from(new Set(auditLog.map(l => l.type))), [auditLog]);

  const filtered = useMemo(() => {
    const since = Date.now() - days * 86400e3;
    return auditLog.filter(l => {
      if (l.ts < since) return false;
      if (typeFilter !== "all" && l.type !== typeFilter) return false;
      if (actorFilter !== "all" && l.actor !== actorFilter) return false;
      return true;
    });
  }, [auditLog, typeFilter, actorFilter, days]);

  const paged = filtered.slice((page - 1) * pageSize, page * pageSize);

  const toggle = (id) => {
    const n = new Set(expanded);
    n.has(id) ? n.delete(id) : n.add(id);
    setExpanded(n);
  };

  return (
    <div className="page" data-screen-label="10 审计日志">
      <div className="page-head">
        <div>
          <div className="page-title">审计日志</div>
          <div className="page-sub">不可篡改 · 所有管理操作的完整记录</div>
        </div>
        <div className="actions">
          <button className="btn"><I.Download size={13}/> 导出 CSV</button>
        </div>
      </div>

      <Card tight>
        <div className="toolbar" style={{ flexWrap: "wrap" }}>
          <div style={{ display: "flex", alignItems: "center", gap: 6 }}>
            <span style={{ color: "var(--text-3)", fontSize: 12 }}>操作类型</span>
            <select className="select input" style={{ width: "auto" }} value={typeFilter} onChange={(e) => { setTypeFilter(e.target.value); setPage(1); }}>
              <option value="all">全部 ({auditLog.length})</option>
              {types.map(t => <option key={t} value={t}>{auditLabel(t)}</option>)}
            </select>
          </div>
          <div style={{ display: "flex", alignItems: "center", gap: 6 }}>
            <span style={{ color: "var(--text-3)", fontSize: 12 }}>执行者</span>
            <select className="select input" style={{ width: "auto" }} value={actorFilter} onChange={(e) => { setActorFilter(e.target.value); setPage(1); }}>
              <option value="all">全部</option>
              {actors.map(a => <option key={a} value={a}>{a}</option>)}
            </select>
          </div>
          <div className="segmented">
            {[1, 7, 30, 90].map(d => (
              <button key={d} className={days === d ? "active" : ""} onClick={() => { setDays(d); setPage(1); }}>
                {d === 1 ? "今日" : `${d}天`}
              </button>
            ))}
          </div>
          <div style={{ flex: 1 }}/>
          <span style={{ color: "var(--text-3)", fontSize: 12, fontFamily: "var(--mono)" }}>结果 {filtered.length} 条</span>
        </div>

        <div className="table-wrap">
        <table className="table">
          <thead>
            <tr>
              <th style={{ width: 32 }}></th>
              <th>时间</th>
              <th>执行者</th>
              <th>操作</th>
              <th>目标 / 属性</th>
              <th>来源 IP</th>
            </tr>
          </thead>
          <tbody>
            {paged.map(l => {
              const open = expanded.has(l.id);
              return (
                <React.Fragment key={l.id}>
                  <tr onClick={() => toggle(l.id)} style={{ cursor: "pointer" }}>
                    <td>
                      <span style={{ color: "var(--text-3)" }}>
                        {open ? <I.ChevDown size={12}/> : <I.ChevRight size={12}/>}
                      </span>
                    </td>
                    <td><span className="mono" style={{ fontSize: 11.5, color: "var(--text-3)" }}>{fmt.dt(l.ts)}</span></td>
                    <td>
                      {l.actor === "system"
                        ? <StatusBadge status="info">system</StatusBadge>
                        : <span className="mono" style={{ fontSize: 12 }}>{l.actor}</span>}
                    </td>
                    <td>
                      <div style={{ fontSize: 12.5, color: "var(--text-1)", fontWeight: 500 }}>{auditLabel(l.type)}</div>
                      <div className="mono" style={{ fontSize: 10.5, color: "var(--text-3)" }}>{l.type}</div>
                    </td>
                    <td>
                      {(() => {
                        const attrs = auditAttrs(l);
                        if (attrs.length === 0) return <span style={{ color: "var(--text-3)" }}>—</span>;
                        return (
                          <div style={{ display: "flex", flexWrap: "wrap", gap: "4px 10px" }}>
                            {attrs.map((a, i) => (
                              <span key={i} style={{ display: "inline-flex", alignItems: "baseline", gap: 4, fontSize: 12, maxWidth: 280 }}>
                                <span style={{ color: "var(--text-3)", fontSize: 11, whiteSpace: "nowrap" }}>{a.k}</span>
                                <span className="mono" style={{ color: "var(--text-2)", overflow: "hidden", textOverflow: "ellipsis" }}>{String(a.v)}</span>
                              </span>
                            ))}
                          </div>
                        );
                      })()}
                    </td>
                    <td><span className="mono" style={{ fontSize: 11.5, color: "var(--text-3)" }}>{l.details?.ip || "—"}</span></td>
                  </tr>
                  {open && (
                    <tr className="expand-row fade-in">
                      <td></td>
                      <td colSpan={5} style={{ padding: "0 14px 14px" }}>
                        <pre className="code-block" style={{ marginTop: 4 }}>{JSON.stringify({
                          id: l.id,
                          ts: new Date(l.ts).toISOString(),
                          actor: l.actor,
                          type: l.type,
                          target: l.target,
                          details: l.details,
                        }, null, 2)}</pre>
                      </td>
                    </tr>
                  )}
                </React.Fragment>
              );
            })}
            {paged.length === 0 && (
              <tr><td colSpan={6} style={{ textAlign: "center", padding: 40, color: "var(--text-3)" }}>该筛选条件下没有日志</td></tr>
            )}
          </tbody>
        </table>
        </div>
        <Pagination total={filtered.length} page={page} pageSize={pageSize} onPage={setPage}/>
      </Card>
    </div>
  );
}
Object.assign(window, { PageAudit });
