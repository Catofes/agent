# HTTPS 反向代理部署

示例配置位于 `deploy/nginx.conf.example`，适用于 Nginx 在同一台机器上终止 HTTPS，并将请求转发到监听 `127.0.0.1:8080` 的课堂 Agent 进程。

## 上线步骤

1. 复制示例配置到 Nginx 的 `sites-available` 目录。
2. 替换域名和 TLS 证书路径。
3. 运行 `nginx -t` 确认语法，再 reload Nginx。
4. 服务端启用 `COOKIE_SECURE=true`，并仅监听回环地址：`LISTEN_ADDR=127.0.0.1:8080`。
5. 从 Pad 所在网络访问 HTTPS 域名，检查学生聊天、锁定广播、教师状态墙和大屏切换。

## 流式配置要点

- `proxy_buffering off`：禁止 Nginx 聚合 `/api/chat` 的 NDJSON 和三类 SSE 响应。
- `gzip off`：避免压缩缓冲将多个小 delta 合并后才发送。
- `proxy_read_timeout 300s`：允许长模型请求和 SSE 连接持续存活；应大于应用默认的 `LLM_TIMEOUT=180s`。如果现场约 60 秒固定断开，应检查实际生效的代理配置，而不是只调大应用超时。
- `X-Accel-Buffering: no`：代理与应用都显式标记禁用缓冲。
- 应用的流式测试会校验 `Cache-Control: no-transform` 和 `X-Accel-Buffering: no`；实际部署仍需在目标代理和局域网 Pad 上观察首段到达时间。

## 上线后检查

```bash
curl -i -N https://agent.example.edu/api/screen/events
```

响应应立即返回 `Content-Type: text/event-stream`、`Cache-Control: no-store, no-transform` 和 `X-Accel-Buffering: no`，随后保持连接。学生聊天接口需要登录 Cookie，建议在浏览器 Network 面板中确认多个 `text_delta` 在请求完成前逐步到达。
