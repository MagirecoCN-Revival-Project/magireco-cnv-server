function HotUpdateCard({ title, icon, bundle, onPublish, kind }) {
  return (
    <div className="card">
      <div className="card-head">
        <div style={{ display: "flex", alignItems: "center", gap: 10 }}>
          <span style={{
            width: 28, height: 28, borderRadius: 7,
            background: kind === "js" ? "rgba(107,33,168,0.18)" : "rgba(13,148,136,0.15)",
            border: `1px solid ${kind === "js" ? "rgba(192,132,252,0.35)" : "rgba(94,234,212,0.3)"}`,
            color: kind === "js" ? "var(--purple-300)" : "var(--teal-300)",
            display: "grid", placeItems: "center",
          }}>
            {icon}
          </span>
          <div>
            <div className="card-title">{title}</div>
            <div className="card-sub">{kind === "js" ? "客户端 JavaScript 逻辑层" : "剧本与资源配置 bundle"}</div>
          </div>
        </div>
        <div className="actions">
          <span className="badge-pill badge-info">v{bundle.version}</span>
        </div>
      </div>
      <div className="card-body">
        <div style={{
          display: "flex", alignItems: "baseline", justifyContent: "space-between",
          padding: "10px 0 18px",
          borderBottom: "1px solid var(--border-1)",
          marginBottom: 14,
        }}>
          <div>
            <div className="kv-label">当前版本</div>
            <div style={{ fontSize: 38, fontWeight: 700, fontFamily: "var(--mono)", letterSpacing: "-0.04em", marginTop: 4 }}>
              v{bundle.version}
            </div>
          </div>
          <div style={{ textAlign: "right" }}>
            <div className="kv-label">发布时间</div>
            <div style={{ fontSize: 13, marginTop: 6 }}>{fmt.ago(bundle.publishedAt)}</div>
            <div style={{ color: "var(--text-3)", fontSize: 11.5, fontFamily: "var(--mono)", marginTop: 2 }}>{fmt.dt(bundle.publishedAt)}</div>
          </div>
        </div>

        <div style={{ display: "grid", gap: 12 }}>
          <div className="kvpair">
            <div className="kv-label">文件大小</div>
            <div className="kv-value mono">{fmt.bytes(bundle.size)} <span style={{ color: "var(--text-3)" }}>({fmt.num(bundle.size)} bytes)</span></div>
          </div>
          <div className="kvpair">
            <div className="kv-label">下载 URL</div>
            <div className="kv-value mono" style={{ fontSize: 12, display: "flex", alignItems: "center", gap: 6 }}>
              <span style={{ color: "var(--purple-300)", wordBreak: "break-all", flex: 1 }}>{bundle.url}</span>
              <CopyValue iconOnly value={bundle.url} mono={false} label="URL"/>
            </div>
          </div>
          <div className="kvpair">
            <div className="kv-label">SHA-256</div>
            <div className="kv-value mono" style={{ fontSize: 11.5, wordBreak: "break-all" }}>
              {bundle.sha256} <CopyValue iconOnly value={bundle.sha256} mono={false} label="哈希"/>
            </div>
          </div>
        </div>
      </div>
      <div style={{ padding: "12px 16px", borderTop: "1px solid var(--border-1)", display: "flex", gap: 8 }}>
        <button className="btn btn-primary" onClick={onPublish}><I.Send size={13}/> 发布 v{bundle.version + 1}</button>
        <button className="btn"><I.History size={13}/> 历史版本</button>
      </div>
    </div>
  );
}

function PublishDrawer({ open, onClose, current, kind, onPublished }) {
  const [mode, setMode] = useState("url");   // "url" | "upload"
  const [url, setUrl] = useState("");
  const [file, setFile] = useState(null);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState("");
  const toast = useToast();
  const apiKind = kind === "js" ? "js" : "scenario";

  useEffect(() => {
    if (open) { setMode("url"); setUrl(""); setFile(null); setError(""); setSubmitting(false); }
  }, [open]);
  if (!open) return null;

  const urlOk = /^https?:\/\/.+/i.test(url.trim());
  const canSubmit = !submitting && (mode === "url" ? urlOk : !!file);

  const submit = async () => {
    if (!canSubmit) return;
    setSubmitting(true);
    setError("");
    try {
      const bundle = mode === "url"
        ? await publishHotBundle(apiKind, url.trim())
        : await uploadHotBundle(apiKind, file);
      onPublished(bundle);
    } catch (e) {
      setError(e.message || "发布失败");
      setSubmitting(false);
    }
  };

  return (
    <>
      <div className="drawer-backdrop" onClick={submitting ? undefined : onClose}/>
      <div className="drawer">
        <div style={{ padding: "16px 20px", borderBottom: "1px solid var(--border-1)", display: "flex", alignItems: "flex-start", gap: 12 }}>
          <div style={{ flex: 1 }}>
            <div style={{ fontSize: 14, fontWeight: 600 }}>发布 {kind === "js" ? "JS Bundle" : "Scenario Bundle"} v{current.version + 1}</div>
            <div style={{ color: "var(--text-3)", fontSize: 12, marginTop: 4 }}>
              服务端校验为合法 zip 后自托管分发,SHA-256 与大小由服务端计算。
            </div>
          </div>
          <button className="btn btn-ghost btn-icon" onClick={onClose} disabled={submitting}><I.X/></button>
        </div>
        <div style={{ padding: 20, display: "flex", flexDirection: "column", gap: 14, flex: 1, overflow: "auto" }}>
          <div className="kvpair">
            <div className="kv-label">当前版本</div>
            <div className="kv-value mono">v{current.version}</div>
          </div>
          <div className="kvpair">
            <div className="kv-label">新版本号</div>
            <div className="kv-value mono" style={{ color: "var(--purple-300)", fontSize: 18, fontWeight: 600 }}>v{current.version + 1}</div>
          </div>
          <div className="divider" style={{ margin: "4px 0" }}/>

          {/* 来源切换:直链 / 上传文件 */}
          <div style={{ display: "flex", gap: 6, background: "var(--bg-2)", border: "1px solid var(--border-1)", borderRadius: 8, padding: 4 }}>
            <button
              className={`btn ${mode === "url" ? "btn-primary" : "btn-ghost"}`}
              style={{ flex: 1, justifyContent: "center" }}
              onClick={() => setMode("url")} disabled={submitting}>
              <I.Cloud size={13}/> 填写直链
            </button>
            <button
              className={`btn ${mode === "upload" ? "btn-primary" : "btn-ghost"}`}
              style={{ flex: 1, justifyContent: "center" }}
              onClick={() => setMode("upload")} disabled={submitting}>
              <I.Package size={13}/> 上传文件
            </button>
          </div>

          {mode === "url" ? (
            <div className="field">
              <label className="field-label">下载直链 (.zip)</label>
              <input className="input mono" autoFocus
                placeholder={apiKind === "js" ? "https://example.cn/js/bundle.zip" : "https://example.cn/scn/scenario.zip"}
                value={url} onChange={(e) => setUrl(e.target.value)} disabled={submitting}/>
              <div className="field-hint" style={{ color: url && !urlOk ? "var(--red-400)" : "var(--text-3)" }}>
                {url && !urlOk ? "需为 http(s):// 直链" : "服务端将下载该文件、校验为合法 zip 后落盘自托管"}
              </div>
            </div>
          ) : (
            <div className="field">
              <label className="field-label">选择热更新包 (.zip)</label>
              <input className="input" type="file" accept=".zip,application/zip"
                onChange={(e) => setFile(e.target.files && e.target.files[0])} disabled={submitting}/>
              <div className="field-hint">
                {file ? <span className="mono">{file.name} · {fmt.bytes(file.size)}</span> : "服务端仅校验为合法 zip 后落盘自托管,不离开服务器再分发"}
              </div>
            </div>
          )}

          {error && (
            <div style={{
              padding: "8px 12px", borderRadius: 6,
              background: "var(--red-50)", border: "1px solid rgba(220,38,38,0.3)",
              color: "var(--red-500)", fontSize: 12.5, display: "flex", alignItems: "center", gap: 8,
            }}>
              <I.Alert size={13}/>{error}
            </div>
          )}

          <div style={{
            padding: 12, borderRadius: 8,
            background: "rgba(217,119,6,0.08)", border: "1px solid rgba(251,191,36,0.25)",
            color: "var(--amber-400)", fontSize: 12, display: "flex", gap: 10,
          }}>
            <I.Alert size={14}/>
            <div>发布后版本号自增且不可撤销,如需回滚请重新发布旧版本的 bundle。{mode === "url" ? "下载大包可能耗时数十秒,请耐心等待。" : ""}</div>
          </div>
        </div>
        <div style={{ padding: 16, borderTop: "1px solid var(--border-1)", display: "flex", gap: 8, justifyContent: "flex-end" }}>
          <button className="btn" onClick={onClose} disabled={submitting}>取消</button>
          <button className="btn btn-primary" onClick={submit} disabled={!canSubmit}>
            {submitting
              ? <><span style={{ width: 12, height: 12, border: "2px solid #fff", borderTopColor: "transparent", borderRadius: "50%", display: "inline-block", animation: "spin 0.6s linear infinite" }}/> {mode === "url" ? "下载校验中…" : "上传校验中…"}</>
              : <><I.Send size={13}/> 确认发布</>}
          </button>
        </div>
      </div>
    </>
  );
}

function PageHotUpdate() {
  const { state, dispatch } = useApp();
  const toast = useToast();
  const { jsBundle, scenarioBundle } = state;
  const [drawer, setDrawer] = useState(null); // 'js' | 'scn' | null

  // 服务端发布/上传成功后回调:bundle 已是服务端算好的 {version,sha256,size,url,publishedAt}。
  // 用 *.loaded 只更新本地展示(服务端已写库 + 审计,不再重复触发副作用请求)。
  const onPublished = (kind) => (bundle) => {
    const actor = (state.currentAdmin && state.currentAdmin.username) || "admin";
    if (kind === "js") {
      dispatch({ type: "jsBundle.loaded", value: bundle });
      dispatch({ type: "events.push", entry: { kind: "publish", text: `热更新 JS Bundle v${bundle.version} 推送`, actor, icon: "Zap", color: "purple" } });
    } else {
      dispatch({ type: "scenarioBundle.loaded", value: bundle });
      dispatch({ type: "events.push", entry: { kind: "publish", text: `剧本 Bundle v${bundle.version} 推送`, actor, icon: "Zap", color: "purple" } });
    }
    setDrawer(null);
    toast(`${kind === "js" ? "JS" : "剧本"} Bundle v${bundle.version} 发布成功`, "ok");
  };

  return (
    <div className="page" data-screen-label="05 热更新">
      <div className="page-head">
        <div>
          <div className="page-title">热更新</div>
          <div className="page-sub">JS 逻辑层与剧本资源的版本推送</div>
        </div>
      </div>

      <div className="grid grid-2">
        <HotUpdateCard
          title="JS Bundle"
          icon={<I.Zap size={14}/>}
          bundle={jsBundle}
          kind="js"
          onPublish={() => setDrawer("js")}
        />
        <HotUpdateCard
          title="Scenario Bundle"
          icon={<I.Package size={14}/>}
          bundle={scenarioBundle}
          kind="scn"
          onPublish={() => setDrawer("scn")}
        />
      </div>

      <PublishDrawer
        open={drawer === "js"}
        kind="js"
        current={jsBundle}
        onClose={() => setDrawer(null)}
        onPublished={onPublished("js")}
      />
      <PublishDrawer
        open={drawer === "scn"}
        kind="scn"
        current={scenarioBundle}
        onClose={() => setDrawer(null)}
        onPublished={onPublished("scn")}
      />
    </div>
  );
}
Object.assign(window, { PageHotUpdate });
