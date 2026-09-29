# 浏览器实测记录

网页客户端在浏览器和浏览器扩展里实际遇到的问题。本文只记录现象、原因和处理办法；网页和 server 据此怎么设计见 [architecture.md](architecture.md) 第 1、10 节，还没验证的列在 [plan.md](plan.md) 的「待确认」。

## 1. AdGuard 拦截发往 `*.*.top` 的跨站 WebSocket

验证环境：Chrome，AdGuard AdBlocker 扩展 5.5.1.8（MV3），开启「AdGuard 中文过滤器」，2026-09-29。

**现象**：网页能登录、能拉会话列表，但实时连接一直是「未连接」。控制台只有一句 `WebSocket connection to 'wss://ra.remote-agent.top/v1/stream' failed:`，没有原因，close code 是 1006。

**原因**：AdGuard 中文过滤器（EasyList China + AdGuard Chinese filter，扩展里的 `ruleset_224`）有一条规则，拦截所有**第三方**页面发往 `wss://*.*.top/` 的请求，本意是挡广告站的 WebSocket。三级的 `.top` 域名，比如 `ra.remote-agent.top`，只要是从别的站点发起的 WebSocket 就会命中。普通的 fetch 请求不受影响，所以登录和拉列表都正常。

**怎么判断是它**：

| 发起的页面 → 目标 | 结果 |
|---|---|
| `http://localhost:4173` → `wss://ra.remote-agent.top` | 3ms 就失败，请求没有离开浏览器（代理和 Caddy 都没有记录） |
| `http://localhost:4173` → `wss://vps.remote-agent.top` | 同样失败 |
| `http://localhost:4173` → `wss://echo.websocket.org` | 正常（`.org` 不命中） |
| `https://ra.remote-agent.top` 页面 → `wss://ra.remote-agent.top` | 正常（同站，不算第三方） |
| 加了下面的放行规则后，localhost → `ra.remote-agent.top` | 正常，177ms 连上；没放行的 `vps.remote-agent.top` 仍然失败 |
| `https://remote-agent.top` → `wss://vps.remote-agent.top`（没有放行） | 正常，341ms 连上（同站） |

失败只用几毫秒、没有任何网络记录，是被扩展拦下的典型特征；网络问题通常要等到超时。

**处理**：

- 在 AdGuard 的「用户规则」里放行 server 的 WebSocket：`@@||ra.remote-agent.top^$websocket`（换成自己的 server 域名）
- 或者开发时让 vite 转发请求，网页只连自己的 origin：`RA_PROXY=https://<server> pnpm preview`，构建时加 `VITE_SAME_ORIGIN_API=1`
- 正式部署时，网页和 server 放在同一个注册域名下（如 `remote-agent.top` 和 `ra.remote-agent.top`），AdGuard 把它们当成同站请求，不会命中这条规则。已验证（同一环境，2026-09-29）：从 `https://remote-agent.top` 页面连没有放行的 `wss://vps.remote-agent.top`，341ms 连上；同样的连接从 localhost 发起，1ms 就被拦。网页不要直接用 `*.pages.dev` 这类别的域名访问

其他拦截扩展（uBlock Origin 等）用的中文过滤列表里可能也有类似规则，未验证。遇到「fetch 正常、WebSocket 几毫秒就失败」时，先在无痕窗口或关掉扩展后试一次。
