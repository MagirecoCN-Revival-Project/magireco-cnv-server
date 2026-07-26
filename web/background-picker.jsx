// Background picker: presets + custom upload.
// Extracts dominant accent hue from uploaded images and applies it as --accent-h.

const { useState: useStateBG, useEffect: useEffectBG, useRef: useRefBG } = React;

/* ---------- Presets ---------- */
// Each preset specifies a CSS background, the accent hue (OKLCH approximation
// matching its dominant color), and a chroma. Hues chosen by eye to feel
// cohesive with the gradient.
const BG_PRESETS = [
  {
    id: "lilac",
    name: "Lilac Dawn",
    css: "linear-gradient(135deg, #efe6ff 0%, #fde9f3 45%, #fff7e6 100%)",
    h: 286, c: 0.16,
  },
  {
    id: "ocean",
    name: "Ocean Mist",
    css: "linear-gradient(135deg, #e2f0ff 0%, #d8f7f0 55%, #f0fbfb 100%)",
    h: 212, c: 0.14,
  },
  {
    id: "sunset",
    name: "Sunset Peach",
    css: "linear-gradient(135deg, #ffe9d6 0%, #ffd6e4 60%, #fff5e1 100%)",
    h: 28,  c: 0.16,
  },
  {
    id: "forest",
    name: "Forest Sage",
    css: "linear-gradient(135deg, #e3f3e0 0%, #ecf3df 50%, #f6f3e1 100%)",
    h: 145, c: 0.13,
  },
  {
    id: "magia",
    name: "Magia Aurora",
    css: "linear-gradient(135deg, #f3e5ff 0%, #e1ecff 45%, #ffe9f6 100%)",
    h: 296, c: 0.18,
  },
  {
    id: "night-mauve",
    name: "Mauve Field",
    css: "linear-gradient(180deg, #ece4f5 0%, #f6eef5 50%, #fff5ed 100%)",
    h: 320, c: 0.14,
  },
];

/* ---------- Helpers ---------- */
function rgbToHsl(r, g, b) {
  r /= 255; g /= 255; b /= 255;
  const max = Math.max(r, g, b), min = Math.min(r, g, b);
  const l = (max + min) / 2;
  let h = 0, s = 0;
  if (max !== min) {
    const d = max - min;
    s = l > 0.5 ? d / (2 - max - min) : d / (max + min);
    if (max === r)      h = (g - b) / d + (g < b ? 6 : 0);
    else if (max === g) h = (b - r) / d + 2;
    else                h = (r - g) / d + 4;
    h *= 60;
  }
  return [h, s, l];
}

/** Pick a "good" accent color from an image: prefer saturated mid-light pixels. */
async function extractAccentFromImage(src) {
  return new Promise((resolve, reject) => {
    const img = new Image();
    img.crossOrigin = "anonymous";
    img.onload = () => {
      try {
        const W = 80;
        const H = Math.max(40, Math.round(W * img.height / img.width));
        const c = document.createElement("canvas");
        c.width = W; c.height = H;
        const ctx = c.getContext("2d");
        ctx.drawImage(img, 0, 0, W, H);
        const data = ctx.getImageData(0, 0, W, H).data;

        const buckets = {};
        for (let i = 0; i < data.length; i += 4) {
          const r = data[i], g = data[i + 1], b = data[i + 2], a = data[i + 3];
          if (a < 100) continue;
          const [h, s, l] = rgbToHsl(r, g, b);
          if (l < 0.18 || l > 0.92) continue;
          if (s < 0.15) continue;
          const key = Math.round(h / 12); // 30 hue buckets
          if (!buckets[key]) buckets[key] = { count: 0, hSum: 0, sSum: 0, lSum: 0 };
          const b0 = buckets[key];
          b0.count++;
          b0.hSum += h;
          b0.sSum += s;
          b0.lSum += l;
        }
        let best = null;
        for (const k in buckets) {
          const b0 = buckets[k];
          // weight by both frequency and saturation
          const score = b0.count * (0.4 + (b0.sSum / b0.count) * 1.2);
          if (!best || score > best.score) best = { ...b0, score };
        }
        if (!best) return resolve({ h: 286, c: 0.16 });
        const avgH = best.hSum / best.count;
        const avgS = best.sSum / best.count;
        // Map HSL hue → OKLCH hue (close-enough approximation, then bias)
        const oklchH = (avgH + 12) % 360;
        // chroma a function of HSL saturation
        const chroma = Math.min(0.22, Math.max(0.08, 0.10 + avgS * 0.18));
        resolve({ h: oklchH, c: chroma });
      } catch (e) {
        reject(e);
      }
    };
    img.onerror = (e) => reject(new Error("image load failed"));
    img.src = src;
  });
}

/* ---------- Apply / persist ---------- */
function applyTheme({ bg, h, c, bgImageUrl }) {
  const root = document.documentElement;
  if (h != null) root.style.setProperty("--accent-h", String(h));
  if (c != null) root.style.setProperty("--accent-c", String(c));
  if (bg) {
    document.body.style.setProperty("--page-bg", bg);
    document.body.setAttribute("data-has-bg", bgImageUrl ? "1" : "0");
  } else {
    document.body.style.removeProperty("--page-bg");
    document.body.setAttribute("data-has-bg", "0");
  }
}

/* ---------- Background button + popover ---------- */
function BackgroundPicker() {
  const [open, setOpen] = useStateBG(false);
  const [active, setActive] = useStateBG(BG_PRESETS[0].id);
  const [extracting, setExtracting] = useStateBG(false);
  const [accentHue, setAccentHue] = useStateBG(BG_PRESETS[0].h);
  const fileRef = useRefBG(null);
  const popRef = useRefBG(null);
  const toast = useToast();

  // initial apply
  useEffectBG(() => {
    const p = BG_PRESETS[0];
    applyTheme({ bg: p.css, h: p.h, c: p.c });
  }, []);

  // outside click
  useEffectBG(() => {
    if (!open) return;
    const handler = (e) => {
      if (popRef.current && !popRef.current.contains(e.target)) setOpen(false);
    };
    setTimeout(() => document.addEventListener("mousedown", handler), 0);
    return () => document.removeEventListener("mousedown", handler);
  }, [open]);

  const pickPreset = (p) => {
    setActive(p.id);
    setAccentHue(p.h);
    applyTheme({ bg: p.css, h: p.h, c: p.c });
  };

  const pickNone = () => {
    setActive("none");
    setAccentHue(286);
    applyTheme({ bg: "linear-gradient(180deg, #f4f3f7 0%, #eeeaf3 100%)", h: 286, c: 0.16 });
  };

  const onUpload = async (e) => {
    const file = e.target.files?.[0];
    if (!file) return;
    if (!/^image\//.test(file.type)) { toast("仅支持图片文件", "err"); return; }

    setExtracting(true);
    const reader = new FileReader();
    reader.onload = async () => {
      const dataUrl = reader.result;
      try {
        const { h, c } = await extractAccentFromImage(dataUrl);
        setActive("custom");
        setAccentHue(h);
        applyTheme({
          bg: `url('${dataUrl}') center / cover no-repeat`,
          h, c,
          bgImageUrl: dataUrl,
        });
        toast(`已切换背景 · 强调色已根据图片调整`, "ok");
      } catch (err) {
        toast("无法解析图片", "err");
      } finally {
        setExtracting(false);
        if (fileRef.current) fileRef.current.value = "";
      }
    };
    reader.readAsDataURL(file);
  };

  return (
    <>
      <button
        className="btn btn-ghost btn-icon"
        title="切换背景与主题色"
        onClick={() => setOpen(o => !o)}
        style={{ position: "relative" }}
      >
        <span style={{
          width: 18, height: 18, borderRadius: 5,
          background: "var(--accent-500)",
          boxShadow: "0 0 0 1px var(--border-2), 0 1px 2px rgba(0,0,0,0.1)",
          display: "grid", placeItems: "center",
        }}>
          <I.Settings size={11} stroke="#fff"/>
        </span>
      </button>

      {open && (
        <div className="bg-popover" ref={popRef}>
          <div className="bg-popover-head">
            <div className="bg-popover-title">背景与主题</div>
            <span className="badge-pill" style={{ fontSize: 10.5 }}>
              accent oklch · h <span className="mono" style={{ color: "var(--accent-700)" }}>{accentHue.toFixed(0)}</span>
            </span>
            <button className="btn btn-ghost btn-icon btn-sm" onClick={() => setOpen(false)}><I.X size={12}/></button>
          </div>
          <div className="bg-grid">
            <div className={`bg-thumb none ${active === "none" ? "active" : ""}`}
              onClick={pickNone}>
              <span style={{ textAlign: "center", lineHeight: 1.3 }}>无背景</span>
              <span className="label" style={{ color: "var(--text-3)", textShadow: "none" }}>plain</span>
            </div>
            {BG_PRESETS.map(p => (
              <div key={p.id}
                className={`bg-thumb ${active === p.id ? "active" : ""}`}
                style={{ background: p.css, backgroundSize: "cover" }}
                onClick={() => pickPreset(p)}>
                <span className="label" style={{ color: "rgba(28,26,38,0.7)", textShadow: "none" }}>{p.name}</span>
              </div>
            ))}
            <div className="bg-thumb upload-thumb"
              onClick={() => fileRef.current?.click()}>
              {extracting
                ? <span style={{ fontSize: 11 }}>分析中…</span>
                : <span style={{ display: "grid", placeItems: "center", gap: 4 }}>
                    <I.Plus size={16}/>
                    <span style={{ fontSize: 11 }}>上传图片</span>
                  </span>}
            </div>
          </div>
          <div className="bg-thumb-foot">
            <div className="accent-swatch"/>
            <div style={{ flex: 1 }}>
              <div style={{ color: "var(--text-2)", fontSize: 12 }}>强调色自动从背景图采样</div>
              <div className="mono" style={{ fontSize: 10.5 }}>oklch · auto-derived</div>
            </div>
          </div>
          <input ref={fileRef} type="file" accept="image/*" hidden onChange={onUpload}/>
        </div>
      )}
    </>
  );
}

Object.assign(window, { BackgroundPicker, applyTheme, extractAccentFromImage });
