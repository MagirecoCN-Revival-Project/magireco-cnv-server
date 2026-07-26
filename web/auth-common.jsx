// 通用认证组件:人机验证框。
// 与服务端 /api/challenge /api/redeem 对接,求解在浏览器内完成;
// 通过 onVerify 回调把 (status, capToken) 反馈给宿主表单。
// 面向终端用户:只展示「未验证 / 验证中 / 已通过」三态 + 中性进度,
// 不暴露算法、版本、品牌等实现细节(技术细节仅在 /admin 后台可见)。

function CapWorkerWidget({ status, onVerify }) {
  const [progress, setProgress] = React.useState({ i: 0, c: 0 });

  const verify = async () => {
    if (status !== "idle") return;
    onVerify("verifying", "");
    try {
      const tok = await solveCapToken((i, c) => setProgress({ i, c }));
      onVerify("verified", tok);
    } catch (err) {
      onVerify("idle", "");
    }
  };

  // 验证中给一个中性的进度百分比作反馈;不泄露挑战数/算法等内部信息。
  const pct = progress.c > 0 ? Math.min(99, Math.round((progress.i / progress.c) * 100)) : 0;
  const sub = status === "verifying" && progress.c > 0 ? `${pct}%` : "";

  return (
    <div className={`cap-widget ${status}`} onClick={verify}>
      <div className="cap-checkbox">
        {status === "verifying" && <span className="spinner"/>}
        {status === "verified" && <I.Check size={16} stroke="#fff" sw={3}/>}
      </div>
      <div className="cap-widget-text">
        {status === "idle" && "我是人类"}
        {status === "verifying" && "正在验证…"}
        {status === "verified" && "验证通过"}
        {sub && <div className="cap-widget-text-sub">{sub}</div>}
      </div>
    </div>
  );
}

Object.assign(window, { CapWorkerWidget });
