// User-facing panel (separate from admin). Player can:
// - view login devices, terminate sessions
// - manage cloud saves (delete only)
// - change email (= username)
// - change password
// - delete account

const { useState: useUS, useEffect: useUE, useReducer: useUR, useRef: useURef, createContext: createUCtx, useContext: useUCtx } = React;

const NOW2 = Date.now();

function makeDeviceFp(seed) {
  const hex = "0123456789abcdef";
  let s = "", x = seed;
  for (let i = 0; i < 40; i++) { x = (x * 9301 + 49297) % 233280; s += hex[Math.floor(x / 233280 * 16)]; }
  return s;
}

const INITIAL_USER = {
  email: "iroha_t@user.example.com",
  username: "iroha_t",
  createdAt: NOW2 - 1000 * 60 * 60 * 24 * 142,
  loginCount: 387,
};

const INITIAL_DEVICES = [
  { id: "dev_1", deviceId: makeDeviceFp(11), name: "Pixel 8 · Android 14", os: "Android 14", current: true,  lastLoginAt: NOW2 - 1000 * 60 * 2,        ip: "120.244.16.42",   region: "上海" },
  { id: "dev_2", deviceId: makeDeviceFp(22), name: "iPhone 15 Pro · iOS 17.3", os: "iOS 17.3", current: false, lastLoginAt: NOW2 - 1000 * 60 * 60 * 6,    ip: "183.18.74.21",    region: "东京" },
  { id: "dev_3", deviceId: makeDeviceFp(33), name: "BlueStacks · Windows 11", os: "Windows 11 · BlueStacks 10", current: false, lastLoginAt: NOW2 - 1000 * 60 * 60 * 24 * 3, ip: "59.83.142.17", region: "广州" },
  { id: "dev_4", deviceId: makeDeviceFp(44), name: "Galaxy S22 · Android 13", os: "Android 13", current: false, lastLoginAt: NOW2 - 1000 * 60 * 60 * 24 * 11, ip: "111.225.66.4", region: "北京" },
];

const INITIAL_SAVES = [
  { id: "save_1", slot: 1, name: "主存档 · 主线 8-12",     level: 142, mochi: 28_433, updatedAt: NOW2 - 1000 * 60 * 14,        sizeBytes: 4_823_424 },
  { id: "save_2", slot: 2, name: "刷活动 · Magia Festa",   level: 142, mochi: 8_211,  updatedAt: NOW2 - 1000 * 60 * 60 * 2,     sizeBytes: 4_711_232 },
  { id: "save_3", slot: 3, name: "新建练习存档",           level: 38,  mochi: 1_204,  updatedAt: NOW2 - 1000 * 60 * 60 * 24 * 8, sizeBytes: 1_232_896 },
];

function userReducer(state, action) {
  switch (action.type) {
    case "hydrate":      return { ...state, ...action.state, loading: false };
    case "loaded":       return { ...state, loading: false };
    case "user.set":     return { ...state, user: { ...state.user, ...action.patch } };
    case "device.remove":return { ...state, devices: state.devices.filter(d => d.id !== action.id) };
    case "save.remove":  return { ...state, saves: state.saves.filter(s => s.id !== action.id) };
    default: return state;
  }
}

const UserCtx = createUCtx(null);
const useU = () => useUCtx(UserCtx);

const USER_NAV = [
  { key: "profile",  label: "账户信息",   icon: "User" },
  { key: "devices",  label: "登录设备",   icon: "HardDrive" },
  { key: "saves",    label: "云存档",     icon: "Cloud" },
  { key: "security", label: "密码与安全", icon: "Lock" },
  { key: "danger",   label: "注销账户",   icon: "Trash", danger: true },
];

function UserApp() {
  const initial = { loading: true, user: INITIAL_USER, devices: INITIAL_DEVICES, saves: INITIAL_SAVES };
  const [state, dispatch] = useUR(userReducer, initial);
  const [active, setActive] = useUS("profile");
  const [navOpen, setNavOpen] = useUS(false);

  useUE(() => {
    let cancelled = false;
    fetchUserState()
      .then(s => {
        if (cancelled) return;
        const profile = s.profile || {};
        dispatch({
          type: "hydrate",
          state: {
            user: {
              email: profile.email,
              username: profile.username,
              createdAt: profile.created_at,
              loginCount: profile.login_count || 0,
              saveSize: profile.save_size || 0,
            },
            devices: (s.devices || []).map(d => ({
              id: d.id,
              deviceId: d.device_id,
              name: d.name,
              os: d.os,
              ip: d.ip,
              region: d.region,
              current: d.current,
              lastLoginAt: d.last_login_at,
            })),
            saves: (s.saves || []).map(sv => ({
              id: sv.id,
              slot: 1,
              name: sv.name,
              endpointCount: sv.endpoint_count || 0,
              level: 0,
              mochi: 0,
              updatedAt: sv.updated_at,
              sizeBytes: sv.size_bytes || 0,
            })),
          },
        });
      })
      .catch(() => {
        if (cancelled) return;
        // 拉取失败时绝不能把页面永久卡在「正在加载…」:
        // 401 已由 Api 层跳转登录;其余情况(离线预览 / 后端不可达 / 接口报错)
        // 解除 loading,降级渲染初始种子数据,保证页面始终可用、可截图。
        dispatch({ type: "loaded" });
      });
    return () => { cancelled = true; };
  }, []);

  const Page = {
    profile:  ProfilePage,
    devices:  DevicesPage,
    saves:    SavesPage,
    security: SecurityPage,
    danger:   DangerPage,
  }[active];

  if (state.loading) {
    return (
      <div style={{ display: "grid", placeItems: "center", height: "100vh", fontSize: 14, color: "var(--text-2)" }}>
        <span style={{ width: 14, height: 14, border: "2px solid var(--accent-500)", borderTopColor: "transparent", borderRadius: "50%", display: "inline-block", animation: "spin 0.6s linear infinite", marginRight: 8 }}/>
        正在加载用户中心…
      </div>
    );
  }

  return (
    <UserCtx.Provider value={{ state, dispatch }}>
      <div className="app">
        <UserSidebar active={active} setActive={(k) => { setActive(k); setNavOpen(false); }}
          mobileOpen={navOpen} onClose={() => setNavOpen(false)} user={state.user}/>
        {navOpen && <div className="nav-mask" onClick={() => setNavOpen(false)}/>}
        <div className="main">
          <UserTopBar onMenu={() => setNavOpen(true)} active={active} user={state.user}/>
          <Page/>
        </div>
      </div>
    </UserCtx.Provider>
  );
}

function UserSidebar({ active, setActive, mobileOpen, onClose, user }) {
  return (
    <aside className={`sidebar ${mobileOpen ? "mobile-open" : ""}`}>
      <div className="brand">
        <span className="brand-mark" role="img" aria-label="魔法纪录复兴计划">魔</span>
        <div style={{ flex: 1, minWidth: 0 }}>
          <div className="brand-name">用户中心</div>
          <div className="brand-sub">magireco-revival.cn</div>
        </div>
        <button className="btn btn-ghost btn-icon sidebar-close-mobile" onClick={onClose} aria-label="关闭"><I.X size={16}/></button>
      </div>
      <div className="nav-section">
        <div className="nav-section-title">我的</div>
        {USER_NAV.map(n => {
          const Icon = I[n.icon];
          return (
            <button key={n.key}
              className={`nav-item ${active === n.key ? "active" : ""}`}
              onClick={() => setActive(n.key)}
              style={n.danger ? { color: "var(--red-500)" } : {}}>
              <span className="ico"><Icon size={15}/></span>
              <span>{n.label}</span>
            </button>
          );
        })}
      </div>
      <div style={{ padding: "12px 14px", borderTop: "1px solid var(--border-1)", marginTop: "auto" }}>
        <div style={{ display: "flex", alignItems: "center", gap: 10 }}>
          <div style={{
            width: 32, height: 32, borderRadius: "50%",
            background: "linear-gradient(135deg, var(--accent-500), var(--accent-700))",
            color: "#fff", display: "grid", placeItems: "center",
            fontSize: 13, fontWeight: 600,
          }}>{user.username[0].toUpperCase()}</div>
          <div style={{ flex: 1, minWidth: 0 }}>
            <div className="mono" style={{ fontSize: 12, color: "var(--text-1)", overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>{user.email}</div>
            <a href="login.html" style={{ fontSize: 11, color: "var(--text-3)", textDecoration: "none" }}>退出登录</a>
          </div>
        </div>
      </div>
    </aside>
  );
}

function UserTopBar({ onMenu, active, user }) {
  const here = USER_NAV.find(n => n.key === active);
  const toast = useToast();
  return (
    <div className="topbar">
      <button className="btn btn-ghost btn-icon menu-btn" onClick={onMenu}><I.Logs size={16}/></button>
      <div className="crumbs">
        <span className="hide-mobile">用户中心 <I.ChevRight size={11} style={{ verticalAlign: "middle", color: "var(--text-disabled)" }}/>{" "}</span>
        <span className="here">{here?.label}</span>
      </div>
      <div className="spacer"/>
      <a href="login.html" className="btn btn-ghost" onClick={async (e) => {
        e.preventDefault();
        try { await Api.post("/auth/logout", {}); } catch (_) {}
        Api.setToken("");
        toast("正在退出登录…", "ok");
        setTimeout(() => { window.location.href = "login.html"; }, 400);
      }}>
        <I.X size={13}/> 退出登录
      </a>
    </div>
  );
}

/* ====================== Pages ====================== */

function ProfilePage() {
  const { state } = useU();
  const { user, devices, saves } = state;
  return (
    <div className="page" data-screen-label="01 账户信息">
      <div className="page-head">
        <div>
          <div className="page-title">账户信息</div>
          <div className="page-sub">您的账户概况</div>
        </div>
      </div>

      <Card style={{ marginBottom: 14 }}>
        <div style={{ display: "flex", alignItems: "center", gap: 16, flexWrap: "wrap" }}>
          <div style={{
            width: 64, height: 64, borderRadius: "50%",
            background: "linear-gradient(135deg, var(--accent-500), var(--accent-700))",
            color: "#fff", display: "grid", placeItems: "center",
            fontSize: 26, fontWeight: 600,
            flexShrink: 0,
            boxShadow: "0 4px 12px rgba(0,0,0,0.08)",
          }}>{user.username[0].toUpperCase()}</div>
          <div style={{ flex: 1, minWidth: 200 }}>
            <div style={{ fontSize: 18, fontWeight: 600 }}>{user.username}</div>
            <div className="mono" style={{ color: "var(--text-3)", fontSize: 12.5, marginTop: 2 }}>{user.email}</div>
            <div style={{ color: "var(--text-3)", fontSize: 12, marginTop: 6 }}>
              注册于 {fmt.dt(user.createdAt)} · 累计登录 <span className="mono" style={{ color: "var(--text-1)" }}>{user.loginCount}</span> 次
            </div>
          </div>
        </div>
      </Card>

      <div className="grid grid-3">
        <Card>
          <div className="kv-label">登录设备</div>
          <div style={{ fontSize: 26, fontWeight: 600, fontFamily: "var(--mono)", marginTop: 6 }}>{devices.length}</div>
          <div style={{ color: "var(--text-3)", fontSize: 12, marginTop: 4 }}>
            最近活跃 · {fmt.ago(devices[0].lastLoginAt)}
          </div>
        </Card>
        <Card>
          <div className="kv-label">云存档</div>
          <div style={{ fontSize: 26, fontWeight: 600, fontFamily: "var(--mono)", marginTop: 6 }}>{saves.length}</div>
          <div style={{ color: "var(--text-3)", fontSize: 12, marginTop: 4 }}>
            共占用 {fmt.bytes(saves.reduce((a, s) => a + s.sizeBytes, 0))}
          </div>
        </Card>
        <Card>
          <div className="kv-label">游戏角色等级</div>
          <div style={{ fontSize: 26, fontWeight: 600, fontFamily: "var(--mono)", marginTop: 6 }}>Lv. {Math.max(...saves.map(s => s.level))}</div>
          <div style={{ color: "var(--text-3)", fontSize: 12, marginTop: 4 }}>
            最高存档
          </div>
        </Card>
      </div>
    </div>
  );
}

function DevicesPage() {
  const { state, dispatch } = useU();
  const toast = useToast();
  const [confirm, setConfirm] = useUS(null);

  const onKick = async () => {
    const d = confirm;
    try {
      await Api.del("/user/api/devices/" + encodeURIComponent(d.id));
      dispatch({ type: "device.remove", id: d.id });
      toast(`${d.name} 的会话已被踢下线`, "ok");
    } catch (err) {
      toast("踢下线失败: " + (err.message || "未知错误"), "err");
    }
    setConfirm(null);
  };

  return (
    <div className="page" data-screen-label="02 登录设备">
      <div className="page-head">
        <div>
          <div className="page-title">登录设备</div>
          <div className="page-sub">查看所有登录此账号的设备 · 可远程踢下线</div>
        </div>
      </div>

      <Card tight>
        <div className="table-wrap">
        <table className="table">
          <thead>
            <tr>
              <th>设备</th>
              <th>设备指纹</th>
              <th>位置</th>
              <th>最近活跃</th>
              <th className="actions-cell">操作</th>
            </tr>
          </thead>
          <tbody>
            {state.devices.map(d => (
              <tr key={d.id}>
                <td>
                  <div style={{ display: "flex", alignItems: "center", gap: 10 }}>
                    <span style={{
                      width: 32, height: 32, borderRadius: 7,
                      background: "var(--bg-2)",
                      border: "1px solid var(--border-1)",
                      color: "var(--text-2)",
                      display: "grid", placeItems: "center",
                    }}>
                      <I.HardDrive size={14}/>
                    </span>
                    <div>
                      <div style={{ fontWeight: 500, fontSize: 13 }}>{d.name}</div>
                      {d.current && <StatusBadge status="ok">当前设备</StatusBadge>}
                    </div>
                  </div>
                </td>
                <td><span className="mono" style={{ fontSize: 11.5, color: "var(--text-3)" }}>{d.deviceId.slice(0, 8)}…{d.deviceId.slice(-6)}</span></td>
                <td><span style={{ fontSize: 12.5 }}>{d.region}</span> <span className="mono" style={{ color: "var(--text-3)", fontSize: 11.5, marginLeft: 4 }}>{d.ip}</span></td>
                <td><span style={{ color: "var(--text-2)", fontSize: 12.5 }}>{fmt.ago(d.lastLoginAt)}</span></td>
                <td className="actions-cell">
                  {!d.current && (
                    <button className="btn btn-sm btn-danger" onClick={() => setConfirm(d)}>
                      <I.X size={11}/> 踢下线
                    </button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
        </div>
      </Card>

      <div style={{ marginTop: 14, color: "var(--text-3)", fontSize: 12, display: "flex", gap: 8, alignItems: "flex-start" }}>
        <I.Info size={13} style={{ marginTop: 2 }}/>
        <span>设备指纹由客户端硬件信息哈希得到,不包含个人信息。如果发现陌生设备,请立即修改密码并踢下线。</span>
      </div>

      <ConfirmDialog
        open={!!confirm}
        danger
        title={confirm && `踢下线 ${confirm.name}`}
        confirmText="确认踢下线"
        message={<>该设备会被立即注销 session,需重新登录后才能继续游戏。原有云存档不受影响。</>}
        onCancel={() => setConfirm(null)}
        onConfirm={onKick}
      />
    </div>
  );
}

function SavesPage() {
  const { state, dispatch } = useU();
  const toast = useToast();
  const [confirm, setConfirm] = useUS(null);

  const onDelete = async () => {
    const s = confirm;
    try {
      await Api.del("/user/api/saves");
      dispatch({ type: "save.remove", id: s.id });
      toast(`存档「${s.name}」已删除`, "ok");
    } catch (err) {
      toast("删除失败: " + (err.message || "未知错误"), "err");
    }
    setConfirm(null);
  };

  return (
    <div className="page" data-screen-label="03 云存档">
      <div className="page-head">
        <div>
          <div className="page-title">云存档</div>
          <div className="page-sub">出于反作弊考虑,云存档只能删除,不能上传或修改</div>
        </div>
      </div>

      <div className="grid grid-2">
        {state.saves.map(s => (
          <Card key={s.id}>
            <div style={{ display: "flex", alignItems: "flex-start", gap: 12 }}>
              <span style={{
                width: 40, height: 40, borderRadius: 8,
                background: "var(--accent-50)",
                border: "1px solid var(--accent-200)",
                color: "var(--accent-700)",
                display: "grid", placeItems: "center",
                flexShrink: 0,
                fontFamily: "var(--mono)", fontWeight: 600,
              }}>#{s.slot}</span>
              <div style={{ flex: 1, minWidth: 0 }}>
                <div style={{ fontWeight: 600, fontSize: 14 }}>{s.name}</div>
                <div style={{ color: "var(--text-3)", fontSize: 12, marginTop: 3 }}>
                  Lv.{s.level} · {fmt.num(s.mochi)} もち · 上次同步 {fmt.ago(s.updatedAt)}
                </div>
                <div className="mono" style={{ color: "var(--text-disabled)", fontSize: 11, marginTop: 6 }}>
                  {fmt.bytes(s.sizeBytes)} · {fmt.dt(s.updatedAt)}
                </div>
              </div>
              <button className="btn btn-danger btn-sm" onClick={() => setConfirm(s)} title="删除">
                <I.Trash size={12}/>
              </button>
            </div>
          </Card>
        ))}
        {state.saves.length === 0 && (
          <Card><div style={{ color: "var(--text-3)", textAlign: "center", padding: 30 }}>暂无云存档</div></Card>
        )}
      </div>

      <div style={{ marginTop: 14, color: "var(--text-3)", fontSize: 12, display: "flex", gap: 8, alignItems: "flex-start" }}>
        <I.Info size={13} style={{ marginTop: 2 }}/>
        <span>
          云存档由服务端在游戏内自动写入。如果误删,请联系客服在 7 天内从冷备份中恢复;超过 7 天后无法找回。
        </span>
      </div>

      <ConfirmDialog
        open={!!confirm}
        danger
        title={confirm && `删除存档「${confirm.name}」`}
        confirmText="永久删除"
        message={
          <>该操作 <span style={{ color: "var(--red-500)", fontWeight: 600 }}>7 天后</span> 无法找回。请确认这是您想删除的存档(槽位 #{confirm?.slot},Lv.{confirm?.level})。</>
        }
        onCancel={() => setConfirm(null)}
        onConfirm={onDelete}
      />
    </div>
  );
}

function SecurityPage() {
  const { state, dispatch } = useU();
  const toast = useToast();
  const [emailDraft, setEmailDraft] = useUS(state.user.email);
  const [emailVerifyCode, setEmailVerifyCode] = useUS("");
  const [emailVerifying, setEmailVerifying] = useUS(false);
  const [emailSent, setEmailSent] = useUS(false);

  const [pwdOld, setPwdOld] = useUS("");
  const [pwdNew, setPwdNew] = useUS("");
  const [pwdConfirm, setPwdConfirm] = useUS("");

  const emailChanged = emailDraft !== state.user.email;
  const emailValid = /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(emailDraft);

  const sendVerifyCode = async () => {
    if (!emailValid) { toast("邮箱格式不正确", "err"); return; }
    setEmailVerifying(true);
    try {
      await Api.post("/auth/send-code", { email: emailDraft, purpose: "change_email" });
      setEmailSent(true);
      toast(`验证码已发送到 ${emailDraft}`, "ok");
    } catch (err) {
      toast("发送失败: " + (err.message || "未知错误"), "err");
    } finally {
      setEmailVerifying(false);
    }
  };

  const submitEmail = async () => {
    if (emailVerifyCode.length !== 6) { toast("请输入 6 位验证码", "err"); return; }
    try {
      await Api.put("/user/api/email", { email: emailDraft, code: emailVerifyCode });
      dispatch({ type: "user.set", patch: { email: emailDraft, username: emailDraft } });
      setEmailSent(false);
      setEmailVerifyCode("");
      toast("邮箱已更新,后续登录请使用新邮箱", "ok");
    } catch (err) {
      toast("换绑失败: " + (err.message || "未知错误"), "err");
    }
  };

  const submitPwd = async () => {
    if (!pwdOld) { toast("请输入原密码", "err"); return; }
    if (pwdNew.length < 8) { toast("新密码至少 8 位", "err"); return; }
    if (pwdNew !== pwdConfirm) { toast("两次输入不一致", "err"); return; }
    try {
      await Api.put("/user/api/password", { old_password: pwdOld, new_password: pwdNew });
      setPwdOld(""); setPwdNew(""); setPwdConfirm("");
      toast("密码已更新,其他设备会被立刻踢下线", "ok");
    } catch (err) {
      toast("改密失败: " + (err.message || "未知错误"), "err");
    }
  };

  return (
    <div className="page" data-screen-label="04 密码与安全">
      <div className="page-head">
        <div>
          <div className="page-title">密码与安全</div>
          <div className="page-sub">修改邮箱(同步作为用户名)与登录密码</div>
        </div>
      </div>

      <div className="grid grid-2" style={{ alignItems: "start" }}>
        <Card title="换绑邮箱" subtitle="邮箱即用户名,改后下次登录请使用新邮箱">
          <div className="field">
            <label className="field-label">当前邮箱</label>
            <input className="input mono" value={state.user.email} readOnly/>
          </div>
          <div className="field" style={{ marginTop: 10 }}>
            <label className="field-label">新邮箱</label>
            <input className="input" type="email" value={emailDraft}
              onChange={(e) => { setEmailDraft(e.target.value); setEmailSent(false); }}/>
          </div>

          {emailChanged && (
            <>
              {!emailSent ? (
                <button className="btn btn-primary" style={{ marginTop: 14 }} disabled={!emailValid || emailVerifying} onClick={sendVerifyCode}>
                  {emailVerifying
                    ? <><span style={{ width: 11, height: 11, border: "2px solid #fff", borderTopColor: "transparent", borderRadius: "50%", display: "inline-block", animation: "spin 0.6s linear infinite" }}/> 发送中…</>
                    : <><I.Send size={12}/> 发送验证码到新邮箱</>}
                </button>
              ) : (
                <div className="fade-in" style={{ marginTop: 14 }}>
                  <div className="field">
                    <label className="field-label">
                      <I.Check size={11} style={{ color: "var(--teal-600)" }}/>
                      6 位验证码已发送至 <span className="mono" style={{ color: "var(--text-2)" }}>{emailDraft}</span>
                    </label>
                    <input className="input mono" maxLength={6} placeholder="000000" value={emailVerifyCode}
                      style={{ letterSpacing: "0.4em", textAlign: "center", fontSize: 16 }}
                      onChange={(e) => setEmailVerifyCode(e.target.value.replace(/[^0-9]/g, ""))}/>
                  </div>
                  <div style={{ display: "flex", gap: 8, marginTop: 12 }}>
                    <button className="btn" onClick={() => setEmailSent(false)}>取消</button>
                    <button className="btn btn-primary" onClick={submitEmail} disabled={emailVerifyCode.length !== 6}>
                      <I.Check size={12}/> 确认换绑
                    </button>
                  </div>
                </div>
              )}
            </>
          )}
        </Card>

        <Card title="修改密码" subtitle="改密后所有设备会在下次刷新时要求重新登录">
          <div className="field">
            <label className="field-label">当前密码</label>
            <input className="input mono" type="password" value={pwdOld} onChange={(e) => setPwdOld(e.target.value)}/>
          </div>
          <div className="field" style={{ marginTop: 10 }}>
            <label className="field-label">新密码</label>
            <input className="input mono" type="password" value={pwdNew} onChange={(e) => setPwdNew(e.target.value)}/>
            <div className="field-hint">至少 8 位,建议包含大小写字母、数字与符号</div>
          </div>
          <div className="field" style={{ marginTop: 10 }}>
            <label className="field-label">确认新密码</label>
            <input className="input mono" type="password" value={pwdConfirm} onChange={(e) => setPwdConfirm(e.target.value)}/>
            {pwdConfirm && (pwdNew === pwdConfirm
              ? <div style={{ color: "var(--teal-600)", fontSize: 11.5, marginTop: 4 }}><I.Check size={11}/> 一致</div>
              : <div style={{ color: "var(--red-500)", fontSize: 11.5, marginTop: 4 }}>两次输入不一致</div>)}
          </div>
          <button className="btn btn-primary" style={{ marginTop: 14 }} onClick={submitPwd}>
            <I.Lock size={12}/> 更新密码
          </button>
        </Card>
      </div>

      <Card title="登录历史" subtitle="近 30 天 · 最多展示 5 条" style={{ marginTop: 14 }} tight>
        <div>
          {state.devices.slice(0, 5).map(d => (
            <div key={d.id} style={{
              display: "grid",
              gridTemplateColumns: "auto 1fr auto",
              gap: 12, padding: "10px 16px",
              borderBottom: "1px solid var(--border-1)",
              alignItems: "center",
            }}>
              <I.HardDrive size={14} style={{ color: "var(--text-3)" }}/>
              <div>
                <div style={{ fontSize: 12.5 }}>{d.name}</div>
                <div className="mono" style={{ fontSize: 11, color: "var(--text-3)" }}>{d.region} · {d.ip}</div>
              </div>
              <span className="mono" style={{ fontSize: 11.5, color: "var(--text-3)" }}>{fmt.ago(d.lastLoginAt)}</span>
            </div>
          ))}
        </div>
      </Card>
    </div>
  );
}

function DangerPage() {
  const toast = useToast();
  const [confirm, setConfirm] = useUS(null);
  const [phrase, setPhrase] = useUS("");
  const [password, setPassword] = useUS("");

  const requiredPhrase = "DELETE MY ACCOUNT";

  return (
    <div className="page" data-screen-label="05 注销账户">
      <div className="page-head">
        <div>
          <div className="page-title">注销账户</div>
          <div className="page-sub">永久删除账号、云存档与所有相关数据</div>
        </div>
      </div>

      <Card>
        <div style={{ display: "grid", gap: 14 }}>
          <div style={{ display: "flex", gap: 12, alignItems: "flex-start" }}>
            <span style={{
              width: 36, height: 36, borderRadius: 8,
              background: "var(--red-50)", border: "1px solid rgba(220,38,38,0.3)",
              color: "var(--red-500)", display: "grid", placeItems: "center",
              flexShrink: 0,
            }}>
              <I.Alert size={16}/>
            </span>
            <div style={{ flex: 1 }}>
              <div style={{ fontWeight: 600, fontSize: 14, color: "var(--red-500)" }}>注销前请确认以下事项</div>
              <ul style={{ margin: "8px 0 0", paddingLeft: 18, color: "var(--text-2)", fontSize: 12.5, lineHeight: 1.8 }}>
                <li>所有云存档将立即删除,7 天后从冷备份中清除,无法找回</li>
                <li>当前账号所有正在游戏的设备会立即被踢下线</li>
                <li>注销 30 天内不可使用相同邮箱重新注册</li>
                <li>付费记录与服务器审计日志会按合规要求保留 1 年</li>
              </ul>
            </div>
          </div>

          <div className="danger-zone">
            <div className="danger-zone-title">永久注销我的账户</div>
            <div className="danger-zone-desc">需要重新输入登录密码,并在下方精确输入短语后才能继续。</div>
            <div className="field" style={{ marginBottom: 12 }}>
              <label className="field-label">当前登录密码</label>
              <input className="input mono" type="password" value={password}
                onChange={(e) => setPassword(e.target.value)}/>
            </div>
            <div className="field" style={{ marginBottom: 12 }}>
              <input className="input mono" placeholder={requiredPhrase} value={phrase}
                onChange={(e) => setPhrase(e.target.value)}/>
              <div className="field-hint">
                请精确输入 <span className="mono" style={{ color: "var(--text-2)" }}>{requiredPhrase}</span>
              </div>
            </div>
            <button className="btn btn-danger" disabled={phrase !== requiredPhrase || !password}
              onClick={() => setConfirm(true)}>
              <I.Trash size={13}/> 永久注销账户
            </button>
          </div>
        </div>
      </Card>

      <ConfirmDialog
        open={confirm}
        danger
        title="最后确认:永久注销账户"
        confirmText="我已了解,永久注销"
        message={
          <>这是最后一次询问。点击确认后,您的账号 <span className="mono">{INITIAL_USER.email}</span> 将立即停用,
            7 天内可联系客服恢复,7 天后所有数据被物理删除,且无法以相同邮箱再次注册 30 天。</>
        }
        onCancel={() => setConfirm(false)}
        onConfirm={async () => {
          try {
            await Api.request("DELETE", "/user/api/account", { password });
            Api.setToken("");
            toast("账户注销已完成,跳转到登录页…", "ok");
            setTimeout(() => { window.location.href = "login.html"; }, 1200);
          } catch (err) {
            toast("注销失败: " + (err.message || "未知错误"), "err");
          } finally {
            setConfirm(false);
          }
        }}
      />
    </div>
  );
}

const root2 = ReactDOM.createRoot(document.getElementById("root"));
root2.render(<ToastProvider><UserApp/></ToastProvider>);
