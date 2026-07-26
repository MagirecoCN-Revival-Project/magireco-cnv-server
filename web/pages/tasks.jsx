/* "定时任务" page — central place to tune all server-side cron intervals (seconds). */

const TASK_DEFS = [
  {
    key: "heartbeatTimeoutSec",
    label: "客户端心跳超时",
    icon: "Heart", color: "var(--teal-600)", bg: "var(--teal-50)", border: "rgba(20,184,166,0.3)",
    desc: "客户端心跳静默多久后视为离线,从「正在下载」列表中移除",
    suggestion: "建议 10–30 秒。过短会误判网络抖动,过长会让封禁等操作延迟生效。",
    min: 5, max: 600,
  },
  {
    key: "banAutoExpireScanSec",
    label: "封禁过期扫描",
    icon: "Ban", color: "var(--red-500)", bg: "var(--red-50)", border: "rgba(220,38,38,0.3)",
    desc: "扫描 active_bans 表,将过期记录搬到 history",
    suggestion: "建议 30–120 秒。",
    min: 10, max: 3600,
  },
  {
    key: "banSweepSec",
    label: "封禁规则扫描",
    icon: "Shield", color: "var(--accent-700)", bg: "var(--accent-50)", border: "var(--accent-200)",
    desc: "对在线设备运行自动封禁规则(多账号 / 高频心跳 / 客户端篡改等)",
    suggestion: "通常 1 小时一次即可,过短可能引起误判风暴。",
    min: 60, max: 86400,
  },
  {
    key: "metricsFlushSec",
    label: "指标 Flush",
    icon: "Dashboard", color: "var(--blue-400)", bg: "#eff6ff", border: "rgba(37,99,235,0.3)",
    desc: "把内存中的 QPS / 延迟 / 错误率指标写入 metrics 后端",
    suggestion: "Prometheus 抓取间隔的一半左右最合适。",
    min: 5, max: 600,
  },
  {
    key: "sessionGcSec",
    label: "会话 GC",
    icon: "User", color: "var(--accent-700)", bg: "var(--accent-50)", border: "var(--accent-200)",
    desc: "清理已过期但未注销的 session_id 与对应的临时 token 缓存",
    suggestion: "15–60 分钟。值越大,无效会话占用内存越多。",
    min: 60, max: 86400,
  },
  {
    key: "capWorkerHealthSec",
    label: "cap-worker 健康检查",
    icon: "Shield", color: "var(--amber-500)", bg: "var(--amber-50)", border: "rgba(217,119,6,0.3)",
    desc: "定期对 cap-worker 端点发起 ping,失败超过阈值后切换到本地降级算法",
    suggestion: "1–5 分钟。",
    min: 30, max: 3600,
  },
];

function PageTasks() {
  const { state, dispatch } = useApp();
  const toast = useToast();
  const { tasks, pipeline, autoPackage, unverifiedPurge } = state;

  const setTask = (key, val) => {
    dispatch({ type: "tasks.set", patch: { [key]: val } });
    dispatch({ type: "audit.add", entry: { type: "task.interval.update", target: key, details: { to: val } } });
  };

  return (
    <div className="page" data-screen-label="03 定时任务">
      <div className="page-head">
        <div>
          <div className="page-title">定时任务</div>
          <div className="page-sub">主节点上的所有后台定时器 · 单位:秒 · 修改后立即生效</div>
        </div>
      </div>

      {/* External-task summary row (interval source of truth lives in those pages) */}
      <Card title="跨页定时器" subtitle="这些定时器的源头在其它页面 · 此处只读" style={{ marginBottom: 14 }}>
        <div className="grid" style={{ gridTemplateColumns: "repeat(2, minmax(0, 1fr))", gap: 12 }}>
          <CrossTaskItem
            icon="Cloud" label="GitHub Release 轮询"
            value={pipeline.poll_interval_sec}
            note={pipeline.auto_sync ? "已启用" : "已暂停"}
            link="资源管理 → 资源同步管道"
          />
          <CrossTaskItem
            icon="Package" label="离线包自动打包"
            value={autoPackage.intervalSec}
            note={autoPackage.enabled ? `下次 ${fmt.countdown(autoPackage.lastRunAt + autoPackage.intervalSec * 1000)}` : "已暂停"}
            link="资源管理 → 离线整包"
          />
        </div>
      </Card>

      <Card title="后台定时器" subtitle={`${TASK_DEFS.length} 个任务 · 全部以秒为单位`} tight>
        <div style={{ padding: 4 }}>
          {TASK_DEFS.map(t => (
            <TaskRow key={t.key}
              def={t}
              value={tasks[t.key]}
              onChange={(v) => setTask(t.key, v)}
            />
          ))}
        </div>
      </Card>

      <UnverifiedPurgeCard cfg={unverifiedPurge} dispatch={dispatch} />

      <div style={{ marginTop: 14, display: "flex", gap: 10, alignItems: "flex-start", color: "var(--text-3)", fontSize: 12 }}>
        <I.Info size={13} style={{ marginTop: 2 }}/>
        <span>
          所有间隔值在主节点的下一个 tick(默认 1 秒)生效。极短的间隔(&lt; 10s)请谨慎配置,可能造成数据库与下游服务被打爆。
          每次改动都会写入审计日志,可在「审计日志」页过滤 <span className="mono" style={{ color: "var(--text-2)" }}>task.interval.update</span> 查看历史。
        </span>
      </div>
    </div>
  );
}

function TaskRow({ def, value, onChange }) {
  const Icon = I[def.icon];
  const [draft, setDraft] = useState(String(value));
  useEffect(() => { setDraft(String(value)); }, [value]);

  const commit = () => {
    let n = parseInt(draft, 10);
    if (isNaN(n)) { setDraft(String(value)); return; }
    n = Math.min(def.max, Math.max(def.min, n));
    setDraft(String(n));
    if (n !== value) onChange(n);
  };

  return (
    <div style={{
      display: "grid",
      gridTemplateColumns: "auto 1fr auto",
      gap: 14,
      alignItems: "center",
      padding: "14px 14px",
      borderBottom: "1px solid var(--border-1)",
    }}>
      <span style={{
        width: 32, height: 32, borderRadius: 8,
        background: def.bg, border: `1px solid ${def.border}`,
        color: def.color,
        display: "grid", placeItems: "center",
        flexShrink: 0,
      }}>
        <Icon size={15}/>
      </span>
      <div style={{ minWidth: 0 }}>
        <div style={{ display: "flex", alignItems: "center", gap: 8, flexWrap: "wrap" }}>
          <span style={{ fontWeight: 600, fontSize: 13.5 }}>{def.label}</span>
          <span className="mono badge-pill" style={{ fontSize: 10.5 }}>{def.key}</span>
        </div>
        <div style={{ color: "var(--text-3)", fontSize: 12, marginTop: 3 }}>{def.desc}</div>
        <div style={{ color: "var(--text-disabled)", fontSize: 11, marginTop: 4, fontStyle: "italic" }}>
          {def.suggestion} · 允许范围 {def.min}–{def.max} 秒
        </div>
      </div>
      <div style={{ display: "flex", alignItems: "center", gap: 6 }}>
        <input
          type="number"
          className="input mono"
          style={{ width: 110, textAlign: "right" }}
          value={draft}
          min={def.min}
          max={def.max}
          onChange={(e) => setDraft(e.target.value)}
          onBlur={commit}
          onKeyDown={(e) => e.key === "Enter" && e.currentTarget.blur()}
        />
        <span style={{ color: "var(--text-3)", fontSize: 12, fontFamily: "var(--mono)" }}>秒</span>
      </div>
    </div>
  );
}

function CrossTaskItem({ icon, label, value, note, link }) {
  const Icon = I[icon];
  return (
    <div style={{
      padding: 14,
      background: "var(--bg-2)",
      border: "1px solid var(--border-1)",
      borderRadius: 8,
    }}>
      <div style={{ display: "flex", alignItems: "center", gap: 8, marginBottom: 8 }}>
        <Icon size={14} style={{ color: "var(--accent-700)" }}/>
        <span style={{ fontWeight: 600, fontSize: 12.5 }}>{label}</span>
      </div>
      <div className="mono" style={{ fontSize: 18, fontWeight: 600 }}>{fmt.num(value)} <span style={{ fontSize: 11, fontWeight: 400, color: "var(--text-3)" }}>秒</span></div>
      <div style={{ color: "var(--text-3)", fontSize: 11, marginTop: 4 }}>{note}</div>
      <div style={{ color: "var(--text-disabled)", fontSize: 10.5, marginTop: 4, fontFamily: "var(--mono)" }}>{link}</div>
    </div>
  );
}

function UnverifiedPurgeCard({ cfg, dispatch }) {
  const [days, setDays] = useState(String(cfg.retainDays ?? 30));
  const [scanSec, setScanSec] = useState(String(cfg.scanSec ?? 86400));
  useEffect(() => { setDays(String(cfg.retainDays ?? 30)); }, [cfg.retainDays]);
  useEffect(() => { setScanSec(String(cfg.scanSec ?? 86400)); }, [cfg.scanSec]);

  const set = (patch) => dispatch({ type: "unverifiedPurge.set", patch });

  const commitDays = () => {
    let n = parseInt(days, 10);
    if (isNaN(n) || n < 1) n = 1;
    if (n > 3650) n = 3650;
    setDays(String(n));
    if (n !== cfg.retainDays) set({ retainDays: n });
  };

  const commitScan = () => {
    let n = parseInt(scanSec, 10);
    if (isNaN(n) || n < 3600) n = 3600;
    if (n > 604800) n = 604800;
    setScanSec(String(n));
    if (n !== cfg.scanSec) set({ scanSec: n });
  };

  return (
    <Card
      title="未验证账号自动清理"
      subtitle="自动删除注册后超过指定天数仍未完成邮箱验证的账号"
      style={{ marginTop: 14 }}
    >
      <div style={{ padding: "14px 16px", display: "flex", flexDirection: "column", gap: 16 }}>

        {/* 启用开关 */}
        <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between" }}>
          <div>
            <div style={{ fontWeight: 600, fontSize: 13.5 }}>启用自动清理</div>
            <div style={{ color: "var(--text-3)", fontSize: 12, marginTop: 2 }}>
              开启后,后台定时任务将周期性删除超期未验证的账号
            </div>
          </div>
          <label style={{ display: "flex", alignItems: "center", gap: 6, cursor: "pointer", userSelect: "none" }}>
            <input
              type="checkbox"
              checked={!!cfg.enabled}
              onChange={(e) => set({ enabled: e.target.checked })}
              style={{ width: 16, height: 16, accentColor: "var(--accent-600)", cursor: "pointer" }}
            />
            <span style={{ fontSize: 12.5, color: cfg.enabled ? "var(--teal-600)" : "var(--text-3)", fontWeight: 500 }}>
              {cfg.enabled ? "已启用" : "已关闭"}
            </span>
          </label>
        </div>

        <div style={{ borderTop: "1px solid var(--border-1)" }}/>

        {/* 保留天数 */}
        <div style={{ display: "grid", gridTemplateColumns: "1fr auto", gap: 14, alignItems: "center" }}>
          <div>
            <div style={{ fontWeight: 600, fontSize: 13 }}>保留天数</div>
            <div style={{ color: "var(--text-3)", fontSize: 12, marginTop: 2 }}>
              注册后超过此天数仍未验证邮箱的账号将被删除 · 允许范围 1–3650 天
            </div>
          </div>
          <div style={{ display: "flex", alignItems: "center", gap: 6 }}>
            <input
              type="number" className="input mono"
              style={{ width: 90, textAlign: "right" }}
              value={days} min={1} max={3650}
              disabled={!cfg.enabled}
              onChange={(e) => setDays(e.target.value)}
              onBlur={commitDays}
              onKeyDown={(e) => e.key === "Enter" && e.currentTarget.blur()}
            />
            <span style={{ color: "var(--text-3)", fontSize: 12, fontFamily: "var(--mono)" }}>天</span>
          </div>
        </div>

        {/* 扫描间隔 */}
        <div style={{ display: "grid", gridTemplateColumns: "1fr auto", gap: 14, alignItems: "center" }}>
          <div>
            <div style={{ fontWeight: 600, fontSize: 13 }}>扫描间隔</div>
            <div style={{ color: "var(--text-3)", fontSize: 12, marginTop: 2 }}>
              每隔多少秒执行一次清理检查 · 允许范围 3600–604800 秒(1 小时–7 天)
            </div>
          </div>
          <div style={{ display: "flex", alignItems: "center", gap: 6 }}>
            <input
              type="number" className="input mono"
              style={{ width: 110, textAlign: "right" }}
              value={scanSec} min={3600} max={604800}
              disabled={!cfg.enabled}
              onChange={(e) => setScanSec(e.target.value)}
              onBlur={commitScan}
              onKeyDown={(e) => e.key === "Enter" && e.currentTarget.blur()}
            />
            <span style={{ color: "var(--text-3)", fontSize: 12, fontFamily: "var(--mono)" }}>秒</span>
          </div>
        </div>

        {cfg.enabled && (
          <div style={{
            padding: "8px 12px", borderRadius: 6,
            background: "var(--amber-50)", border: "1px solid rgba(217,119,6,0.3)",
            color: "var(--amber-600)", fontSize: 12,
            display: "flex", alignItems: "flex-start", gap: 8,
          }}>
            <I.Alert size={13} style={{ marginTop: 2, flexShrink: 0 }}/>
            <span>
              删除操作<strong>不可撤销</strong>。账号相关的云存档、设备绑定、审计日志将被一并清除。
              建议通过邮件系统在验证期届满前发送提醒。
            </span>
          </div>
        )}
      </div>
    </Card>
  );
}

Object.assign(window, { PageTasks });
