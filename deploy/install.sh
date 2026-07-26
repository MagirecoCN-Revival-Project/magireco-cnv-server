#!/usr/bin/env bash
# 魔法纪录复兴计划 · 服务端一键部署脚本(systemd 托管)
# 本脚本会把对应角色(panel / node-business / node-edge)的二进制、systemd unit、
# 配置 .env、运行用户与日志切割统一下发,跑完即为完整可托管态。
# ------------------------------------------------------------------------------
# 受支持的 Linux:
#   - Ubuntu : 20.04, 22.04, 24.04
#   - Debian : 11, 12
#   - RHEL 系: AlmaLinux / Rocky / RHEL 8、9
# ------------------------------------------------------------------------------
# 入口模式:
#   远程一行(仿 MCSManager 风格,无需 clone 仓库):
#     sudo su -c "wget -qO- https://raw.githubusercontent.com/MagirecoCN-Revival-Project/magirecocn-resource-server/main/deploy/install.sh | bash"
#     sudo su -c "curl -fsSL https://raw.githubusercontent.com/MagirecoCN-Revival-Project/magirecocn-resource-server/main/deploy/install.sh | bash -s -- --role panel"
#
#   本地 checkout:
#     sudo ./deploy/install.sh --role panel
#     sudo ./deploy/install.sh --role node-business [--bin /path/to/magireco-node]
#     sudo ./deploy/install.sh --role node-edge     [--non-interactive]
# ------------------------------------------------------------------------------
# 设计原则:
#   * 面板 / 业务节点:**业务配置由各自安装向导管**(panel /install,node /setup);
#     本脚本只下二进制、写 systemd unit、生成它们启动前必须的密钥
#     (panel 的 CNV_PANEL_KEY、business 的 CNV_ADMIN_JWT_SECRET)。
#   * 边缘节点:**没有向导,配置全部由 .env 管**,按 prompts 填关键字段。
#   * 幂等:再跑只覆盖二进制 + unit,**不动**现有 .env 与 /var/lib/magireco/<角色>/。
# ------------------------------------------------------------------------------

set -u
set -o pipefail

# --------------- 可配置项(命令行可覆盖)---------------#

# 目标仓库(用来从 Release latest 拉二进制)
repo="MagirecoCN-Revival-Project/magirecocn-resource-server"
release_base="https://github.com/${repo}/releases/latest/download"

# 部署根路径,FHS 风格
opt_root="/opt/magireco"
var_root="/var/lib/magireco"
log_root="/var/log/magireco"
etc_root="/etc/magireco"
systemd_root="/etc/systemd/system"

# 运行身份
service_user="magireco"
service_group="magireco"

# 角色(panel / node-business / node-edge),命令行 --role 覆盖或交互菜单设置
role=""
# 二进制路径覆盖(--bin),默认从本地查找 + Release 下载兜底
bin_override=""
# 边缘节点跳过 prompt(--non-interactive)
non_interactive=false


# --------------- 全局状态变量 ---------------#
#                  DO NOT MODIFY                  #

# 终端能力(detect_terminal_capabilities 后赋值)
SUPPORTS_COLOR=false
SUPPORTS_STYLE=false
RESET="\033[0m"

# 颜色与样式表(与 MCSManager 同结构,便于一致的视觉)
declare -A FG_COLORS=(
  [black]="\033[0;30m"
  [red]="\033[0;31m"
  [green]="\033[0;32m"
  [yellow]="\033[0;33m"
  [blue]="\033[0;34m"
  [magenta]="\033[0;35m"
  [cyan]="\033[0;36m"
  [white]="\033[0;37m"
)
declare -A STYLES=(
  [bold]="\033[1m"
  [underline]="\033[4m"
  [italic]="\033[3m"
  [clear_line]="\r\033[2K"
  [strikethrough]="\033[9m"
)

# 部署前必须存在的系统命令
required_commands=(
  install
  useradd
  id
  uname
  date
  head
  od
  tr
  sed
  mktemp
  cat
  realpath
  systemctl
)

# 运行时检测
arch=""

# 脚本所在目录,用来定位本地 deploy/systemd/* 与 deploy/edge.env.example。
# curl|bash 模式下 BASH_SOURCE 是 /dev/fd/63 之类的伪路径或不存在,deploy_dir
# 会被设为空字符串;后续 install_unit / fetch_edge_env_template 自动回落到内嵌。
deploy_dir=""
if [[ -n "${BASH_SOURCE[0]:-}" && -f "${BASH_SOURCE[0]}" ]]; then
  deploy_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" >/dev/null 2>&1 && pwd)" || deploy_dir=""
fi


### --------------- 辅助函数 --------------- ###

# 与 MCSManager 同接口的彩色打印:cprint <color> [style...] [noprefix] [nonl] "text"
# 颜色: black|red|green|yellow|blue|magenta|cyan|white
# 样式: bold|underline|italic|clear_line|strikethrough
# 前缀: 默认带时间戳 + "[Magireco Installer]" 标签,noprefix 关闭
# 换行: 默认有,nonl 关闭
cprint() {
  local color="" styles="" text=""
  local disable_prefix=false disable_newline=false

  while [[ $# -gt 1 ]]; do
    case "$1" in
      black|red|green|yellow|blue|magenta|cyan|white)
        color="$1"
        ;;
      bold|underline|italic|clear_line|strikethrough)
        styles+="${STYLES[$1]}"
        ;;
      noprefix) disable_prefix=true ;;
      nonl)     disable_newline=true ;;
    esac
    shift
  done

  text="${1:-}"

  local prefix_text=""
  if [[ "$disable_prefix" != true ]]; then
    local ts label
    ts="[$(date +%H:%M:%S)]"
    label="[Magireco Installer]"
    prefix_text="${FG_COLORS[white]}$ts $label${RESET} "
  fi

  local prefix=""
  if [[ -n "$color" && "$SUPPORTS_COLOR" == true ]]; then
    prefix+="${FG_COLORS[$color]}"
  fi
  if [[ "$SUPPORTS_STYLE" == true ]]; then
    prefix="$styles$prefix"
  fi

  if [[ "$disable_newline" == true ]]; then
    printf "%b%b%s%b"   "$prefix_text" "$prefix" "$text" "$RESET"
  else
    printf "%b%b%s%b\n" "$prefix_text" "$prefix" "$text" "$RESET"
  fi
}

# 步骤包装器:函数失败时打红字 + 退出 1,避免 set -e 直接吞掉上下文
safe_run() {
  local func="$1" err_msg="$2"
  shift 2
  if ! "$func" "$@"; then
    cprint red bold "错误:$err_msg"
    exit 1
  fi
}

# 检查当前终端是否支持彩色与样式
detect_terminal_capabilities() {
  SUPPORTS_COLOR=false
  SUPPORTS_STYLE=false

  if [[ -t 1 ]] && command -v tput >/dev/null 2>&1; then
    if [[ "$(tput colors 2>/dev/null || echo 0)" -ge 8 ]]; then
      SUPPORTS_COLOR=true
    fi
    if tput bold >/dev/null 2>&1 && tput smul >/dev/null 2>&1; then
      SUPPORTS_STYLE=true
    fi
  fi

  if [[ "$SUPPORTS_COLOR" == true ]]; then
    cprint green "[OK] 终端支持彩色输出。"
  else
    cprint yellow "注:终端不支持彩色输出,改为纯文本继续。"
  fi
  if [[ "$SUPPORTS_STYLE" == true ]]; then
    cprint green "[OK] 终端支持粗体与下划线。"
  else
    cprint yellow "注:终端不支持高级样式,改为纯文本继续。"
  fi
  return 0
}

# 必须 root 才能跑(写 /etc/systemd、建系统用户)
check_root() {
  if [[ -n "${EUID:-}" ]]; then
    if [[ "$EUID" -ne 0 ]]; then
      cprint red bold "错误:本脚本只能在 root 或 sudo 下运行,请切换用户或加 sudo。"
      return 1
    fi
  elif [[ "$(id -u)" -ne 0 ]]; then
    cprint red bold "错误:本脚本只能在 root 或 sudo 下运行,请切换用户或加 sudo。"
    return 1
  fi
  cprint green "已确认 root 身份。"
}

# 把 uname -m 映射成 Release 资产命名约定
detect_arch() {
  local m
  m="$(uname -m)"
  case "$m" in
    x86_64|amd64)  arch="amd64" ;;
    aarch64|arm64) arch="arm64" ;;
    *)
      cprint red bold "不支持的架构:$m(仅 x86_64 / aarch64)"
      return 1
      ;;
  esac
  cprint cyan "检测到架构:$arch"
}

# 必备命令存在性检查
check_required_commands() {
  local missing=0 cmd
  for cmd in "${required_commands[@]}"; do
    if ! command -v "$cmd" >/dev/null 2>&1; then
      cprint red "缺少必需命令:$cmd"
      missing=1
    fi
  done

  # curl 或 wget 任一即可(下载二进制时用)
  if ! command -v curl >/dev/null 2>&1 && ! command -v wget >/dev/null 2>&1; then
    cprint red "需要 curl 或 wget(任一)用于下载二进制。"
    missing=1
  fi

  if [[ $missing -ne 0 ]]; then
    cprint red bold "请先用包管理器装上以上命令再重试。"
    return 1
  fi
  cprint green "必需命令检查通过。"
}

# 命令行参数解析
parse_args() {
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --role)
        if [[ -n "${2:-}" ]]; then
          role="$2"
          shift 2
        else
          cprint red "错误:--role 缺少值(panel / node-business / node-edge)"
          return 1
        fi
        ;;
      --bin)
        if [[ -n "${2:-}" ]]; then
          bin_override="$2"
          shift 2
        else
          cprint red "错误:--bin 缺少路径"
          return 1
        fi
        ;;
      --non-interactive)
        non_interactive=true
        shift
        ;;
      # 兼容旧用法:直接 panel / node-business / node-edge 作为首个位置参数
      panel|node-business|node-edge)
        role="$1"
        shift
        ;;
      -h|--help|help)
        print_usage
        exit 0
        ;;
      *)
        cprint red "错误:未知参数 '$1'"
        print_usage
        return 1
        ;;
    esac
  done

  # 没指定角色 → 进交互菜单(菜单直接设全局 role,避免 stdout 被 $() 捕获)
  if [[ -z "$role" ]]; then
    interactive_menu
  fi

  case "$role" in
    panel|node-business|node-edge) : ;;
    *)
      cprint red "错误:未知角色 '$role'(必须是 panel / node-business / node-edge)"
      return 1
      ;;
  esac
  cprint cyan "目标角色:$role"
}

print_usage() {
  cat <<EOF
用法:
  在线一键:
    sudo su -c "curl -fsSL https://raw.githubusercontent.com/${repo}/main/deploy/install.sh | bash"
    sudo su -c "wget -qO- https://raw.githubusercontent.com/${repo}/main/deploy/install.sh | bash -s -- --role panel"

  本地 checkout:
    sudo ./deploy/install.sh --role panel
    sudo ./deploy/install.sh --role node-business [--bin PATH]
    sudo ./deploy/install.sh --role node-edge     [--bin PATH] [--non-interactive]

  --role ROLE        panel / node-business / node-edge,不指定时进入交互菜单
  --bin PATH         指定二进制位置(默认本地 ./, ./bin/, /usr/local/bin/ 找,
                     都没有则从 GitHub Release latest 下载)
  --non-interactive  node-edge 跳过 prompt,使用模板默认值

部署后的关键路径:
  二进制:   $opt_root/<角色>/bin/
  状态:     $var_root/<角色>/
  配置:     $etc_root/<panel|node-business|node-edge>.env
  日志:     $log_root/<角色>.log
  systemd: $systemd_root/magireco-<角色>.service
EOF
}

# 从 /dev/tty 读一行(curl|bash 时 stdin 被管道占用,普通 read 立刻 EOF)
tty_read() {
  local q="$1" var="$2" default="${3:-}"
  local input answer
  if [[ -r /dev/tty ]]; then
    input=/dev/tty
  elif [[ -t 0 ]]; then
    input=/dev/stdin
  else
    if [[ -n "$default" ]]; then
      printf -v "$var" '%s' "$default"
      return
    fi
    cprint red bold "非交互式环境且无默认值,无法询问 '$q'"
    return 1
  fi
  local answer=""
  if [[ -n "$default" ]]; then
    read -r -p "$q [$default]: " answer < "$input" 2>/dev/null || answer=""
    [[ -z "$answer" ]] && answer="$default"
  else
    read -r -p "$q: " answer < "$input" 2>/dev/null || answer=""
  fi
  printf -v "$var" '%s' "$answer"
}

# 三选一菜单(无 --role 时调用)
interactive_menu() {
  cprint cyan noprefix ""
  cprint cyan noprefix "  请选择要部署的角色:"
  cprint white noprefix "    1) panel          管理面板"
  cprint white noprefix "    2) node-business  业务节点(单机:跟面板一起装)"
  cprint white noprefix "    3) node-edge      边缘节点(分担资源下载带宽)"
  cprint white noprefix "    q) 退出"
  cprint cyan noprefix ""

  local choice picked=""
  while [[ -z "$picked" ]]; do
    tty_read "请选择 [1-3, q]" choice "" || return 1
    case "$choice" in
      1) picked="panel" ;;
      2) picked="node-business" ;;
      3) picked="node-edge" ;;
      q|Q) exit 0 ;;
      "")
        cprint red bold "未能读到选择(可能没有 /dev/tty);请用 --role 指定角色。"
        return 1
        ;;
      *) cprint yellow "无效选择 '$choice',请重新输入。" ;;
    esac
  done
  role="$picked"
}

# 64 字节 hex(32 字节熵)。读 /dev/urandom 避开 openssl 等可选依赖。
gen_secret() {
  head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n'
}

# 创建系统运行用户(--system,无 home,无 login shell)
ensure_service_user() {
  if id "$service_user" >/dev/null 2>&1; then
    cprint cyan "系统用户 $service_user 已存在。"
    return 0
  fi
  cprint cyan "创建系统用户 $service_user …"
  useradd --system --no-create-home --shell /usr/sbin/nologin \
          --user-group "$service_user"
  cprint green "用户 $service_user 已建。"
}

ensure_dir() {
  local p="$1" mode="${2:-0750}"
  install -d -m "$mode" -o "$service_user" -g "$service_group" "$p"
}


### --------------- 二进制 / unit / .env --------------- ###

# 二进制定位:--bin > 本地 ./<name> > ./bin/<name> > /usr/local/bin/<name> > Release 下载
# 输出最终落地路径到 stdout(供调用方 install_binary)
fetch_binary() {
  local name="$1" override="${2:-}"
  if [[ -n "$override" ]]; then
    if [[ ! -f "$override" ]]; then
      cprint red bold "找不到 --bin 指定的二进制:$override" >&2
      return 1
    fi
    realpath "$override"
    return
  fi
  local cand
  for cand in "./$name" "./bin/$name" "/usr/local/bin/$name"; do
    if [[ -f "$cand" ]]; then
      realpath "$cand"
      return
    fi
  done

  # 本地都没有 —— 走 Release 下载
  local url tmp
  url="${release_base}/${name}-linux-${arch}"
  tmp="$(mktemp /tmp/magireco-bin.XXXXXX)"
  cprint cyan "从 GitHub Release 下载:$url" >&2
  if command -v curl >/dev/null 2>&1; then
    if ! curl --fail --location --progress-bar --output "$tmp" "$url" >&2; then
      rm -f "$tmp"
      cprint red bold "下载失败:$url" >&2
      return 1
    fi
  else
    if ! wget --quiet --show-progress -O "$tmp" "$url" >&2; then
      rm -f "$tmp"
      cprint red bold "下载失败:$url" >&2
      return 1
    fi
  fi
  chmod 0755 "$tmp"
  printf '%s' "$tmp"
}

install_binary() {
  local src="$1" dst_dir="$2"
  install -d -m 0755 -o root -g root "$dst_dir"
  install -m 0755 -o root -g root "$src" "$dst_dir/"
  cprint green "二进制 $(basename "$src") → $dst_dir/"
}

# 前端静态资源落地。面板托管 web/(节点已不再托管 WebUI),故面板安装时铺一份。
# 来源:本地仓库优先(deploy_dir/../web),否则从 Release 下载 web-static.tar.gz 解压。
# 幂等:目标目录内容刷新为最新一份;属主 root:root,服务用户只读。
lay_down_web() {
  local dest="$1"   # 形如 /opt/magireco/panel/web
  local src_dir=""
  if [[ -n "$deploy_dir" && -d "$deploy_dir/../web" ]]; then
    src_dir="$(cd "$deploy_dir/../web" >/dev/null 2>&1 && pwd)" || src_dir=""
  fi

  install -d -m 0755 -o root -g root "$dest"
  rm -rf "${dest:?}/"* 2>/dev/null || true

  if [[ -n "$src_dir" ]]; then
    cp -a "$src_dir/." "$dest/"
    cprint green "前端资源 ← 本地仓库 $src_dir"
  else
    local url tmp tmpd
    url="${release_base}/web-static.tar.gz"
    tmp="$(mktemp /tmp/magireco-web.XXXXXX)"
    tmpd="$(mktemp -d)"
    cprint cyan "从 GitHub Release 下载前端:$url"
    if command -v curl >/dev/null 2>&1; then
      if ! curl --fail --location --progress-bar --output "$tmp" "$url" >&2; then
        rm -rf "$tmp" "$tmpd"; cprint red bold "前端下载失败:$url"; return 1
      fi
    else
      if ! wget --quiet --show-progress -O "$tmp" "$url" >&2; then
        rm -rf "$tmp" "$tmpd"; cprint red bold "前端下载失败:$url"; return 1
      fi
    fi
    if ! tar -xzf "$tmp" -C "$tmpd"; then
      rm -rf "$tmp" "$tmpd"; cprint red bold "前端解压失败:$tmp"; return 1
    fi
    # 打包命令为 `tar -czf web-static.tar.gz web`,解出来是 web/ 子目录。
    if [[ -d "$tmpd/web" ]]; then
      cp -a "$tmpd/web/." "$dest/"
    else
      cp -a "$tmpd/." "$dest/"
    fi
    rm -rf "$tmp" "$tmpd"
    cprint green "前端资源 ← Release web-static.tar.gz"
  fi

  chown -R root:root "$dest"
  chmod -R a+rX "$dest"
}

# 写 unit 到 /etc/systemd/system,本地仓库优先,否则用内嵌 heredoc
install_unit() {
  local role_name="$1"
  local unit="magireco-${role_name}.service"
  local dest="$systemd_root/$unit"
  local local_src=""
  if [[ -n "$deploy_dir" && -f "$deploy_dir/systemd/$unit" ]]; then
    local_src="$deploy_dir/systemd/$unit"
  fi

  if [[ -n "$local_src" ]]; then
    install -m 0644 -o root -g root "$local_src" "$dest"
    cprint green "unit $unit → $dest(来自本地仓库)"
  else
    local tmp
    tmp="$(mktemp)"
    case "$role_name" in
      panel)         emit_unit_panel         > "$tmp" ;;
      node-business) emit_unit_node_business > "$tmp" ;;
      node-edge)     emit_unit_node_edge     > "$tmp" ;;
      *) rm -f "$tmp"; cprint red bold "未知角色: $role_name"; return 1 ;;
    esac
    install -m 0644 -o root -g root "$tmp" "$dest"
    rm -f "$tmp"
    cprint green "unit $unit → $dest(内嵌模板)"
  fi
}

# .env 原子写:权限 0640,属主 root:magireco(root 可写,服务可读)
write_env_atomic() {
  local path="$1" content="$2"
  install -d -m 0755 -o root -g root "$(dirname "$path")"
  local tmp
  tmp="$(mktemp "$(dirname "$path")/.env.XXXXXX")"
  printf '%s' "$content" > "$tmp"
  chown root:"$service_group" "$tmp"
  chmod 0640 "$tmp"
  mv -f "$tmp" "$path"
}

ensure_logrotate() {
  local conf="/etc/logrotate.d/magireco"
  [[ -f "$conf" ]] && return 0
  cat > "$conf" <<'LR'
/var/log/magireco/*.log {
    weekly
    rotate 4
    compress
    delaycompress
    missingok
    notifempty
    copytruncate
    su magireco magireco
}
LR
  chmod 0644 "$conf"
  cprint green "logrotate 配置 → $conf"
}

# 边缘节点 .env 模板:本地优先,否则用内嵌
fetch_edge_env_template() {
  if [[ -n "$deploy_dir" && -f "$deploy_dir/edge.env.example" ]]; then
    cat "$deploy_dir/edge.env.example"
  else
    emit_edge_env_example
  fi
}


### --------------- 内嵌模板(必须与 deploy/systemd/*.service 同源)--------- ###

emit_unit_panel() {
cat <<'EOF'
# 面板。WordPress 式安装向导在浏览器里完成 DB/管理员配置;本 unit 只负责
# 进程托管 + 自动重启。CNV_PANEL_KEY(cookie HMAC)由 deploy/install.sh
# 在首次安装时生成并写入 /etc/magireco/panel.env。
#
# 修改后必须 `systemctl daemon-reload`。

[Unit]
Description=Magireco Revival Panel
Documentation=https://github.com/MagirecoCN-Revival-Project/magirecocn-resource-server
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=magireco
Group=magireco
WorkingDirectory=/var/lib/magireco/panel
EnvironmentFile=/etc/magireco/panel.env
ExecStart=/opt/magireco/panel/bin/magireco-panel
Restart=on-failure
RestartSec=5s

NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ReadWritePaths=/var/lib/magireco/panel /var/log/magireco
ProtectHome=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
RestrictSUIDSGID=true
LockPersonality=true
SystemCallArchitectures=native
StandardOutput=append:/var/log/magireco/panel.log
StandardError=append:/var/log/magireco/panel.log

[Install]
WantedBy=multi-user.target
EOF
}

emit_unit_node_business() {
cat <<'EOF'
# 业务节点。配置(CNV_DB_URL / CNV_ADMIN_JWT_SECRET 等)由面板安装向导
# 写入 /etc/magireco/node-business.env;本 unit 只负责进程托管。
#
# 流程:
#   1) deploy/install.sh --role node-business → 装二进制 + unit + 生成 ADMIN_JWT_SECRET
#                                                (此时 CNV_DB_URL 还没有,服务不会启)
#   2) 启动面板,在向导第 4 步勾"本机也装业务节点"
#   3) 向导写完业务 DB 后,把 CNV_DB_URL 追加到 /etc/magireco/node-business.env
#      并 systemctl start
#
# 注:本 unit 默认 enabled 但不立刻 start —— 等向导填完配置再起。

[Unit]
Description=Magireco Revival Business Node
Documentation=https://github.com/MagirecoCN-Revival-Project/magirecocn-resource-server
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=magireco
Group=magireco
WorkingDirectory=/var/lib/magireco/node-business
EnvironmentFile=/etc/magireco/node-business.env
ExecStart=/opt/magireco/node-business/bin/magireco-node
Restart=on-failure
RestartSec=5s

NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ReadWritePaths=/var/lib/magireco/node-business /var/log/magireco
ProtectHome=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
RestrictSUIDSGID=true
LockPersonality=true
SystemCallArchitectures=native
StandardOutput=append:/var/log/magireco/node-business.log
StandardError=append:/var/log/magireco/node-business.log

[Install]
WantedBy=multi-user.target
EOF
}

emit_unit_node_edge() {
cat <<'EOF'
# 边缘节点。配置全部由 /etc/magireco/node-edge.env 决定;deploy/install.sh
# 会从 deploy/edge.env.example 复制一份并按问答填好关键字段。
#
# 边缘节点不连业务 DB、不持游戏数据,只分发资源 + 与面板维持管控 WS。

[Unit]
Description=Magireco Revival Edge Node
Documentation=https://github.com/MagirecoCN-Revival-Project/magirecocn-resource-server
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=magireco
Group=magireco
WorkingDirectory=/var/lib/magireco/node-edge
EnvironmentFile=/etc/magireco/node-edge.env
ExecStart=/opt/magireco/node-edge/bin/magireco-node
Restart=on-failure
RestartSec=5s

NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ReadWritePaths=/var/lib/magireco/node-edge /var/log/magireco
ProtectHome=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
RestrictSUIDSGID=true
LockPersonality=true
SystemCallArchitectures=native
StandardOutput=append:/var/log/magireco/node-edge.log
StandardError=append:/var/log/magireco/node-edge.log

[Install]
WantedBy=multi-user.target
EOF
}

emit_edge_env_example() {
cat <<'EOF'
# 边缘节点配置 — 安装时由 deploy/install.sh 复制到 /etc/magireco/node-edge.env
# 之后按需修改。systemd EnvironmentFile 不支持 ${VAR} 展开,所有值必须是字面量。
#
# 修改后:  systemctl restart magireco-node-edge

# ── 必填 ──────────────────────────────────────────────────────────────
CNV_NODE_ROLE=edge

# 节点对外的 base URL,客户端从面板拿到 directory 后会直连这个地址下载资源。
# 必须能从公网解析 + 通过反代/Nginx 落到本节点的 CNV_ADDR。
CNV_PUBLIC_URL=https://edge-1.example.com

# 本节点要分发的资源根目录。文件层级即 URL 路径,见 docs/self-host/nodes.md。
CNV_PRIMARY_RES_DIR=/srv/magireco-res

# ── 可选(给默认值即可) ──────────────────────────────────────────────
CNV_ADDR=:8080
CNV_PRIMARY_RES_PATH=/res
CNV_NODE_ID=edge-1

# 节点密钥文件;首次启动自动生成并打印,管理员复制到面板节点注册表配对。
CNV_NODE_KEY_FILE=/var/lib/magireco/node-edge/node.key

# 管控 WS:面板拨号到这个地址。同机部署面板时用 127.0.0.1,跨机改 :9090 并防火墙限制。
CNV_CONTROL_ADDR=127.0.0.1:9090

# 反代后请设为信任的前置网段,否则限流和审计 IP 会全部记到反代上。
# 取值:off / all / loopback / CIDR 列表。见 docs/self-host/configuration.md。
# CNV_TRUST_PROXY=loopback
EOF
}


### --------------- 三角色安装 --------------- ###

install_role_panel() {
  local bin
  cprint cyan bold "正在部署:管理面板(panel)"
  if ! bin="$(fetch_binary magireco-panel "$bin_override")"; then
    return 1
  fi
  cprint cyan "二进制源:$bin"

  ensure_service_user
  ensure_dir "$opt_root/panel/bin" 0755
  ensure_dir "$var_root/panel"
  ensure_dir "$log_root"
  install -d -m 0755 -o root -g root "$etc_root"

  install_binary "$bin" "$opt_root/panel/bin"

  # 面板托管全部人类前端(登录/注册/管理后台/用户中心),需要 web/ 静态资源。
  if ! lay_down_web "$opt_root/panel/web"; then
    cprint red bold "前端资源落地失败,面板将只能显示内置节点状态页。可稍后手动铺 $opt_root/panel/web"
  fi

  local env_file="$etc_root/panel.env"
  if [[ ! -f "$env_file" ]]; then
    local secret; secret="$(gen_secret)"
    write_env_atomic "$env_file" "# 面板配置 — 仅启动前必须的密钥由本脚本生成。
# 其它运行时配置(节点注册表、面板管理员等)由浏览器内的安装向导写入
# 面板自带的 SQLite,不在此文件里管。
#
# 修改后 systemctl restart magireco-panel
CNV_PANEL_KEY=$secret
CNV_PANEL_DB_FILE=$var_root/panel/panel.db
CNV_WEB_DIR=$opt_root/panel/web
CNV_ADDR=:8090
"
    cprint green "已生成 $env_file(含随机 CNV_PANEL_KEY)"
  else
    cprint yellow "$env_file 已存在,保留旧值不动。"
  fi

  install_unit panel
  ensure_logrotate

  cprint cyan "正在重新加载 systemd 守护进程 …"
  systemctl daemon-reload
  cprint cyan "正在启动面板服务 …"
  if systemctl enable --now magireco-panel.service; then
    cprint green "面板服务已启动。"
  else
    cprint red bold "启动面板失败,检查 journalctl -u magireco-panel"
    return 1
  fi
}

install_role_node_business() {
  local bin
  cprint cyan bold "正在部署:业务节点(node-business)"
  if ! bin="$(fetch_binary magireco-node "$bin_override")"; then
    return 1
  fi
  cprint cyan "二进制源:$bin"

  ensure_service_user
  ensure_dir "$opt_root/node-business/bin" 0755
  ensure_dir "$var_root/node-business"
  ensure_dir "$log_root"
  install -d -m 0755 -o root -g root "$etc_root"

  install_binary "$bin" "$opt_root/node-business/bin"

  local env_file="$etc_root/node-business.env"
  if [[ ! -f "$env_file" ]]; then
    local secret; secret="$(gen_secret)"

    # 面板对外 URL:接入面板时由运维输入,脚本据此全自动写入 .env(留空=单机自带前端)。
    # 作用:① 客户端入口页 /account/register|forgot|verify-email 302 跳面板;
    #       ② CORS 放行来源,允许面板托管的前端跨域直连本节点 API。
    local panel_public_url="" panel_line
    if [[ "$non_interactive" != true ]]; then
      cprint cyan ""
      cprint cyan bold "业务节点接入面板(直接回车=留空,按单机自带前端部署):"
      tty_read "  面板对外 URL CNV_PANEL_PUBLIC_URL(浏览器/客户端访问面板的地址,如 http://1.2.3.4:8090)" panel_public_url ""
    fi
    if [[ -n "$panel_public_url" ]]; then
      panel_public_url="${panel_public_url%/}"   # 去尾斜杠,与节点侧规整一致
      panel_line="CNV_PANEL_PUBLIC_URL=$panel_public_url"
    else
      panel_line="# 接入面板时填面板对外 URL(客户端入口页 302 跳面板 + 放行前端跨域直连),如:
# CNV_PANEL_PUBLIC_URL=http://<面板域名或IP>:8090"
    fi

    write_env_atomic "$env_file" "# 业务节点配置 — 启动前必须的密钥由本脚本生成。
# CNV_DB_URL 留空 —— 面板安装向导在第 4 步勾'本机也装业务节点'后会回写。
# 如果你不用面板向导(独立部署),把 CNV_DB_URL 填上再 systemctl start。
#
# 修改后 systemctl restart magireco-node-business
CNV_NODE_ROLE=business
CNV_ADMIN_JWT_SECRET=$secret
CNV_NODE_KEY_FILE=$var_root/node-business/node.key
CNV_CONTROL_ADDR=127.0.0.1:9090
CNV_ADDR=:8080
# CNV_DB_URL=postgres://user:pass@host:5432/magireco?sslmode=disable
$panel_line
"
    if [[ -n "$panel_public_url" ]]; then
      cprint green "已生成 $env_file(随机 CNV_ADMIN_JWT_SECRET;CNV_PANEL_PUBLIC_URL=$panel_public_url;CNV_DB_URL 待填)"
    else
      cprint green "已生成 $env_file(含随机 CNV_ADMIN_JWT_SECRET;CNV_DB_URL 待填;未接面板=单机自带前端)"
    fi
  else
    cprint yellow "$env_file 已存在,保留旧值不动。"
  fi

  install_unit node-business
  ensure_logrotate

  cprint cyan "正在重新加载 systemd 守护进程 …"
  systemctl daemon-reload
  cprint cyan "正在启用业务节点服务(不立刻 start)…"
  if systemctl enable magireco-node-business.service; then
    cprint green "业务节点 unit 已 enable(等向导填 DB 后 systemctl start)。"
  else
    cprint red bold "enable 失败,检查 systemctl status magireco-node-business"
    return 1
  fi
}

install_role_node_edge() {
  local bin
  cprint cyan bold "正在部署:边缘节点(node-edge)"
  if ! bin="$(fetch_binary magireco-node "$bin_override")"; then
    return 1
  fi
  cprint cyan "二进制源:$bin"

  ensure_service_user
  ensure_dir "$opt_root/node-edge/bin" 0755
  ensure_dir "$var_root/node-edge"
  ensure_dir "$log_root"
  install -d -m 0755 -o root -g root "$etc_root"

  install_binary "$bin" "$opt_root/node-edge/bin"

  local env_file="$etc_root/node-edge.env"
  if [[ ! -f "$env_file" ]]; then
    local rendered
    rendered="$(fetch_edge_env_template | sed \
        "s|^CNV_NODE_KEY_FILE=.*|CNV_NODE_KEY_FILE=$var_root/node-edge/node.key|")"

    if [[ "$non_interactive" != true ]]; then
      cprint cyan ""
      cprint cyan bold "边缘节点关键配置(直接回车保留默认):"
      local public_url res_dir node_id
      tty_read "  CNV_PUBLIC_URL" public_url "https://edge-1.example.com"
      tty_read "  CNV_PRIMARY_RES_DIR" res_dir "/srv/magireco-res"
      tty_read "  CNV_NODE_ID" node_id "edge-1"
      rendered="$(printf '%s' "$rendered" | sed \
          -e "s|^CNV_PUBLIC_URL=.*|CNV_PUBLIC_URL=$public_url|" \
          -e "s|^CNV_PRIMARY_RES_DIR=.*|CNV_PRIMARY_RES_DIR=$res_dir|" \
          -e "s|^CNV_NODE_ID=.*|CNV_NODE_ID=$node_id|")"
    else
      cprint yellow "非交互模式,$env_file 使用模板默认值 — 必须手工编辑 CNV_PUBLIC_URL 等再启用。"
    fi

    write_env_atomic "$env_file" "$rendered"
    cprint green "已生成 $env_file"
  else
    cprint yellow "$env_file 已存在,保留旧值不动。"
  fi

  install_unit node-edge
  ensure_logrotate

  cprint cyan "正在重新加载 systemd 守护进程 …"
  systemctl daemon-reload
  cprint cyan "正在启用边缘节点服务(不立刻 start)…"
  if systemctl enable magireco-node-edge.service; then
    cprint green "边缘节点 unit 已 enable(请确认 .env 后 systemctl start)。"
  else
    cprint red bold "enable 失败,检查 systemctl status magireco-node-edge"
    return 1
  fi
}


### --------------- 结尾横幅 + 结果 --------------- ###

print_banner() {
  clear || true
  # 用反斜杠转义反引号(下方 ASCII art 含 \` 用作字符)
  cprint cyan noprefix " __  __             _                              "
  cprint cyan noprefix "|  \\/  | __ _  __ _(_)_ __ ___  ___ ___           "
  cprint cyan noprefix "| |\\/| |/ _\` |/ _\` | | '__/ _ \\/ __/ _ \\        "
  cprint cyan noprefix "| |  | | (_| | (_| | | | |  __/ (_| (_) |          "
  cprint cyan noprefix "|_|  |_|\\__,_|\\__, |_|_|  \\___|\\___\\___/      "
  cprint cyan noprefix "              |___/                                "
  echo ""
  cprint white noprefix "  魔法纪录复兴计划 · 服务端部署器"
  cprint white noprefix "  MagirecoCN Revival · Server Installer"
  cprint white noprefix "  https://github.com/${repo}"
  echo ""
}

print_install_result() {
  local ip_address
  ip_address="$(hostname -I 2>/dev/null | awk '{print $1}')"
  [[ -z "$ip_address" ]] && ip_address="<本机 IP>"

  cprint cyan noprefix ""
  cprint cyan noprefix "──────────────────────────────────────────────────────────────"
  cprint green bold noprefix "✔ 安装完成(角色:$role,架构:linux/$arch)"
  cprint cyan noprefix "──────────────────────────────────────────────────────────────"
  cprint cyan noprefix ""

  cprint yellow noprefix "关键路径:"
  cprint white  noprefix "  二进制:  $opt_root/$role/bin/"
  cprint white  noprefix "  状态:    $var_root/$role/"
  cprint white  noprefix "  配置:    $etc_root/$role.env"
  cprint white  noprefix "  日志:    $log_root/$role.log"
  cprint white  noprefix "  systemd: $systemd_root/magireco-$role.service"
  cprint cyan noprefix ""

  cprint yellow noprefix "服务管理命令:"
  cprint white  noprefix nonl "  systemctl start   "
  cprint yellow noprefix             "magireco-$role.service"
  cprint white  noprefix nonl "  systemctl stop    "
  cprint yellow noprefix             "magireco-$role.service"
  cprint white  noprefix nonl "  systemctl restart "
  cprint yellow noprefix             "magireco-$role.service"
  cprint white  noprefix nonl "  systemctl status  "
  cprint yellow noprefix             "magireco-$role.service"
  cprint white  noprefix nonl "  journalctl -u "
  cprint yellow noprefix             "magireco-$role.service -f"
  cprint cyan noprefix ""

  case "$role" in
    panel)
      cprint yellow noprefix "下一步:"
      cprint white  noprefix "  浏览器打开 http://$ip_address:8090/  进入 WordPress 式安装向导"
      cprint white  noprefix "  装完后面板根路径即托管登录/注册/管理后台前端(前端跨域直连业务节点)"
      cprint white  noprefix "  第 4 步若勾'本机也装业务节点',请先 --role node-business 装上"
      ;;
    node-business)
      cprint yellow noprefix "下一步任选其一:"
      cprint white  noprefix "  A. 面板向导:在 /install 第 4 步勾'本机也装业务节点',向导回写 CNV_DB_URL 后 start"
      cprint white  noprefix "  B. 独立部署:手填 $etc_root/node-business.env 的 CNV_DB_URL,再"
      cprint white  noprefix "     systemctl start magireco-node-business"
      cprint white  noprefix "  ⚠ CNV_PANEL_PUBLIC_URL 已按安装时输入写入;若当时留空,补填到"
      cprint white  noprefix "    node-business.env 后 systemctl restart 即生效(空=单机自带前端)"
      ;;
    node-edge)
      cprint yellow noprefix "下一步:"
      cprint white  noprefix "  1) 编辑 $etc_root/node-edge.env 确认 CNV_PUBLIC_URL / CNV_PRIMARY_RES_DIR"
      cprint white  noprefix "  2) systemctl start magireco-node-edge"
      cprint white  noprefix "  3) 首次启动会在日志里打印节点密钥 —— journalctl -u magireco-node-edge"
      cprint white  noprefix "     复制到面板节点注册表完成配对"
      ;;
  esac
  cprint cyan noprefix ""

  cprint yellow noprefix "官方文档:"
  # GitHub Pages 把 owner 小写
  local _owner_lc
  _owner_lc="$(printf '%s' "${repo%/*}" | tr '[:upper:]' '[:lower:]')"
  cprint white  noprefix "  https://${_owner_lc}.github.io/${repo#*/}/"
  cprint cyan noprefix ""
  cprint green noprefix "祝你部署顺利,Mami 也会为你祈祷的。"
  echo ""
}


### --------------- 主入口 --------------- ###

main() {
  trap 'cprint red bold "发生意外错误。"; exit 99' ERR

  safe_run detect_terminal_capabilities "检测终端能力失败"
  print_banner
  safe_run check_root              "脚本必须以 root 身份运行"
  safe_run detect_arch             "架构检测失败"
  safe_run check_required_commands "必备系统命令缺失"
  safe_run parse_args              "命令行参数解析失败" "$@"

  case "$role" in
    panel)         safe_run install_role_panel         "面板安装失败" ;;
    node-business) safe_run install_role_node_business "业务节点安装失败" ;;
    node-edge)     safe_run install_role_node_edge     "边缘节点安装失败" ;;
  esac

  safe_run print_install_result "结果展示失败"
}

main "$@"
