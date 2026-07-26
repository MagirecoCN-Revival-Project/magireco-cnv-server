function PageAccounts() {
  const { state, dispatch } = useApp();
  const toast = useToast();
  const { accounts } = state;

  const [q, setQ] = useState("");
  const [statusFilter, setStatusFilter] = useState("all");
  const [sort, setSort] = useState({ key: "lastLoginAt", dir: "desc" });
  const [page, setPage] = useState(1);
  const pageSize = 12;

  const [detailFor, setDetailFor] = useState(null);
  const [createOpen, setCreateOpen] = useState(false);
  const [createForm, setCreateForm] = useState({ username: "", email: "", password: "" });
  const [confirm, setConfirm] = useState(null);

  const filtered = useMemo(() => {
    let list = accounts;
    if (q) {
      const s = q.toLowerCase();
      list = list.filter(a => a.username.includes(s) || a.email.includes(s) || a.id.includes(s));
    }
    if (statusFilter !== "all") list = list.filter(a => a.status === statusFilter);
    list = [...list].sort((a, b) => {
      const av = a[sort.key], bv = b[sort.key];
      if (av < bv) return sort.dir === "asc" ? -1 : 1;
      if (av > bv) return sort.dir === "asc" ? 1 : -1;
      return 0;
    });
    return list;
  }, [accounts, q, statusFilter, sort]);

  const paged = filtered.slice((page - 1) * pageSize, page * pageSize);

  const toggleSort = (key) => {
    if (sort.key === key) setSort({ key, dir: sort.dir === "asc" ? "desc" : "asc" });
    else setSort({ key, dir: "asc" });
  };

  const sortIcon = (key) => sort.key !== key ? null : (sort.dir === "asc" ? " ↑" : " ↓");

  const submitCreate = () => {
    if (!createForm.username || !createForm.email || !createForm.password) {
      toast("请填写完整字段", "err"); return;
    }
    if (accounts.find(a => a.username === createForm.username)) {
      toast("用户名已存在", "err"); return;
    }
    dispatch({ type: "accounts.add", value: {
      id: `acc_${Date.now()}`,
      username: createForm.username,
      email: createForm.email,
      createdAt: Date.now(),
      lastLoginAt: Date.now(),
      status: "active",
    }});
    dispatch({ type: "audit.add", entry: { type: "account.create", target: createForm.username, details: {} } });
    setCreateOpen(false);
    setCreateForm({ username: "", email: "", password: "" });
    toast(`账号 ${createForm.username} 已创建`, "ok");
  };

  const doConfirm = () => {
    const c = confirm;
    if (c.kind === "disable") {
      dispatch({ type: "accounts.update", id: c.acc.id, patch: { status: "disabled" } });
      dispatch({ type: "audit.add", entry: { type: "account.disable", target: c.acc.username, details: {} } });
      toast(`已停用账号 ${c.acc.username}`, "ok");
    } else if (c.kind === "enable") {
      dispatch({ type: "accounts.update", id: c.acc.id, patch: { status: "active" } });
      dispatch({ type: "audit.add", entry: { type: "account.enable", target: c.acc.username, details: {} } });
      toast(`已启用账号 ${c.acc.username}`, "ok");
    } else if (c.kind === "reset") {
      dispatch({ type: "audit.add", entry: { type: "account.reset_pwd", target: c.acc.username, details: {} } });
      toast(`已为 ${c.acc.username} 重置密码,临时密码已通过邮件发送`, "ok");
    } else if (c.kind === "delete") {
      dispatch({ type: "accounts.remove", id: c.acc.id });
      dispatch({ type: "audit.add", entry: { type: "account.delete", target: c.acc.username, details: {} } });
      toast(`账号 ${c.acc.username} 已删除`, "ok");
    }
    setConfirm(null);
    setDetailFor(null);
  };

  return (
    <div className="page" data-screen-label="06 账号管理">
      <div className="page-head">
        <div>
          <div className="page-title">账号管理</div>
          <div className="page-sub">玩家账号 · 创建 / 停用 / 重置密码 / 删除</div>
        </div>
        <div className="actions">
          <button className="btn btn-primary" onClick={() => setCreateOpen(true)}><I.Plus size={13}/> 创建账号</button>
        </div>
      </div>

      <Card tight>
        <div className="toolbar">
          <div className="search-input">
            <span className="ico"><I.Search size={14}/></span>
            <input className="input" placeholder="搜索用户名 / 邮箱 / ID"
              value={q} onChange={(e) => { setQ(e.target.value); setPage(1); }}/>
          </div>
          <div className="segmented">
            <button className={statusFilter === "all" ? "active" : ""} onClick={() => { setStatusFilter("all"); setPage(1); }}>全部 {accounts.length}</button>
            <button className={statusFilter === "active" ? "active" : ""} onClick={() => { setStatusFilter("active"); setPage(1); }}>活跃 {accounts.filter(a=>a.status==="active").length}</button>
            <button className={statusFilter === "disabled" ? "active" : ""} onClick={() => { setStatusFilter("disabled"); setPage(1); }}>停用 {accounts.filter(a=>a.status==="disabled").length}</button>
          </div>
          <div style={{ flex: 1 }}/>
          <span style={{ color: "var(--text-3)", fontSize: 12, fontFamily: "var(--mono)" }}>结果 {filtered.length}</span>
        </div>

        <div className="table-wrap">
        <table className="table">
          <thead>
            <tr>
              <th className="sortable" onClick={() => toggleSort("username")}>用户名{sortIcon("username")}</th>
              <th>邮箱</th>
              <th className="sortable" onClick={() => toggleSort("createdAt")}>注册时间{sortIcon("createdAt")}</th>
              <th className="sortable" onClick={() => toggleSort("lastLoginAt")}>最近登录{sortIcon("lastLoginAt")}</th>
              <th>状态</th>
              <th className="actions-cell">操作</th>
            </tr>
          </thead>
          <tbody>
            {paged.map(a => (
              <tr key={a.id}>
                <td>
                  <div style={{ display: "flex", alignItems: "center", gap: 8 }}>
                    <span style={{
                      width: 24, height: 24, borderRadius: "50%",
                      background: `oklch(${50 + (a.username.charCodeAt(0) % 30)}% 0.12 ${(a.username.charCodeAt(0) * 13) % 360})`,
                      display: "grid", placeItems: "center", color: "#fff",
                      fontSize: 11, fontWeight: 600,
                    }}>{a.username[0].toUpperCase()}</span>
                    <span className="mono">{a.username}</span>
                  </div>
                </td>
                <td><span style={{ color: "var(--text-2)" }}>{a.email}</span></td>
                <td><span className="mono" style={{ color: "var(--text-3)", fontSize: 11.5 }}>{fmt.dt(a.createdAt)}</span></td>
                <td><span style={{ color: "var(--text-2)" }}>{fmt.ago(a.lastLoginAt)}</span></td>
                <td>
                  <StatusBadge status={a.status === "active" ? "ok" : "muted"}>
                    {a.status === "active" ? "活跃" : "已停用"}
                  </StatusBadge>
                </td>
                <td className="actions-cell">
                  <div style={{ display: "inline-flex", gap: 4 }}>
                    <button className="btn btn-ghost btn-sm" onClick={() => setDetailFor(a)} title="查看详情"><I.Eye size={12}/></button>
                    <button className="btn btn-ghost btn-sm" onClick={() => setConfirm({ kind: "reset", acc: a })} title="重置密码"><I.Lock size={12}/></button>
                    <button className="btn btn-ghost btn-sm"
                      onClick={() => setConfirm({ kind: a.status === "active" ? "disable" : "enable", acc: a })}
                      title={a.status === "active" ? "停用" : "启用"}>
                      {a.status === "active" ? <I.Ban size={12}/> : <I.Check size={12}/>}
                    </button>
                    <button className="btn btn-ghost btn-sm" onClick={() => setConfirm({ kind: "delete", acc: a })} title="删除"
                      style={{ color: "var(--red-400)" }}><I.Trash size={12}/></button>
                  </div>
                </td>
              </tr>
            ))}
            {paged.length === 0 && (
              <tr><td colSpan={6} style={{ textAlign: "center", padding: 40, color: "var(--text-3)" }}>没有匹配的账号</td></tr>
            )}
          </tbody>
        </table>
        </div>

        <Pagination total={filtered.length} page={page} pageSize={pageSize} onPage={setPage}/>
      </Card>

      {/* Create */}
      <Modal
        open={createOpen}
        title="创建账号"
        subtitle="新账号默认为活跃状态"
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
          <input className="input mono" value={createForm.username}
            onChange={(e) => setCreateForm({ ...createForm, username: e.target.value })}/>
        </div>
        <div className="field">
          <label className="field-label">邮箱</label>
          <input className="input" value={createForm.email}
            onChange={(e) => setCreateForm({ ...createForm, email: e.target.value })}/>
        </div>
        <div className="field">
          <label className="field-label">初始密码</label>
          <input className="input mono" type="password" value={createForm.password}
            onChange={(e) => setCreateForm({ ...createForm, password: e.target.value })}/>
          <div className="field-hint">推荐 12 位以上、包含大小写字母与数字</div>
        </div>
      </Modal>

      {/* Detail */}
      <Modal
        open={!!detailFor}
        title={detailFor ? `账号详情 · ${detailFor.username}` : ""}
        onClose={() => setDetailFor(null)}
        wide
        footer={
          detailFor && <>
            <button className="btn" onClick={() => setDetailFor(null)}>关闭</button>
            <button className="btn btn-danger" onClick={() => setConfirm({ kind: "disable", acc: detailFor })}
              disabled={detailFor.status === "disabled"}>
              <I.Ban size={13}/> 停用
            </button>
            <button className="btn btn-primary" onClick={() => setConfirm({ kind: "reset", acc: detailFor })}>
              <I.Lock size={13}/> 重置密码
            </button>
          </>
        }
      >
        {detailFor && (
          <div className="grid grid-2" style={{ gap: 18 }}>
            <div className="kvpair"><div className="kv-label">用户 ID</div><div className="kv-value mono">{detailFor.id}</div></div>
            <div className="kvpair"><div className="kv-label">状态</div><div className="kv-value"><StatusBadge status={detailFor.status === "active" ? "ok" : "muted"}>{detailFor.status === "active" ? "活跃" : "已停用"}</StatusBadge></div></div>
            <div className="kvpair"><div className="kv-label">用户名</div><div className="kv-value mono">{detailFor.username}</div></div>
            <div className="kvpair"><div className="kv-label">邮箱</div><div className="kv-value">{detailFor.email}</div></div>
            <div className="kvpair"><div className="kv-label">注册时间</div><div className="kv-value mono">{fmt.dt(detailFor.createdAt)}</div></div>
            <div className="kvpair"><div className="kv-label">最近登录</div><div className="kv-value mono">{fmt.dt(detailFor.lastLoginAt)}</div></div>
          </div>
        )}
      </Modal>

      {/* Confirm */}
      <ConfirmDialog
        open={!!confirm}
        title={confirm?.kind === "delete" ? `永久删除账号 ${confirm.acc.username}`
          : confirm?.kind === "disable" ? `停用账号 ${confirm.acc.username}`
          : confirm?.kind === "enable" ? `启用账号 ${confirm.acc.username}`
          : confirm?.kind === "reset" ? `重置 ${confirm.acc.username} 的密码` : ""}
        confirmText={confirm?.kind === "delete" ? "永久删除" : confirm?.kind === "disable" ? "停用" : confirm?.kind === "enable" ? "启用" : "重置密码"}
        danger={confirm?.kind === "delete" || confirm?.kind === "disable"}
        message={
          confirm?.kind === "delete" ? <>该操作不可撤销。账号 <span className="mono">{confirm.acc.username}</span> 的存档和绑定设备记录会被删除。</>
          : confirm?.kind === "disable" ? <>账号 <span className="mono">{confirm.acc.username}</span> 将无法登录,已在线的会话会在下一次心跳时被踢出。</>
          : confirm?.kind === "enable" ? <>账号 <span className="mono">{confirm.acc.username}</span> 将恢复正常登录。</>
          : confirm?.kind === "reset" ? <>系统会生成一次性密码,并向 <span className="mono">{confirm.acc.email}</span> 发送邮件。下次登录后强制改密。</>
          : ""
        }
        onCancel={() => setConfirm(null)}
        onConfirm={doConfirm}
      />
    </div>
  );
}
Object.assign(window, { PageAccounts });
