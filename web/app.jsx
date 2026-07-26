/* Main app shell: sidebar nav + top bar + page router + global state. */

const NAV_ITEMS = [
  { key: "dashboard",  label: "仪表盘",     icon: "Dashboard", section: "运营" },
  { key: "server",     label: "服务器控制", icon: "Server",    section: "运营" },
  { key: "tasks",      label: "定时任务",   icon: "Clock",     section: "运营" },
  { key: "versions",   label: "版本管理",   icon: "Tag",       section: "资源" },
  { key: "resources",  label: "资源管理",   icon: "Package",   section: "资源" },
  { key: "hotupdate",  label: "热更新",     icon: "Zap",       section: "资源" },
  { key: "accounts",   label: "账号管理",   icon: "User",      section: "用户" },
  { key: "devices",    label: "设备封禁",   icon: "Ban",       section: "用户" },
  { key: "heartbeat",  label: "心跳监控",   icon: "Heart",     section: "用户" },
  { key: "captcha",    label: "验证码",     icon: "Shield",    section: "系统" },
  { key: "audit",      label: "审计日志",   icon: "Logs",      section: "系统" },
  { key: "permissions",label: "权限管理",   icon: "Key",       section: "系统", superOnly: true },
];

/* ----------------- State reducer ----------------- */
function appReducer(state, action) {
  switch (action.type) {
    case "hydrate":
      return { ...state, ...action.state, loading: false };
    case "server.set":
      return { ...state, server: { ...state.server, ...action.patch } };
    case "versions.set":
      return { ...state, versions: action.value };
    case "services.set":
      return { ...state, services: { ...(state.services || {}), ...action.patch } };
    case "mirrors.add":
      return { ...state, mirrors: [...state.mirrors, action.mirror] };
    case "mirrors.remove":
      return { ...state, mirrors: state.mirrors.filter((_, i) => i !== action.index) };
    case "mirrors.update": {
      const list = state.mirrors.map((m, i) => i === action.index ? { ...m, ...action.patch } : m);
      return { ...state, mirrors: list };
    }
    case "mirrors.reorder": {
      const list = [...state.mirrors];
      const [m] = list.splice(action.from, 1);
      list.splice(action.to, 0, m);
      return { ...state, mirrors: list };
    }
    case "mirrors.setLimits": {
      // 更新对应镜像的 stats 中的 limit 字段（乐观更新）
      const list = state.mirrors.map(m => m.url === action.url
        ? { ...m, stats: { ...(m.stats || {}), daily_limit_bytes: action.dailyLimitBytes, speed_limit_bps: action.speedLimitBps } }
        : m
      );
      return { ...state, mirrors: list };
    }
    case "mirrors.statsRefresh":
      return { ...state, mirrors: action.mirrors };
    case "nodes.set":
      return { ...state, nodes: { ...state.nodes, [action.which]: { ...state.nodes[action.which], ...action.patch } } };
    case "pipeline.set":
      return { ...state, pipeline: { ...state.pipeline, ...action.patch } };
    case "pipeline.triggerSync":
      return { ...state, pipeline: {
        ...state.pipeline,
        releases: [action.release, ...state.pipeline.releases],
        lastPolledAt: Date.now(),
      }};
    case "autoPackage.set":
      return { ...state, autoPackage: { ...state.autoPackage, ...action.patch } };
    case "tasks.set":
      return { ...state, tasks: { ...state.tasks, ...action.patch } };
    case "unverifiedPurge.set":
      return { ...state, unverifiedPurge: { ...state.unverifiedPurge, ...action.patch } };
    case "limits.set":
      return { ...state, limits: { ...state.limits, ...action.patch } };
    case "admin.add":
      return { ...state, adminRoster: [action.value, ...state.adminRoster] };
    case "admin.update":
      return { ...state, adminRoster: state.adminRoster.map(a => a.id === action.id ? { ...a, ...action.patch } : a) };
    case "admin.remove":
      return { ...state, adminRoster: state.adminRoster.filter(a => a.id !== action.id) };
    case "offlinePackage.set":
      return { ...state, offlinePackage: action.value };
    case "jsBundle.set":
      return { ...state, jsBundle: action.value };
    case "scenarioBundle.set":
      return { ...state, scenarioBundle: action.value };
    // *.loaded:发布/上传由页面直接 await API(服务端算 sha256/size/url),拿到结果后
    // 只更新本地展示,不再触发副作用请求(adminApiCall 无此 case)。
    case "jsBundle.loaded":
      return { ...state, jsBundle: action.value };
    case "scenarioBundle.loaded":
      return { ...state, scenarioBundle: action.value };
    case "captcha.set":
      return { ...state, captcha: { ...state.captcha, ...action.patch } };
    case "autoban.set":
      return { ...state, autoban: action.value };
    case "accounts.add":
      return { ...state, accounts: [action.value, ...state.accounts] };
    case "accounts.update":
      return { ...state, accounts: state.accounts.map(a => a.id === action.id ? { ...a, ...action.patch } : a) };
    case "accounts.remove":
      return { ...state, accounts: state.accounts.filter(a => a.id !== action.id) };
    case "bans.add":
      return { ...state, activeBans: [action.value, ...state.activeBans] };
    case "bans.lift": {
      const b = state.activeBans.find(x => x.id === action.id);
      if (!b) return state;
      const histEntry = { ...b, expiredAt: Date.now(), liftedBy: "admin_homura" };
      return {
        ...state,
        activeBans: state.activeBans.filter(x => x.id !== action.id),
        banHistory: [histEntry, ...state.banHistory],
      };
    }
    case "heartbeats.set":
      return { ...state, heartbeats: action.value };
    case "heartbeat.tick": {
      const list = state.heartbeats.map(h => {
        if (h.id !== action.id) return h;
        const inc = Math.random() * 3 + 0.5;
        const newProg = Math.min(100, h.progress + inc);
        return {
          ...h,
          progress: newProg,
          speedBps: Math.max(50 * 1024, h.speedBps + (Math.random() - 0.5) * 200 * 1024),
          lastHeartbeat: Date.now(),
        };
      });
      return { ...state, heartbeats: list };
    }
    case "heartbeat.remove":
      return { ...state, heartbeats: state.heartbeats.filter(h => h.id !== action.id) };
    case "audit.add":
      return {
        ...state,
        auditLog: [
          {
            id: `log_${Date.now()}_${Math.random().toString(36).slice(2, 5)}`,
            ts: Date.now(),
            actor: action.entry.actor || "admin_homura",
            type: action.entry.type,
            target: action.entry.target || "—",
            details: { ip: "10.7.42.18", ...(action.entry.details || {}) },
          },
          ...state.auditLog,
        ],
      };
    case "events.push":
      return {
        ...state,
        events: [
          { id: `evt_${Date.now()}`, ts: Date.now(), ...action.entry },
          ...state.events,
        ].slice(0, 30),
      };
    default:
      return state;
  }
}

// 初始状态用 mock 数据兜底,实际值由 /admin/state 在 App 挂载后注入。
// loading=true 时不渲染主面板,避免页面读到空字段崩溃。
const initialState = {
  loading: true,
  server: MOCK.initialServerState,
  versions: MOCK.initialVersions,
  services: MOCK.initialServices,
  mirrors: MOCK.initialMirrors,
  nodes: MOCK.initialNodes,
  pipeline: MOCK.initialPipeline,
  autoPackage: MOCK.initialAutoPackage,
  tasks: MOCK.initialTasks,
  unverifiedPurge: MOCK.initialUnverifiedPurge,
  limits: MOCK.initialLimits,
  currentAdmin: MOCK.initialCurrentAdmin,
  adminRoster: MOCK.initialAdminRoster,
  offlinePackage: MOCK.initialOfflinePackage,
  jsBundle: MOCK.initialJsBundle,
  scenarioBundle: MOCK.initialScenarioBundle,
  captcha: MOCK.initialCaptcha,
  autoban: MOCK.initialAutoban,
  accounts: MOCK.initialAccounts,
  activeBans: MOCK.initialActiveBans,
  banHistory: MOCK.initialBanHistory,
  heartbeats: MOCK.initialHeartbeats,
  auditLog: MOCK.initialAuditLog,
  events: MOCK.EVENT_LIST,
};

/* ----------------- Sidebar ----------------- */
function Sidebar({ active, setActive, state, mobileOpen, onMobileClose }) {
  const sectionTitles = ["运营", "资源", "用户", "系统"];

  return (
    <aside className={`sidebar ${mobileOpen ? "mobile-open" : ""}`}>
      <div className="brand">
        <span className="brand-mark" role="img" aria-label="魔法纪录复兴计划">魔</span>
        <div style={{ flex: 1 }}>
          <div className="brand-name">魔法纪录复兴计划</div>
          <div className="brand-sub">magireco-revival.cn</div>
        </div>
        <button className="btn btn-ghost btn-icon sidebar-close-mobile" onClick={onMobileClose} aria-label="关闭菜单">
          <I.X size={16}/>
        </button>
      </div>

          {sectionTitles.map(section => {
            const items = NAV_ITEMS.filter(n => n.section === section && (!n.superOnly || state.currentAdmin.role === "super_admin"));
            if (items.length === 0) return null;
            return (
              <div className="nav-section" key={section}>
                <div className="nav-section-title">{section}</div>
                {items.map(n => {
                  const Icon = I[n.icon];
                  let badge = null;
                  if (n.key === "devices")    badge = state.activeBans.length;
                  if (n.key === "heartbeat")  badge = state.heartbeats.length;
                  if (n.key === "accounts")   badge = state.accounts.filter(a => a.status === "active").length;
                  if (n.key === "permissions") badge = state.adminRoster.length;
                  return (
                    <button key={n.key}
                      className={`nav-item ${active === n.key ? "active" : ""}`}
                      onClick={() => { setActive(n.key); onMobileClose?.(); }}>
                      <span className="ico"><Icon size={15}/></span>
                      <span>{n.label}</span>
                      {badge != null && <span className="badge">{badge}</span>}
                    </button>
                  );
                })}
              </div>
            );
          })}

      <div className="sidebar-foot">
        <span className="dot"/>
        <span>magireco-srv-01 · v0.9.4</span>
      </div>
    </aside>
  );
}

/* ----------------- Top bar ----------------- */
function TopBar({ active, state, onMenu }) {
  const here = NAV_ITEMS.find(n => n.key === active);
  const { server, currentAdmin } = state;
  const [now, setNow] = useState(new Date());
  const [userMenuOpen, setUserMenuOpen] = useState(false);
  const userMenuRef = useRef(null);
  const toast = useToast();

  useEffect(() => { const t = setInterval(() => setNow(new Date()), 1000); return () => clearInterval(t); }, []);

  useEffect(() => {
    if (!userMenuOpen) return;
    const handler = (e) => {
      if (userMenuRef.current && !userMenuRef.current.contains(e.target)) setUserMenuOpen(false);
    };
    setTimeout(() => document.addEventListener("mousedown", handler), 0);
    return () => document.removeEventListener("mousedown", handler);
  }, [userMenuOpen]);

  const onLogout = async () => {
    setUserMenuOpen(false);
    try { await Api.post("/auth/logout", {}); } catch (_) {}
    Api.setToken("");
    toast("正在退出登录…", "ok");
    setTimeout(() => { window.location.href = "login.html"; }, 400);
  };

  const roleInfo = MOCK.ROLE_LABELS[currentAdmin.role] || MOCK.ROLE_LABELS.admin;

  return (
    <div className="topbar">
      <button className="btn btn-ghost btn-icon menu-btn" onClick={onMenu} aria-label="菜单">
        <I.Logs size={16}/>
      </button>
      <div className="crumbs">
        <span className="hide-mobile">管理后端 <I.ChevRight size={11} style={{ verticalAlign: "middle", color: "var(--text-disabled)" }}/>{" "}
        <span style={{ marginLeft: 4 }}>{here?.section}</span>
        <I.ChevRight size={11} style={{ verticalAlign: "middle", color: "var(--text-disabled)", margin: "0 2px" }}/></span>
        <span className="here">{here?.label}</span>
      </div>
      <div className="spacer"/>
      <div className="topbar-meta">
        <span className="hide-mobile">{now.toLocaleString("zh-CN", { hour12: false })}</span>
        <span className="hide-mobile" style={{ color: "var(--border-strong)" }}>|</span>
        <StatusBadge status={server.status === "ok" ? "ok" : server.status === "warn" ? "warn" : "err"}>
          {server.status === "ok" ? "服务正常" : server.status === "warn" ? "维护中" : "服务异常"}
        </StatusBadge>
      </div>
      <BackgroundPicker/>

      {/* User menu */}
      <div ref={userMenuRef} style={{ position: "relative" }}>
        <button
          className="btn btn-ghost"
          onClick={() => setUserMenuOpen(o => !o)}
          style={{ paddingLeft: 6, paddingRight: 10, gap: 8 }}>
          <div style={{
            width: 28, height: 28, borderRadius: "50%",
            background: "linear-gradient(135deg, var(--accent-500), var(--accent-700))",
            color: "#fff", display: "grid", placeItems: "center",
            fontSize: 12, fontWeight: 600,
            flexShrink: 0,
          }}>{currentAdmin.avatarLetter}</div>
          <div className="hide-mobile" style={{ display: "flex", flexDirection: "column", alignItems: "flex-start", lineHeight: 1.15 }}>
            <span style={{ fontSize: 12.5, color: "var(--text-1)", fontWeight: 500 }}>{currentAdmin.username}</span>
            <span style={{ fontSize: 10.5, color: "var(--text-3)", fontFamily: "var(--mono)" }}>{roleInfo.label}</span>
          </div>
          <I.ChevDown size={12} style={{ color: "var(--text-3)" }} className="hide-mobile"/>
        </button>

        {userMenuOpen && (
          <div className="user-menu">
            <div className="user-menu-head">
              <div style={{
                width: 36, height: 36, borderRadius: "50%",
                background: "linear-gradient(135deg, var(--accent-500), var(--accent-700))",
                color: "#fff", display: "grid", placeItems: "center",
                fontSize: 15, fontWeight: 600,
                flexShrink: 0,
              }}>{currentAdmin.avatarLetter}</div>
              <div style={{ flex: 1, minWidth: 0 }}>
                <div style={{ fontWeight: 600, fontSize: 13 }}>{currentAdmin.username}</div>
                <div style={{ color: "var(--text-3)", fontSize: 11.5, fontFamily: "var(--mono)" }}>{currentAdmin.email}</div>
              </div>
              <StatusBadge status={roleInfo.color}>{roleInfo.label}</StatusBadge>
            </div>
            <div className="user-menu-list">
              <a className="user-menu-item" href="user.html" target="_blank">
                <I.ExternalLink size={13}/>
                <span>打开用户面板</span>
              </a>
              <button className="user-menu-item" onClick={() => { setUserMenuOpen(false); toast("个人设置即将开放", "ok"); }}>
                <I.Settings size={13}/>
                <span>个人设置</span>
              </button>
              <div className="user-menu-divider"/>
              <button className="user-menu-item danger" onClick={onLogout}>
                <I.X size={13}/>
                <span>退出登录</span>
              </button>
            </div>
          </div>
        )}
      </div>
    </div>
  );
}

/* ----------------- App ----------------- */
function App() {
  const [active, setActive] = useState("dashboard");
  const [state, rawDispatch] = React.useReducer(appReducer, initialState);
  const [navOpen, setNavOpen] = useState(false);
  const toast = useToast();
  // wrapDispatch 需要在异步回调里拿"最新 state",但 reducer 是异步刷新的;
  // 用 ref 镜像当前 state,getState() 总是返回上一次 dispatch 结束后的值。
  const stateRef = React.useRef(state);
  React.useEffect(() => { stateRef.current = state; }, [state]);
  const dispatch = React.useMemo(
    () => wrapDispatch(rawDispatch, () => stateRef.current, toast),
    [toast]
  );

  // 首屏:从后端拉一份完整状态注入 reducer
  useEffect(() => {
    let cancelled = false;
    fetchAdminState()
      .then(s => { if (!cancelled) rawDispatch({ type: "hydrate", state: s }); })
      .catch(err => {
        if (err.status === 401) return; // Api 已跳登录
        toast("加载面板数据失败: " + (err.message || "未知错误"), "err");
      });
    return () => { cancelled = true; };
  }, [toast]);

  useEffect(() => {
    const handler = (e) => { if (e.key === "Escape") setNavOpen(false); };
    window.addEventListener("keydown", handler);
    return () => window.removeEventListener("keydown", handler);
  }, []);

  // keyboard shortcuts: 1-0 jumps to pages
  useEffect(() => {
    const handler = (e) => {
      if (e.target.tagName === "INPUT" || e.target.tagName === "TEXTAREA") return;
      const n = parseInt(e.key, 10);
      if (!isNaN(n) && n >= 1 && n <= NAV_ITEMS.length) {
        setActive(NAV_ITEMS[n - 1].key);
      }
    };
    window.addEventListener("keydown", handler);
    return () => window.removeEventListener("keydown", handler);
  }, []);

  const Page = {
    dashboard: PageDashboard,
    server: PageServerControl,
    tasks: PageTasks,
    versions: PageVersions,
    resources: PageResources,
    hotupdate: PageHotUpdate,
    accounts: PageAccounts,
    devices: PageDevices,
    heartbeat: PageHeartbeat,
    captcha: PageCaptcha,
    audit: PageAudit,
    permissions: PagePermissions,
  }[active];

  if (state.loading) {
    return (
      <div style={{ display: "grid", placeItems: "center", height: "100vh", fontSize: 14, color: "var(--text-2)" }}>
        <div>
          <span style={{ width: 14, height: 14, border: "2px solid var(--accent-500)", borderTopColor: "transparent", borderRadius: "50%", display: "inline-block", animation: "spin 0.6s linear infinite", marginRight: 8 }}/>
          正在加载管理后台…
        </div>
      </div>
    );
  }
  return (
    <AppCtx.Provider value={{ state, dispatch }}>
      <div className="app">
        <Sidebar active={active} setActive={(k) => { setActive(k); setNavOpen(false); }} state={state}
          mobileOpen={navOpen} onMobileClose={() => setNavOpen(false)}/>
        {navOpen && <div className="nav-mask" onClick={() => setNavOpen(false)}/>}
        <div className="main">
          <TopBar active={active} state={state} onMenu={() => setNavOpen(true)}/>
          <Page/>
        </div>
      </div>
    </AppCtx.Provider>
  );
}

const root = ReactDOM.createRoot(document.getElementById("root"));
root.render(<ToastProvider><App/></ToastProvider>);
