function PageVersions() {
  const { state, dispatch } = useApp();
  const toast = useToast();
  const { versions } = state;

  const [draft, setDraft] = useState(versions);
  useEffect(() => { setDraft(versions); }, [versions]);

  const [newVer, setNewVer] = useState("");
  const dirty = JSON.stringify(draft) !== JSON.stringify(versions);

  const addVersion = () => {
    const v = newVer.trim();
    if (!v) return;
    if (draft.allowed.includes(v)) { toast("版本号已存在", "err"); return; }
    setDraft({ ...draft, allowed: [...draft.allowed, v] });
    setNewVer("");
  };
  const removeVersion = (v) => setDraft({ ...draft, allowed: draft.allowed.filter(x => x !== v) });

  const save = () => {
    dispatch({ type: "versions.set", value: draft });
    dispatch({ type: "audit.add", entry: { type: "version.allowed.update", target: "client_versions", details: { count: draft.allowed.length } } });
    toast("版本配置已保存,将在下次握手生效", "ok");
  };

  return (
    <div className="page" data-screen-label="03 版本管理">
      <div className="page-head">
        <div>
          <div className="page-title">版本管理</div>
          <div className="page-sub">客户端版本白名单 · JNI 伪造字段 · 更新通道 URL</div>
        </div>
        <div className="actions">
          {dirty && <span style={{ color: "var(--amber-400)", fontSize: 12 }}><I.Alert size={12}/> 有未保存的更改</span>}
          <button className="btn" disabled={!dirty} onClick={() => setDraft(versions)}>放弃</button>
          <button className="btn btn-primary" disabled={!dirty} onClick={save}><I.Check size={13}/> 保存全部</button>
        </div>
      </div>

      <div className="grid grid-2" style={{ alignItems: "start" }}>
        <Card title="允许的客户端版本" subtitle="不在白名单中的客户端将被 /init 拒绝">
          <div style={{ display: "flex", flexWrap: "wrap", gap: 6, marginBottom: 12, minHeight: 28 }}>
            {draft.allowed.length === 0 && <span style={{ color: "var(--text-3)", fontSize: 12 }}>暂无,所有客户端将被拒绝</span>}
            {draft.allowed.map(v => <Tag key={v} onRemove={() => removeVersion(v)}>{v}</Tag>)}
          </div>
          <div style={{ display: "flex", gap: 8 }}>
            <input className="input mono" placeholder="例如 1.4.7 / 1.5.0-beta"
              value={newVer}
              onChange={(e) => setNewVer(e.target.value)}
              onKeyDown={(e) => e.key === "Enter" && addVersion()} />
            <button className="btn" onClick={addVersion}><I.Plus size={13}/> 添加</button>
          </div>
          <div className="field-hint" style={{ marginTop: 10 }}>
            支持任意字符串。版本格式与客户端 <span className="mono">Application.version</span> 完全匹配。
          </div>
        </Card>

        <Card title="JNI 伪造字段" subtitle="服务端下发后,客户端在 native 层将本地值替换为以下内容">
          <div className="grid" style={{ gap: 14 }}>
            <div className="field">
              <label className="field-label">
                <I.Tag size={12}/> 伪造版本号
                <span className="badge-pill" style={{ marginLeft: "auto" }}>versionName</span>
              </label>
              <input className="input mono" value={draft.fakeVersion}
                onChange={(e) => setDraft({ ...draft, fakeVersion: e.target.value })}/>
              <div className="field-hint">通过 JNI 注入到 PackageInfo,绕过日服客户端版本检测。</div>
            </div>
            <div className="field">
              <label className="field-label">
                <I.Package size={12}/> 伪造应用名
                <span className="badge-pill" style={{ marginLeft: "auto" }}>applicationLabel</span>
              </label>
              <input className="input" value={draft.fakeAppName}
                onChange={(e) => setDraft({ ...draft, fakeAppName: e.target.value })}/>
              <div className="field-hint">影响系统设置中显示的应用名,以及部分内嵌错误页。</div>
            </div>
          </div>
        </Card>
      </div>

      <Card title="更新通道 URL" subtitle="客户端启动握手时拉取的软更新提示与 APK 下载地址" style={{ marginTop: 14 }}>
        <div className="field" style={{ marginBottom: 14 }}>
          <label className="field-label">
            <I.Tag size={12}/> 最新版本号（软提示）
            <span className="badge-pill" style={{ marginLeft: "auto" }}>latest_version</span>
          </label>
          <input className="input mono" placeholder="例如 4.1.0"
            value={draft.latest_version || ""}
            onChange={(e) => setDraft({ ...draft, latest_version: e.target.value })}/>
          <div className="field-hint">
            下发后客户端按渠道比较：正式版仅在 major.minor 升高时提示；内测版任意版本位升高即提示。留空则不下发软提示。
          </div>
        </div>
        <div className="grid grid-2">
          <div className="field">
            <label className="field-label">
              <I.Wifi size={12}/> 正式通道 APK
              <StatusBadge status="ok">production</StatusBadge>
            </label>
            <input className="input mono" value={draft.update_url_normal || draft.updateUrl || ""}
              onChange={(e) => setDraft({ ...draft, update_url_normal: e.target.value })}/>
            <div className="field-hint">正式版用户更新时下载此 APK。</div>
          </div>
          <div className="field">
            <label className="field-label">
              <I.Zap size={12}/> 内测通道 APK
              <StatusBadge status="info">internal</StatusBadge>
            </label>
            <input className="input mono" value={draft.update_url_internal_test || draft.testUpdateUrl || ""}
              onChange={(e) => setDraft({ ...draft, update_url_internal_test: e.target.value })}/>
            <div className="field-hint">内测版用户更新时下载此 APK。</div>
          </div>
        </div>
        <div className="grid grid-2" style={{ marginTop: 14 }}>
          <div className="field">
            <label className="field-label">
              <I.Lock size={12}/> 正式通道 APK SHA-256
              <StatusBadge status="ok">production</StatusBadge>
            </label>
            <input className="input mono" placeholder="64 位小写十六进制"
              value={draft.update_apk_sha256 || ""}
              onChange={(e) => setDraft({ ...draft, update_apk_sha256: e.target.value })}/>
            <div className="field-hint">正式版渠道下载后的 APK 完整性校验哈希。</div>
          </div>
          <div className="field">
            <label className="field-label">
              <I.Lock size={12}/> 内测通道 APK SHA-256
              <StatusBadge status="info">internal</StatusBadge>
            </label>
            <input className="input mono" placeholder="留空则与正式通道共用同一 sha256"
              value={draft.update_apk_sha256_internal_test || ""}
              onChange={(e) => setDraft({ ...draft, update_apk_sha256_internal_test: e.target.value })}/>
            <div className="field-hint">内测 APK 与正式包文件不同时单独填写,服务端按请求渠道下发对应哈希。</div>
          </div>
        </div>
      </Card>

      <div style={{ marginTop: 18, display: "flex", gap: 10, alignItems: "center", color: "var(--text-3)", fontSize: 12 }}>
        <I.Info size={13}/> 所有改动会在下次客户端调用 <span className="mono" style={{ color: "var(--text-2)" }}>/api/v2/init</span> 时生效,无需重启服务。
      </div>
    </div>
  );
}
Object.assign(window, { PageVersions });
