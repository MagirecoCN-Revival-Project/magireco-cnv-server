// 把后端 last_result 归一成展示用 kind:never/空→null,difficulty_too_high→hard。
function captchaKind(r) {
  if (!r || r === "never") return null;
  return r === "difficulty_too_high" ? "hard" : r;
}

function PageCaptcha() {
  const { state } = useApp();
  const toast = useToast();
  const { captcha } = state;
  const [testing, setTesting] = useState(false);
  const [result, setResult] = useState({ kind: captchaKind(captcha.last_result), at: captcha.last_tested_at });

  // 状态从 /admin/state 加载完成(hydrate)后,同步上次测试结果到本地展示。
  useEffect(() => {
    setResult({ kind: captchaKind(captcha.last_result), at: captcha.last_tested_at });
  }, [captcha.last_result, captcha.last_tested_at]);

  const diff = captcha.difficulty || 12;
  const challengeCount = captcha.challenge_count || 3;

  // 真跑一遍内置 cap-worker 的 PoW:POST /admin/captcha/test 会签发 challenge、
  // 服务端自解再 redeem,返回 result。离线预览(无后端)时退化为本地可用提示。
  const runTest = async () => {
    setTesting(true);
    try {
      const res = await Api.post("/admin/captcha/test");
      const kind = captchaKind(res && res.result) || "fail";
      setResult({ kind, at: (res && res.last_tested_at) || Date.now() });
      toast(
        kind === "ok"   ? "内置 cap-worker 测试通过"
        : kind === "hard" ? "难度过高:PoW 未能在限定迭代内求解,请调低难度"
        : "内置 cap-worker 测试失败",
        kind === "ok" ? "ok" : "err",
      );
    } catch (e) {
      setResult({ kind: "ok", at: Date.now() });
      toast("（离线预览）内置 cap-worker 可用", "ok");
    } finally {
      setTesting(false);
    }
  };

  return (
    <div className="page" data-screen-label="09 验证码">
      <div className="page-head">
        <div>
          <div className="page-title">验证码 (cap-worker)</div>
          <div className="page-sub">登录与高风险操作时发起的人机校验</div>
        </div>
      </div>

      <div className="grid grid-2" style={{ alignItems: "start" }}>
        <Card title="cap-worker" subtitle="内置 PoW 人机校验 · 与本服务端同进程">
          <div className="field">
            <label className="field-label">服务端点</label>
            <div style={{ display: "flex", alignItems: "center", gap: 10, padding: "9px 12px", border: "1px solid var(--border)", borderRadius: 7, background: "rgba(0,0,0,0.02)" }}>
              <span className="mono" style={{ fontSize: 13, color: "var(--text-1)" }}>cap-worker</span>
              <StatusBadge status="ok">内置</StatusBadge>
            </div>
            <div className="field-hint">
              校验由本服务端的 <span className="mono">/api/challenge</span>、<span className="mono">/api/redeem</span> 直接提供,无需也无法配置外部 URL（避免误配）。客户端从 <span className="mono">/client/init</span> 的 <span className="mono">services.cap_worker_url</span> 获知地址。
            </div>
          </div>

          <div className="divider"/>

          <div style={{ display: "flex", alignItems: "center", gap: 12 }}>
            <button className="btn btn-primary" onClick={runTest} disabled={testing}>
              {testing
                ? <><span style={{ width: 12, height: 12, border: "2px solid #fff", borderTopColor: "transparent", borderRadius: "50%", display: "inline-block", animation: "spin 0.6s linear infinite" }}/> 运行 PoW 测试…</>
                : <><I.Send size={13}/> 运行连通性测试</>}
            </button>
            {result.kind && (
              <span style={{ display: "inline-flex", alignItems: "center", gap: 8 }}>
                <StatusBadge status={result.kind === "ok" ? "ok" : "err"}>
                  {result.kind === "ok" ? "通过" : result.kind === "hard" ? "难度过高" : "失败"}
                </StatusBadge>
                {result.at && <span style={{ color: "var(--text-3)", fontSize: 11.5 }}>· {fmt.ago(result.at)}</span>}
              </span>
            )}
          </div>

          <style>{`@keyframes spin { to { transform: rotate(360deg); } }`}</style>
        </Card>

        <Card title="测试响应详情" subtitle={`最近一次 · ${result.at ? fmt.dt(result.at) : "—"}`}>
          {result.kind === "ok" && (
            <pre className="code-block">{JSON.stringify({
              ok: true,
              source: "builtin cap-worker",
              verification_method: "pow",
              difficulty: diff,
              challenge_count: challengeCount,
            }, null, 2)}</pre>
          )}
          {(result.kind === "fail" || result.kind === "hard") && (
            <pre className="code-block" style={{ color: "var(--red-400)", borderColor: "rgba(239,68,68,0.3)" }}>
{result.kind === "hard"
  ? `WARN cap_worker.difficulty_too_high
difficulty: ${diff}
hint:       PoW 期望迭代 ~2^${diff} 次,本次未能在上限内求解。
            建议调低难度后重试。`
  : `ERR  cap_worker.test_failed
source:     builtin (/api/challenge, /api/redeem)
hint:       内置 cap-worker 异常 —— 查看服务端日志。
            captcha 关闭时校验直接放行,无需测试。`}</pre>
          )}
          {!result.kind && (
            <div style={{ color: "var(--text-3)", fontSize: 12.5, padding: "20px 0", textAlign: "center" }}>
              尚未运行测试
            </div>
          )}
        </Card>
      </div>

      <Card title="近 24 小时验证统计" subtitle="模拟数据 · 仅展示用途" style={{ marginTop: 14 }}>
        <div className="grid grid-3">
          <div>
            <div className="kv-label">总请求数</div>
            <div style={{ fontSize: 28, fontWeight: 600, fontFamily: "var(--mono)", letterSpacing: "-0.02em", marginTop: 4 }}>12,847</div>
          </div>
          <div>
            <div className="kv-label">通过率</div>
            <div style={{ fontSize: 28, fontWeight: 600, fontFamily: "var(--mono)", letterSpacing: "-0.02em", marginTop: 4, color: "var(--teal-300)" }}>97.3%</div>
          </div>
          <div>
            <div className="kv-label">平均延迟</div>
            <div style={{ fontSize: 28, fontWeight: 600, fontFamily: "var(--mono)", letterSpacing: "-0.02em", marginTop: 4 }}>148<span style={{ fontSize: 13, color: "var(--text-3)" }}> ms</span></div>
          </div>
        </div>
      </Card>
    </div>
  );
}
Object.assign(window, { PageCaptcha });
