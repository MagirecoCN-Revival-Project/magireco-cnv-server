#!/usr/bin/env python3
# Claude Code PreToolUse 钩子（matcher: Bash）—— 提交前守卫：密钥扫描 + 资产边界
#
# 服务端仓库有两条不能破的线（见 .claude/CLAUDE.md 铁律二、铁律三）：
#   1. 密钥与凭据永不入库——尤其是节点目录的 Ed25519 签名私钥，它是客户端信任链
#      的根，泄漏即可伪造节点目录、把玩家登录与存档流量导向攻击者的节点；
#   2. 游戏资产不进 git——服务端持有并分发资产，但资产由部署期挂载，不入版本库。
#
# 二者一旦进入 git 历史都无法靠一次 rm 消除，故在提交前拦截。
#
# 健壮性：任何内部异常一律放行（exit 0, fail-open）—— 钩子自身绝不能挡住正常工作。
# 判定来源：已暂存文件 + 命令内联的 `git add` 目标 + `git commit -a` 的已改跟踪文件。
import sys, json, subprocess, re, shlex, os

# ── 密钥类文件名 ──────────────────────────────────────────────────────────────
SECRET_NAME = re.compile(
    r'(^|/)('
    r'\.env(\.[A-Za-z0-9_-]+)?'          # .env / .env.production（.env.example 见豁免）
    r'|credentials(\.[A-Za-z0-9]+)?'
    r'|secrets?\.(json|ya?ml|toml|ini|txt)'
    r'|id_(rsa|dsa|ecdsa|ed25519)'
    r')$', re.IGNORECASE)

SECRET_EXT = re.compile(r'\.(pem|key|p12|pfx|jks|keystore|asc)$', re.IGNORECASE)

# 示例文件豁免：只允许 .example / .sample / .template 后缀
SECRET_EXEMPT = re.compile(r'\.(example|sample|template|dist)$', re.IGNORECASE)

# ── 文件内容里的私钥块 ────────────────────────────────────────────────────────
PRIVATE_KEY_BLOCK = re.compile(rb'-----BEGIN [A-Z ]*PRIVATE KEY-----')

# ── 游戏资产 ─────────────────────────────────────────────────────────────────
ASSET_EXT = re.compile(
    r'\.(moc3|moc|exp\.json|exp3\.json|motion3\.json|physics3\.json|cdi3\.json'
    r'|model3\.json|hca|acb|awb|acf|plist|ExportJson|so|dex|smali|apk)$',
    re.IGNORECASE)

# 自制占位数据的唯一合法位置
TESTDATA = re.compile(r'^testdata/')

MEDIA_EXT = re.compile(r'\.(png|jpg|jpeg|webp|gif|bmp|tga|ogg|mp3|wav|m4a|mp4|webm)$',
                       re.IGNORECASE)


def git(*args):
    try:
        return subprocess.run(["git", *args], capture_output=True,
                              text=True, timeout=10).stdout
    except Exception:
        return ""


def collect_files(cmd):
    """还原本次提交将纳入的文件集合。"""
    files = set()
    for ln in git("diff", "--cached", "--name-only").splitlines():
        if ln.strip():
            files.add(ln.strip())
    if "git add" in cmd:
        for seg in re.split(r'&&|\|\||;|\n', cmd):
            idx = seg.find("git add")
            if idx < 0:
                continue
            try:
                toks = shlex.split(seg[idx:])[2:]
            except Exception:
                continue
            if any(t in (".", "-A", "--all", "-Av", ":/") for t in toks):
                for ln in git("status", "--porcelain").splitlines():
                    p = ln[3:].strip()
                    if p:
                        files.add(p)
            else:
                for t in toks:
                    if not t.startswith("-"):
                        files.add(t)
    if (" --all" in cmd) or re.search(r'commit[^\n;&|]*\s-[A-Za-z]*a[A-Za-z]*\b', cmd):
        for ln in git("diff", "--name-only").splitlines():
            if ln.strip():
                files.add(ln.strip())
    return files


def is_secret_name(path):
    if SECRET_EXEMPT.search(path):
        return False
    return bool(SECRET_NAME.search(path) or SECRET_EXT.search(path))


def has_private_key_block(path):
    """只读前 64KB：私钥块总在文件开头附近，避免读入大文件。"""
    try:
        if not os.path.isfile(path) or os.path.getsize(path) > 8 * 1024 * 1024:
            return False
        with open(path, "rb") as f:
            return bool(PRIVATE_KEY_BLOCK.search(f.read(65536)))
    except Exception:
        return False


def main():
    try:
        data = json.loads(sys.stdin.read())
    except Exception:
        return 0
    cmd = (data.get("tool_input") or {}).get("command") or ""
    if "git commit" not in cmd:
        return 0

    files = collect_files(cmd)

    secrets = sorted(f for f in files if is_secret_name(f))
    # 文件名没露馅，但内容含私钥块的（如被命名为 config.txt 的私钥）
    if "[skip-secret-check]" not in cmd:
        secrets += sorted(f for f in files
                          if f not in secrets and has_private_key_block(f))
    if "[skip-secret-check]" in cmd:
        secrets = []

    assets = sorted(f for f in files if ASSET_EXT.search(f))
    media = sorted(f for f in files
                   if MEDIA_EXT.search(f) and not TESTDATA.match(f))
    if "[skip-asset-check]" in cmd:
        assets, media = [], []

    if not secrets and not assets and not media:
        return 0

    msg = []
    if secrets:
        msg.append(
            "⛔ 密钥扫描未通过：本次提交含疑似密钥／凭据的文件。\n  "
            + "\n  ".join(secrets) + "\n\n"
            "配置一律经环境变量或部署期密文注入，源码中保持空值。\n"
            "**节点目录的 Ed25519 签名私钥是客户端信任链的根**——泄漏即可伪造节点目录、\n"
            "把玩家的登录与存档流量导向攻击者的节点。它不得出现在任何在线服务上。\n"
            "确属误判（如示例文件）时加标记 [skip-secret-check]，并写明理由。\n\n")
    if assets or media:
        msg.append("⛔ 资产边界检查未通过：本次提交含游戏资产。\n")
        if assets:
            msg.append("  明确的资产格式：\n    " + "\n    ".join(assets) + "\n")
        if media:
            msg.append("  图片／音视频（自制占位数据请放 testdata/）：\n    "
                       + "\n    ".join(media) + "\n")
        msg.append(
            "\n服务端虽持有并分发资产，但资产由**部署期挂载或同步**，不入版本库；\n"
            "仓库里只应有拉取与校验脚本。确属误判时加标记 [skip-asset-check]。\n\n")
    msg.append("注意：以上内容一旦进入 git 历史，都无法靠一次 rm 消除。\n")
    sys.stderr.write("".join(msg))
    return 2


if __name__ == "__main__":
    try:
        sys.exit(main())
    except Exception:
        sys.exit(0)   # fail-open
