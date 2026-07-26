// Shared UI components & icons.
// Exposes to window so each <script type="text/babel"> can use them.

const { useState, useEffect, useRef, useMemo, useCallback, createContext, useContext } = React;

/* ----------------- Icons (lucide-style strokes, inline) ----------------- */
const Icon = ({ d, size = 16, fill = "none", stroke = "currentColor", sw = 1.7, ...rest }) => (
  <svg width={size} height={size} viewBox="0 0 24 24" fill={fill} stroke={stroke}
    strokeWidth={sw} strokeLinecap="round" strokeLinejoin="round" {...rest}>
    {d}
  </svg>
);
const I = {
  Dashboard: (p) => <Icon {...p} d={<><rect x="3" y="3" width="7" height="9"/><rect x="14" y="3" width="7" height="5"/><rect x="14" y="12" width="7" height="9"/><rect x="3" y="16" width="7" height="5"/></>}/>,
  Server: (p) => <Icon {...p} d={<><rect x="3" y="4" width="18" height="7" rx="2"/><rect x="3" y="13" width="18" height="7" rx="2"/><path d="M7 8h.01M7 17h.01"/></>}/>,
  Tag: (p) => <Icon {...p} d={<><path d="M20 12l-8 8-9-9V3h8z"/><circle cx="7.5" cy="7.5" r="1"/></>}/>,
  Package: (p) => <Icon {...p} d={<><path d="M12 2l9 5v10l-9 5-9-5V7z"/><path d="M3.3 7L12 12l8.7-5M12 22V12"/></>}/>,
  Zap: (p) => <Icon {...p} d={<path d="M13 2L3 14h7l-1 8 10-12h-7z"/>}/>,
  User: (p) => <Icon {...p} d={<><circle cx="12" cy="8" r="4"/><path d="M4 21c1.5-4 4.5-6 8-6s6.5 2 8 6"/></>}/>,
  Ban: (p) => <Icon {...p} d={<><circle cx="12" cy="12" r="9"/><path d="M5.6 5.6l12.8 12.8"/></>}/>,
  Heart: (p) => <Icon {...p} d={<path d="M3 12h4l2-7 4 14 2-7h6"/>}/>,
  Shield: (p) => <Icon {...p} d={<><path d="M12 2l8 4v6c0 5-3.5 8.5-8 10-4.5-1.5-8-5-8-10V6z"/><path d="M9 12l2 2 4-4"/></>}/>,
  Logs: (p) => <Icon {...p} d={<><path d="M4 5h16M4 12h16M4 19h10"/></>}/>,
  Plus: (p) => <Icon {...p} d={<><path d="M12 5v14M5 12h14"/></>}/>,
  X: (p) => <Icon {...p} d={<><path d="M5 5l14 14M19 5L5 19"/></>}/>,
  Search: (p) => <Icon {...p} d={<><circle cx="11" cy="11" r="7"/><path d="M21 21l-4.3-4.3"/></>}/>,
  Copy: (p) => <Icon {...p} d={<><rect x="9" y="9" width="11" height="11" rx="2"/><path d="M5 15V5a2 2 0 0 1 2-2h10"/></>}/>,
  Check: (p) => <Icon {...p} d={<path d="M5 13l4 4L19 7"/>}/>,
  Trash: (p) => <Icon {...p} d={<><path d="M4 7h16M9 7V4h6v3M6 7l1 13h10l1-13"/></>}/>,
  Edit: (p) => <Icon {...p} d={<><path d="M14.7 4.3l5 5L8 21H3v-5z"/></>}/>,
  Refresh: (p) => <Icon {...p} d={<><path d="M21 12a9 9 0 1 1-3-6.7L21 8"/><path d="M21 3v5h-5"/></>}/>,
  Pause: (p) => <Icon {...p} d={<><rect x="6" y="5" width="4" height="14"/><rect x="14" y="5" width="4" height="14"/></>}/>,
  Play: (p) => <Icon {...p} d={<path d="M6 4l14 8L6 20z"/>}/>,
  Drag: (p) => <Icon {...p} d={<><circle cx="9" cy="6" r="1"/><circle cx="9" cy="12" r="1"/><circle cx="9" cy="18" r="1"/><circle cx="15" cy="6" r="1"/><circle cx="15" cy="12" r="1"/><circle cx="15" cy="18" r="1"/></>}/>,
  ChevDown: (p) => <Icon {...p} d={<path d="M6 9l6 6 6-6"/>}/>,
  ChevRight: (p) => <Icon {...p} d={<path d="M9 6l6 6-6 6"/>}/>,
  ChevLeft: (p) => <Icon {...p} d={<path d="M15 6l-6 6 6 6"/>}/>,
  ArrowUp: (p) => <Icon {...p} d={<><path d="M12 19V5M5 12l7-7 7 7"/></>}/>,
  ArrowDown: (p) => <Icon {...p} d={<><path d="M12 5v14M19 12l-7 7-7-7"/></>}/>,
  Alert: (p) => <Icon {...p} d={<><path d="M12 9v4M12 17h.01"/><path d="M10.3 3.4L1.8 18a2 2 0 0 0 1.7 3h17a2 2 0 0 0 1.7-3L13.7 3.4a2 2 0 0 0-3.4 0z"/></>}/>,
  Info: (p) => <Icon {...p} d={<><circle cx="12" cy="12" r="9"/><path d="M12 8v.01M11 12h1v5h1"/></>}/>,
  Key: (p) => <Icon {...p} d={<><circle cx="8" cy="15" r="4"/><path d="M10.85 12.15L21 2l-3 3M16 5l3 3"/></>}/>,
  Cloud: (p) => <Icon {...p} d={<path d="M17.5 19a4.5 4.5 0 0 0 .5-9c-.4-3.5-3.3-6-6.7-6-3 0-5.5 2-6.3 4.7C2.4 9 1 11 1 13.5 1 16.5 3.5 19 6.5 19z"/>}/>,
  HardDrive: (p) => <Icon {...p} d={<><line x1="3" y1="12" x2="21" y2="12"/><path d="M5 12V6a2 2 0 0 1 2-2h10a2 2 0 0 1 2 2v6"/><path d="M5 12v6a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2v-6"/><circle cx="7" cy="16" r=".5" fill="currentColor"/></>}/>,
  Download: (p) => <Icon {...p} d={<><path d="M12 3v12M7 10l5 5 5-5M5 21h14"/></>}/>,
  Lock: (p) => <Icon {...p} d={<><rect x="4" y="11" width="16" height="10" rx="2"/><path d="M8 11V8a4 4 0 0 1 8 0v3"/></>}/>,
  Eye: (p) => <Icon {...p} d={<><path d="M2 12s3.5-7 10-7 10 7 10 7-3.5 7-10 7-10-7-10-7z"/><circle cx="12" cy="12" r="3"/></>}/>,
  Settings: (p) => <Icon {...p} d={<><circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.7 1.7 0 0 0 .3 1.8l.1.1a2 2 0 1 1-2.8 2.8l-.1-.1a1.7 1.7 0 0 0-1.8-.3 1.7 1.7 0 0 0-1 1.5V21a2 2 0 0 1-4 0v-.1A1.7 1.7 0 0 0 9 19.4a1.7 1.7 0 0 0-1.8.3l-.1.1a2 2 0 1 1-2.8-2.8l.1-.1a1.7 1.7 0 0 0 .3-1.8 1.7 1.7 0 0 0-1.5-1H3a2 2 0 0 1 0-4h.1a1.7 1.7 0 0 0 1.5-1 1.7 1.7 0 0 0-.3-1.8l-.1-.1a2 2 0 1 1 2.8-2.8l.1.1a1.7 1.7 0 0 0 1.8.3 1.7 1.7 0 0 0 1-1.5V3a2 2 0 0 1 4 0v.1c0 .7.4 1.3 1 1.5a1.7 1.7 0 0 0 1.8-.3l.1-.1a2 2 0 1 1 2.8 2.8l-.1.1a1.7 1.7 0 0 0-.3 1.8c.3.6.8 1 1.5 1H21a2 2 0 0 1 0 4h-.1a1.7 1.7 0 0 0-1.5 1z"/></>}/>,
  ExternalLink: (p) => <Icon {...p} d={<><path d="M18 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h6"/><path d="M15 3h6v6M10 14L21 3"/></>}/>,
  More: (p) => <Icon {...p} d={<><circle cx="5" cy="12" r="1"/><circle cx="12" cy="12" r="1"/><circle cx="19" cy="12" r="1"/></>}/>,
  Filter: (p) => <Icon {...p} d={<path d="M3 5h18l-7 9v6l-4-2v-4z"/>}/>,
  Clock: (p) => <Icon {...p} d={<><circle cx="12" cy="12" r="9"/><path d="M12 7v5l3 2"/></>}/>,
  Send: (p) => <Icon {...p} d={<path d="M22 2L11 13M22 2l-7 20-4-9-9-4z"/>}/>,
  Wifi: (p) => <Icon {...p} d={<><path d="M2 9a16 16 0 0 1 20 0"/><path d="M5 13a11 11 0 0 1 14 0"/><path d="M8.5 16.5a6 6 0 0 1 7 0"/><circle cx="12" cy="20" r="0.5" fill="currentColor"/></>}/>,
  History: (p) => <Icon {...p} d={<><path d="M3 12a9 9 0 1 0 3-6.7L3 8"/><path d="M3 3v5h5"/><path d="M12 7v5l3 2"/></>}/>,
};

/* ----------------- Badges ----------------- */
function StatusBadge({ status, children }) {
  const map = {
    ok:   { cls: "badge-ok",   label: children || "正常" },
    warn: { cls: "badge-warn", label: children || "维护中" },
    err:  { cls: "badge-err",  label: children || "异常" },
    info: { cls: "badge-info", label: children || "信息" },
    muted:{ cls: "badge-muted",label: children || "—" },
  };
  const cfg = map[status] || map.muted;
  return <span className={`badge-pill ${cfg.cls}`}><span className="dot"/>{cfg.label}</span>;
}

function Tag({ children, onRemove }) {
  return (
    <span className="tag">
      <span>{children}</span>
      {onRemove && <span className="x" onClick={onRemove}><I.X size={11}/></span>}
    </span>
  );
}

/* ----------------- Switch ----------------- */
function Switch({ value, onChange, disabled }) {
  return (
    <button
      type="button"
      className={`switch ${value ? "on" : ""}`}
      onClick={() => !disabled && onChange(!value)}
      disabled={disabled}
      aria-pressed={value}
    />
  );
}

/* ----------------- Modal ----------------- */
function Modal({ open, title, subtitle, onClose, children, footer, wide, danger, icon }) {
  useEffect(() => {
    if (!open) return;
    const handler = (e) => { if (e.key === "Escape") onClose?.(); };
    window.addEventListener("keydown", handler);
    return () => window.removeEventListener("keydown", handler);
  }, [open, onClose]);

  if (!open) return null;
  return (
    <div className="modal-backdrop" onMouseDown={(e) => e.target === e.currentTarget && onClose?.()}>
      <div className={`modal ${wide ? "wide" : ""} ${danger ? "danger" : ""}`}>
        <div className="modal-head">
          {icon && <div className="danger-icon">{icon}</div>}
          <div style={{ flex: 1 }}>
            <div className="modal-title">{title}</div>
            {subtitle && <div className="modal-sub">{subtitle}</div>}
          </div>
          <button className="btn btn-ghost btn-icon" onClick={onClose}><I.X/></button>
        </div>
        <div className="modal-body">{children}</div>
        {footer && <div className="modal-foot">{footer}</div>}
      </div>
    </div>
  );
}

/* ----------------- Confirm helper ----------------- */
function ConfirmDialog({ open, onCancel, onConfirm, title, message, confirmText = "确认", danger = true, busy }) {
  return (
    <Modal
      open={open}
      onClose={onCancel}
      title={title}
      icon={danger ? <I.Alert size={18}/> : <I.Info size={18}/>}
      danger={danger}
      footer={
        <>
          <button className="btn" onClick={onCancel}>取消</button>
          <button className={`btn ${danger ? "btn-danger" : "btn-primary"}`} onClick={onConfirm} disabled={busy}>
            {busy ? "处理中…" : confirmText}
          </button>
        </>
      }
    >
      <div style={{ color: "var(--text-2)", fontSize: 13, lineHeight: 1.6 }}>{message}</div>
    </Modal>
  );
}

/* ----------------- Toast ----------------- */
const ToastCtx = createContext(null);
function ToastProvider({ children }) {
  const [items, setItems] = useState([]);
  const push = useCallback((msg, kind = "ok") => {
    const id = Math.random().toString(36).slice(2);
    setItems((arr) => [...arr, { id, msg, kind }]);
    setTimeout(() => setItems((arr) => arr.filter((x) => x.id !== id)), 3200);
  }, []);
  return (
    <ToastCtx.Provider value={push}>
      {children}
      <div className="toast-stack">
        {items.map((t) => (
          <div key={t.id} className={`toast ${t.kind}`}>
            <span className="ico">{t.kind === "ok" ? <I.Check size={16}/> : <I.Alert size={16}/>}</span>
            <span>{t.msg}</span>
          </div>
        ))}
      </div>
    </ToastCtx.Provider>
  );
}
const useToast = () => useContext(ToastCtx);

/* ----------------- Copy helper ----------------- */
function CopyValue({ value, mono = true, max, label, iconOnly }) {
  const toast = useToast();
  const display = max && value.length > max
    ? value.slice(0, Math.floor(max/2)) + "…" + value.slice(-Math.floor(max/2))
    : value;
  return (
    <span style={{ display: "inline-flex", alignItems: "center", gap: 6 }}>
      {!iconOnly && <span className={mono ? "mono" : ""}>{display}</span>}
      <button className="btn btn-ghost btn-icon btn-sm" title={`复制 ${label || ""}`}
        onClick={(e) => {
          e.stopPropagation();
          navigator.clipboard?.writeText(value);
          toast?.(`已复制${label ? " " + label : ""}`, "ok");
        }}>
        <I.Copy size={12}/>
      </button>
    </span>
  );
}

/* ----------------- Pagination ----------------- */
function Pagination({ total, page, pageSize, onPage }) {
  const pages = Math.max(1, Math.ceil(total / pageSize));
  const start = (page - 1) * pageSize + 1;
  const end = Math.min(total, page * pageSize);
  const list = [];
  for (let i = 1; i <= pages; i++) list.push(i);
  return (
    <div className="pagination">
      <span>显示 {total === 0 ? 0 : start}–{end} / 共 {total}</span>
      <div className="spacer"/>
      <button className="pg-btn" disabled={page === 1} onClick={() => onPage(page - 1)}><I.ChevLeft size={12}/></button>
      {list.slice(0, 5).map(p => (
        <button key={p} className={`pg-btn ${p === page ? "active" : ""}`} onClick={() => onPage(p)}>{p}</button>
      ))}
      {pages > 5 && <span style={{ color: "var(--text-3)" }}>… / {pages}</span>}
      <button className="pg-btn" disabled={page === pages} onClick={() => onPage(page + 1)}><I.ChevRight size={12}/></button>
    </div>
  );
}

/* ----------------- Card ----------------- */
function Card({ title, subtitle, actions, children, tight, style }) {
  return (
    <div className="card" style={style}>
      {(title || actions) && (
        <div className="card-head">
          <div>
            {title && <div className="card-title">{title}</div>}
            {subtitle && <div className="card-sub">{subtitle}</div>}
          </div>
          {actions && <div className="actions">{actions}</div>}
        </div>
      )}
      <div className={`card-body ${tight ? "tight" : ""}`}>{children}</div>
    </div>
  );
}

/* ----------------- Tabs ----------------- */
function Tabs({ tabs, value, onChange }) {
  return (
    <div className="tabs">
      {tabs.map((t) => (
        <button key={t.value}
          className={`tab ${value === t.value ? "active" : ""}`}
          onClick={() => onChange(t.value)}>
          {t.label}
          {t.count != null && <span className="count">{t.count}</span>}
        </button>
      ))}
    </div>
  );
}

/* ----------------- Sparkline ----------------- */
function Sparkline({ data, color = "var(--purple-500)", fill = "rgba(147,51,234,0.18)" }) {
  if (!data?.length) return null;
  const w = 100, h = 32;
  const max = Math.max(...data, 1);
  const min = Math.min(...data, 0);
  const range = max - min || 1;
  const step = w / (data.length - 1 || 1);
  const pts = data.map((v, i) => `${(i * step).toFixed(2)},${(h - ((v - min) / range) * (h - 4) - 2).toFixed(2)}`).join(" ");
  return (
    <svg className="sparkline" viewBox={`0 0 ${w} ${h}`} preserveAspectRatio="none">
      <polyline fill={fill} stroke="none" points={`0,${h} ${pts} ${w},${h}`} />
      <polyline fill="none" stroke={color} strokeWidth="1.4" points={pts}/>
    </svg>
  );
}

/* ----------------- Format helpers ----------------- */
const fmt = {
  num: (n) => (n ?? 0).toLocaleString("en-US"),
  bytes: (b) => {
    if (b == null) return "—";
    const u = ["B", "KB", "MB", "GB"];
    let i = 0, n = b;
    while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
    return `${n < 10 ? n.toFixed(2) : n.toFixed(1)} ${u[i]}`;
  },
  speed: (bps) => fmt.bytes(bps) + "/s",
  short: (s, n = 12) => !s ? (s ?? "") : s.length > n ? s.slice(0, n - 4) + "…" + s.slice(-3) : s,
  ago: (ts) => {
    const d = (Date.now() - ts) / 1000;
    if (d < 60) return `${Math.floor(d)} 秒前`;
    if (d < 3600) return `${Math.floor(d/60)} 分钟前`;
    if (d < 86400) return `${Math.floor(d/3600)} 小时前`;
    return `${Math.floor(d/86400)} 天前`;
  },
  dt: (ts) => {
    const d = new Date(ts);
    const pad = (n) => String(n).padStart(2, "0");
    return `${d.getFullYear()}-${pad(d.getMonth()+1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`;
  },
  countdown: (ts) => {
    const s = Math.max(0, Math.floor((ts - Date.now()) / 1000));
    if (s === 0) return "已过期";
    const d = Math.floor(s / 86400);
    const h = Math.floor((s % 86400) / 3600);
    const m = Math.floor((s % 3600) / 60);
    if (d > 0) return `${d}天 ${h}小时`;
    if (h > 0) return `${h}小时 ${m}分`;
    return `${m}分 ${s % 60}秒`;
  },
  uptime: (sec) => {
    const d = Math.floor(sec / 86400);
    const h = Math.floor((sec % 86400) / 3600);
    const m = Math.floor((sec % 3600) / 60);
    return `${d}d ${String(h).padStart(2,"0")}h ${String(m).padStart(2,"0")}m`;
  },
  pct: (v) => `${(v * 100).toFixed(1)}%`,
};

/* ----------------- AppState context ----------------- */
const AppCtx = createContext(null);
const useApp = () => useContext(AppCtx);

/* ----------------- Export to window ----------------- */
Object.assign(window, {
  I, Icon, StatusBadge, Tag, Switch, Modal, ConfirmDialog,
  ToastProvider, useToast, CopyValue, Pagination, Card, Tabs, Sparkline, fmt,
  AppCtx, useApp,
});
