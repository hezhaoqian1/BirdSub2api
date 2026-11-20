# 2.11.2+ 修复了「连接失败后重试时请求体已被关闭」的问题，Caddyfile 的重试兜底依赖它
FROM caddy:2.11.9-builder AS builder
RUN xcaddy build --with github.com/caddy-dns/cloudflare

FROM caddy:2.11.9
COPY --from=builder /usr/bin/caddy /usr/bin/caddy
COPY Caddyfile /etc/caddy/Caddyfile
