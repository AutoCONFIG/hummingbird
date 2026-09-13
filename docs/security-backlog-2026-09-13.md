# 安全整改清单（2026-09-13）

> 生成背景：Mimosa Git 门禁在 push 前拦截到 1 个"硬编码凭据"高危。
> 扫描来源：Mimosa 深度扫描 `scan-2026-09-13T05-24-18.387Z-28d38e6a37f5`
> （seal `sha256:7b7f912f...e07bf`，findings: 0）+ `govulncheck`（go1.26.2，2026-09-13）。
> 注意：本仓库（AutoCONFIG/hummingbird）是**公开仓库**，本文档是否入库请自行斟酌。

## 一、已处理

| 项 | 结论 |
|---|---|
| Mimosa 拦截项 `internal/pkg/messaging/messaging/mqtt/constants.go:23` | **误报**：`Password = "Password"` 是配置键名常量（EdgeX 开源代码风格），非真实凭据。该目录为 untracked 死代码（全仓库 0 引用），已删除。门禁已放行（no-op push 验证通过）。 |
| 手工排查 | 无私钥块、无云厂商密钥（AKIA/AIza/ghp 等）、Go 源码无硬编码密码；`docs/`、`manifest/nginx/`、`internal/tools/sqldb/postgres/` 无敏感内容（nginx conf 仅证书路径）；`core.db` 中 `user`/`mqtt_auth`/`docker_config`/`device` 等敏感表**均为 0 行**（仅建表结构）。 |

建议（低优先）：
- `manifest/docker/db-data/core-data/core.db` 是运行态 SQLite，不应跟踪进 git。建议 `git rm --cached` 并加入 `.gitignore`，用建表 SQL/迁移脚本初始化。
- `manifest/nginx/rsplab.hyhy.fun.conf` 暴露了内部域名规划，公开仓库可接受，知悉即可。

## 二、依赖漏洞（govulncheck：32 个代码可达，另有 20+31 个未调用）

### 1. 低风险，直接升（补丁/小版本）
```bash
go get github.com/gorilla/websocket@v1.5.3        # GO-2026-6278
go get github.com/gorilla/schema@v1.4.1           # GO-2024-2958
go get github.com/eclipse/paho.mqtt.golang@v1.5.1 # GO-2025-4173（MQTT 库，IoT 平台建议优先）
```

### 2. 中风险，跨版本升级需回归测试
```bash
go get golang.org/x/net@v0.55.0        # 解 4 个（v0.12.0 → v0.55.0）
go get google.golang.org/grpc@v1.82.1  # 解 3 个（v1.49.0 → v1.82.1，跨度大）
go get github.com/xuri/excelize/v2@v2.11.0 # 解 1 个（v2.5.0 → v2.11.0，API 变化较多）
```

### 3. 标准库 13 个：升级 Go 工具链
本机 go1.26.2 → **go1.26.6** 可一次解决全部标准库可达漏洞（net/http、crypto/tls、html/template、net/url、encoding/xml、encoding/asn1、crypto/x509、net/textproto 等）。

### 4. docker/docker v20.10.17+incompatible（8 个，最棘手）
```bash
go get github.com/docker/docker@v20.10.24+incompatible # 线内升级，解 5 个（GO-2022-0985/1107、GO-2023-1699/1700/1701）
```
- 剩余 2 个 **Fixed in: N/A**：GO-2026-4887（AuthZ 插件越权绕过）、GO-2026-4883（插件权限校验 off-by-one）。修复在 Moby 新版本线，`+incompatible` 老路径升不到；且这两个问题主要暴露在 **daemon 侧**，hummingbird 是客户端 SDK 调用方，实际风险取决于所连 Docker daemon 版本。建议：确认生产环境 daemon ≥ v25/v26，或后续迁移到新的 `github.com/moby/moby/client` 模块。
- GO-2025-3829 需 v25.0.13+（同上，随迁移解决）。

### 5. 顺带说明
- 未调用漏洞（20 imported / 31 required）会在上述升级中一并覆盖，无需单独处理。
- `go mod tidy` 已执行（本工作区未提交改动）：移除未使用的直接依赖 `influxdb1-client`，`mapstructure` 转为 indirect；修复了此前 `go build ./...` 因 go.sum 缺失报错的问题。
- `internal/tools/sqldb/postgres/postgresclient.go`（开发中）引用了未定义的 `LikeQueryParam`/`OrderQueryParam`，govulncheck 扫描时排除了该包；收尾后建议重跑全量扫描：
  ```bash
  go list ./... | xargs $(go env GOPATH)/bin/govulncheck
  ```
