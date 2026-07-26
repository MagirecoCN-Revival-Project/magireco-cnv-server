# 反向代理与域名

生产环境通常把服务端放在 Nginx / Caddy 等前置网关后面,由网关终结 TLS。本页给出域名规划与反代配置示例。

## 域名规划建议

| 域名 | 反代到 | 用途 |
|---|---|---|
| `api.magi-reco.top` | 主节点 `:8080`(整站) | 客户端握手、账号、管理后台、用户中心 |
| `captcha.magireco.top` | 主节点 `:8080/api/*` | 人机验证(只需 challenge/redeem) |
| `node-{N}.example.com` | 对应副节点 `:8080` | 就近资源下载(仅 `/res/*` 实际命中本地) |

把验证码拆到独立域名是为了让客户端的 WebView 验证页与主 API 隔离;不拆也能工作(主节点本身就有 `/api/*`)。

## ⚠️ 关键:配好 trust proxy

放在反代后面,**最容易踩的坑**是来源 IP。如果不告诉服务端"我信任前置网关的转发头",它会:

- 默认**只看 TCP 对端** → 所有请求的 IP 都记成网关 IP。
- 后果:限流变成全局共享一个桶(一个人触发,所有人被限)、审计日志 IP 全错、设备来源失真。

所以放在反代后必须设 `CNV_TRUST_PROXY`,值为**网关的来源网段**:

```bash
# 网关与服务端同机
export CNV_TRUST_PROXY='loopback'
# 网关在内网另一台
export CNV_TRUST_PROXY='10.0.0.0/8'
```

同时,前置网关**必须剥离客户端伪造的 `X-Forwarded-For`** 并自己重写,否则等于把伪造 IP 的能力开放给所有人。详见 [受信任代理](/security/trust-proxy)。

## Nginx 示例

```nginx
# 主节点(整站)
server {
    listen 443 ssl http2;
    server_name api.magi-reco.top;

    ssl_certificate     /etc/ssl/magireco.crt;
    ssl_certificate_key /etc/ssl/magireco.key;

    # 云存档可能较大,放宽请求体上限(服务端自身全站上限 8 MiB)
    client_max_body_size 16m;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;

        # 关键:重写转发头(先清掉客户端可能伪造的,再写真实值)
        proxy_set_header X-Real-IP        $remote_addr;
        proxy_set_header X-Forwarded-For  $remote_addr;   # 不要用 $proxy_add_x_forwarded_for 直接透传客户端伪造段
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header Host             $host;

        proxy_read_timeout 120s;
    }
}
```

对应服务端设 `CNV_TRUST_PROXY='loopback'`(网关在本机)。

::: tip 为什么用 `$remote_addr` 而非 `$proxy_add_x_forwarded_for`
`$proxy_add_x_forwarded_for` 会把客户端发来的 `X-Forwarded-For` 原样接在前面 —— 等于信任了客户端伪造的链。直接用 `$remote_addr` 让网关用自己看到的真实对端覆盖,服务端再取链首,才拿得到可信 IP。
:::

## Caddy 示例

Caddy 自动签发/续期证书,更省心:

```nginx
api.magi-reco.top {
    request_body {
        max_size 16MB
    }
    reverse_proxy 127.0.0.1:8080 {
        header_up X-Real-IP {remote_host}
        header_up X-Forwarded-For {remote_host}
        header_up X-Forwarded-Proto {scheme}
    }
}
```

## 进程内终结 TLS(不用网关)

如果你不想要前置网关,让服务端自己终结 TLS:

```bash
export CNV_TLS_CERT=/etc/ssl/magireco.crt
export CNV_TLS_KEY=/etc/ssl/magireco.key
# 此时不要设 CNV_TRUST_PROXY(没有可信前置,直连对端就是真实 IP)
```

这种模式下服务端会自动下发 HSTS 响应头(仅当 TLS 终结于本进程)。

## CSP 与第三方资源

管理后台前端用浏览器内 Babel + React via unpkg CDN,服务端已下发对应 CSP:

- `script-src` 放行了 `https://unpkg.com`(React/Babel)与 `'unsafe-eval'`(浏览器内转译需要)。
- `style-src` / `font-src` 放行了 Google Fonts。

如果你把前端资源自托管(不走 unpkg),可以相应收紧 CSP —— 但需要改 `internal/middleware/middleware.go` 里的 `SecurityHeaders`,属于代码改动。

## 健康检查

- 副节点有 `/healthz`(返回固定 `ok`),可直接给负载均衡探活。
- 主节点没有专门的 healthz,可用 `GET /`(返回前端页)或任意轻量端点探活。
