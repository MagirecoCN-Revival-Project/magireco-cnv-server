/* Permissions page — manage admin user roles. Only visible to super_admin. */

function PagePermissions() {
  const { state, dispatch } = useApp();
  const toast = useToast();
  const { adminRoster, currentAdmin } = state;
  const ROLES = MOCK.ROLE_LABELS;

  const isSuper = currentAdmin.role === "super_admin";
  const [q, setQ] = useState("");
  const [createOpen, setCreateOpen] = useState(false);
  const [createForm, setCreateForm] = useState({ username: "", email: "", role: "admin", password: "" });
  const [confirm, setConfirm] = useState(null);

  const filtered = adminRoster.filter(a =>
    !q || a.username.includes(q) || a.email.includes(q)
  );

  if (!isSuper) {
    return (
      <div className="page" data-screen-label="12 权限管理">
        <Card>
          <div style={{ padding: 40, textAlign: "center" }}>
            <I.Lock size={28} style={{ color: "var(--text-3)", marginBottom: 10 }}/>
            <div style={{ fontSize: 14, fontWeight: 600 }}>仅超级管理员可访问</div>
            <div style={{ color: "var(--text-3)", fontSize: 12.5, marginTop: 6 }}>
              当前账户 <span className="mono">{currentAdmin.username}</span> 的角色为「{ROLES[currentAdmin.role].label}」,
              无权查看此页。
            </div>
          </div>
        </Card>
      </div>
    );
  }

  const setRole = (id, newRole) => {
    const target = adminRoster.find(a => a.id === id);
    if (!target) return;
    if (target.role === "super_admin" && adminRoster.filter(a => a.role === "super_admin").length === 1 && newRole !== "super_admin") {
      toast("至少保留一名超级管理员", "err");
      return;
    }
    dispatch({ type: "admin.update", id, patch: { role: newRole } });
    dispatch({ type: "audit.add", entry: { type: "admin.role.update", target: target.username, details: { from: target.role, to: newRole } } });
    toast(`${target.username} 的角色已改为 ${ROLES[newRole].label}`, "ok");
  };

  const submitCreate = () => {
    const { username, email, role, password } = createForm;
    if (!username || !email || !password) { toast("请填写完整字段", "err"); return; }
    if (adminRoster.find(a => a.username === username)) { toast("用户名已存在", "err"); return; }
    const entry = {
      id: `adm_${Date.now()}`,
      username, email, role,
      lastLoginAt: 0,
      createdAt: Date.now(),
    };
    dispatch({ type: "admin.add", value: entry });
    dispatch({ type: "audit.add", entry: { type: "admin.create", target: username, details: { role } } });
    setCreateOpen(false);
    setCreateForm({ username: "", email: "", role: "admin", password: "" });
    toast(`管理员 ${username} 已创建`, "ok");
  };

  const doConfirm = () => {
    const c = confirm;
    if (c.kind === "remove") {
      if (c.target.role === "super_admin" && adminRoster.filter(a => a.role === "super_admin").length === 1) {
        toast("不能删除最后一个超级管理员", "err");
        setConfirm(null);
        return;
      }
      if (c.target.id === adminRoster.find(a => a.username === currentAdmin.username)?.id) {
        toast("不能删除自己", "err");
        setConfirm(null);
        return;
      }
      dispatch({ type: "admin.remove", id: c.target.id });
      dispatch({ type: "audit.add", entry: { type: "admin.remove", target: c.target.username, details: {} } });
      toast(`管理员 ${c.target.username} 已删除`, "ok");
    } else if (c.kind === "reset") {
      dispatch({ type: "audit.add", entry: { type: "admin.reset_pwd", target: c.target.username, details: {} } });
      toast(`已为 ${c.target.username} 重置密码,临时密码已邮件发送`, "ok");
    }
    setConfirm(null);
  };

  const counts = {
    super_admin: adminRoster.filter(a => a.role === "super_admin").length,
    admin: adminRoster.filter(a => a.role === "admin").length,
    readonly: adminRoster.filter(a => a.role === "readonly").length,
  };

  return (
    <div className="page" data-screen-label="12 权限管理">
      <div className="page-head">
        <div>
          <div className="page-title">权限管理</div>
          <div className="page-sub">面板管理员 · 仅超级管理员可见</div>
        </div>
        <div className="actions">
          <button className="btn btn-primary" onClick={() => setCreateOpen(true)}>
            <I.Plus size={13}/> 新建管理员
          </button>
        </div>
      </div>

      <div className="grid grid-3" style={{ marginBottom: 14 }}>
        <RoleStatCard role="super_admin" count={counts.super_admin} ROLES={ROLES}/>
        <RoleStatCard role="admin" count={counts.admin} ROLES={ROLES}/>
        <RoleStatCard role="readonly" count={counts.readonly} ROLES={ROLES}/>
      </div>

      <Card tight>
        <div className="toolbar">
          <div className="search-input">
            <span className="ico"><I.Search size={14}/></span>
            <input className="input" placeholder="搜索用户名 / 邮箱" value={q} onChange={(e) => setQ(e.target.value)}/>
          </div>
          <div style={{ flex: 1 }}/>
          <span style={{ color: "var(--text-3)", fontSize: 12, fontFamily: "var(--mono)" }}>结果 {filtered.length}</span>
        </div>

        <div className="table-wrap">
        <table className="table">
          <thead>
            <tr>
              <th>账户</th>
              <th>邮箱</th>
              <th>角色</th>
              <th>注册时间</th>
              <th>最近登录</th>
              <th className="actions-cell">操作</th>
            </tr>
          </thead>
          <tbody>
            {filtered.map(a => {
              const isMe = a.username === currentAdmin.username;
              const info = ROLES[a.role];
              return (
                <tr key={a.id}>
                  <td>
                    <div style={{ display: "flex", alignItems: "center", gap: 8 }}>
                      <span style={{
                        width: 26, height: 26, borderRadius: "50%",
                        background: `oklch(${50 + (a.username.charCodeAt(6) % 30)}% 0.12 ${(a.username.charCodeAt(6) * 13) % 360})`,
                        display: "grid", placeItems: "center", color: "#fff",
                        fontSize: 11, fontWeight: 600,
                      }}>{a.username.replace(/^(admin_|audit_|ops_)/, "")[0]?.toUpperCase()}</span>
                      <span className="mono">{a.username}</span>
                      {isMe && <span className="badge-pill badge-info" style={{ fontSize: 10 }}>当前账户</span>}
                    </div>
                  </td>
                  <td><span style={{ color: "var(--text-2)" }}>{a.email}</span></td>
                  <td>
                    <select className="select input mono"
                      style={{ width: 150, padding: "4px 24px 4px 8px", fontSize: 12 }}
                      value={a.role}
                      onChange={(e) => setRole(a.id, e.target.value)}>
                      {Object.entries(ROLES).map(([key, r]) => (
                        <option key={key} value={key}>{r.label}</option>
                      ))}
                    </select>
                  </td>
                  <td><span className="mono" style={{ color: "var(--text-3)", fontSize: 11.5 }}>{fmt.dt(a.createdAt)}</span></td>
                  <td>
                    <span style={{ color: "var(--text-2)" }}>
                      {a.lastLoginAt ? fmt.ago(a.lastLoginAt) : <span style={{ color: "var(--text-3)" }}>未登录</span>}
                    </span>
                  </td>
                  <td className="actions-cell">
                    <div style={{ display: "inline-flex", gap: 4 }}>
                      <button className="btn btn-ghost btn-sm" title="重置密码"
                        onClick={() => setConfirm({ kind: "reset", target: a })}>
                        <I.Lock size={12}/>
                      </button>
                      <button className="btn btn-ghost btn-sm" title="删除"
                        disabled={isMe}
                        onClick={() => setConfirm({ kind: "remove", target: a })}
                        style={{ color: isMe ? "var(--text-disabled)" : "var(--red-500)" }}>
                        <I.Trash size={12}/>
                      </button>
                    </div>
                  </td>
                </tr>
              );
            })}
            {filtered.length === 0 && (
              <tr><td colSpan={6} style={{ textAlign: "center", padding: 40, color: "var(--text-3)" }}>没有匹配的管理员</td></tr>
            )}
          </tbody>
        </table>
        </div>
      </Card>

      <Card title="角色权限矩阵" subtitle="只读 · 用于参考" style={{ marginTop: 14 }} tight>
        <div className="table-wrap">
        <table className="table">
          <thead>
            <tr>
              <th>能力</th>
              <th style={{ textAlign: "center" }}>超级管理员</th>
              <th style={{ textAlign: "center" }}>管理员</th>
              <th style={{ textAlign: "center" }}>只读审计</th>
            </tr>
          </thead>
          <tbody>
            {[
              ["查看仪表盘 / 监控",       true, true, true],
              ["切换服务状态 / 维护文案",  true, true, false],
              ["发布热更新 / 离线包",       true, true, false],
              ["管理玩家账号 / 设备封禁",  true, true, false],
              ["编辑定时任务间隔",          true, true, false],
              ["resource_token 签名密钥轮换",  true, true, false],
              ["管理面板管理员 (本页)",   true, false, false],
              ["导出审计日志",              true, true, true],
            ].map(([label, s, a, r], i) => (
              <tr key={i}>
                <td>{label}</td>
                <td style={{ textAlign: "center" }}>{s ? <I.Check size={14} stroke="var(--teal-600)"/> : <span style={{ color: "var(--text-disabled)" }}>—</span>}</td>
                <td style={{ textAlign: "center" }}>{a ? <I.Check size={14} stroke="var(--teal-600)"/> : <span style={{ color: "var(--text-disabled)" }}>—</span>}</td>
                <td style={{ textAlign: "center" }}>{r ? <I.Check size={14} stroke="var(--teal-600)"/> : <span style={{ color: "var(--text-disabled)" }}>—</span>}</td>
              </tr>
            ))}
          </tbody>
        </table>
        </div>
      </Card>

      <Modal
        open={createOpen}
        title="新建管理员"
        subtitle="创建后会发送带临时密码的邀请邮件"
        onClose={() => setCreateOpen(false)}
        footer={
          <>
            <button className="btn" onClick={() => setCreateOpen(false)}>取消</button>
            <button className="btn btn-primary" onClick={submitCreate}><I.Plus size={13}/> 创建</button>
          </>
        }
      >
        <div className="field">
          <label className="field-label">用户名</label>
          <input className="input mono" placeholder="admin_xxx" value={createForm.username}
            onChange={(e) => setCreateForm({ ...createForm, username: e.target.value })}/>
        </div>
        <div className="field">
          <label className="field-label">邮箱</label>
          <input className="input" placeholder="xxx@magireco-revival.cn" value={createForm.email}
            onChange={(e) => setCreateForm({ ...createForm, email: e.target.value })}/>
        </div>
        <div className="field">
          <label className="field-label">初始密码</label>
          <input className="input mono" type="password" value={createForm.password}
            onChange={(e) => setCreateForm({ ...createForm, password: e.target.value })}/>
          <div className="field-hint">首次登录后强制改密</div>
        </div>
        <div className="field">
          <label className="field-label">角色</label>
          <div style={{ display: "flex", flexDirection: "column", gap: 6 }}>
            {Object.entries(ROLES).map(([key, r]) => (
              <button key={key} type="button"
                onClick={() => setCreateForm({ ...createForm, role: key })}
                style={{
                  display: "flex", alignItems: "flex-start", gap: 10,
                  padding: "10px 12px",
                  background: createForm.role === key ? "var(--accent-50)" : "var(--bg-2)",
                  border: `1px solid ${createForm.role === key ? "var(--accent-400)" : "var(--border-1)"}`,
                  borderRadius: 6,
                  textAlign: "left",
                  cursor: "pointer",
                  color: "inherit",
                  fontFamily: "inherit",
                }}>
                <span style={{
                  width: 14, height: 14, borderRadius: "50%", marginTop: 3, flexShrink: 0,
                  background: createForm.role === key ? "var(--accent-600)" : "transparent",
                  border: `1.5px solid ${createForm.role === key ? "var(--accent-600)" : "var(--border-strong)"}`,
                  boxShadow: createForm.role === key ? "inset 0 0 0 3px var(--bg-1-solid)" : "none",
                }}/>
                <div style={{ flex: 1 }}>
                  <div style={{ fontSize: 13, fontWeight: 500 }}>{r.label}</div>
                  <div style={{ color: "var(--text-3)", fontSize: 11.5 }}>{r.desc}</div>
                </div>
              </button>
            ))}
          </div>
        </div>
      </Modal>

      <ConfirmDialog
        open={!!confirm}
        title={confirm?.kind === "remove"
          ? `删除管理员 ${confirm.target.username}`
          : `重置 ${confirm?.target?.username} 的密码`}
        confirmText={confirm?.kind === "remove" ? "删除" : "重置密码"}
        danger={confirm?.kind === "remove"}
        message={
          confirm?.kind === "remove"
            ? <>该操作不可撤销。删除后,<span className="mono">{confirm.target.username}</span> 立即失去面板访问权限,其所有 session 会被踢出。</>
            : confirm?.kind === "reset"
              ? <>系统会生成一次性密码并发送到 <span className="mono">{confirm.target.email}</span>。下次登录后强制改密。</>
              : ""
        }
        onCancel={() => setConfirm(null)}
        onConfirm={doConfirm}
      />
    </div>
  );
}

function RoleStatCard({ role, count, ROLES }) {
  const info = ROLES[role];
  const cls = info.color === "info" ? "badge-info"
    : info.color === "ok" ? "badge-ok"
    : "badge-muted";
  return (
    <div className="card">
      <div className="card-body">
        <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", marginBottom: 4 }}>
          <span className={`badge-pill ${cls}`}><span className="dot"/>{info.label}</span>
          <span className="mono" style={{ fontSize: 11.5, color: "var(--text-3)" }}>{role}</span>
        </div>
        <div style={{ fontSize: 28, fontWeight: 600, fontFamily: "var(--mono)", letterSpacing: "-0.02em", marginTop: 6 }}>{count}</div>
        <div style={{ color: "var(--text-3)", fontSize: 12, marginTop: 2 }}>{info.desc}</div>
      </div>
    </div>
  );
}

Object.assign(window, { PagePermissions });
