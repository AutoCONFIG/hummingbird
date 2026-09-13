# 翠鸟平台 · 部署与运维手册

> 配套：`docs/water-platform-plan.md`（总方案）、`docs/device-protocol.md`（设备协议）、`docs/api-contract.md`（小程序契约）
> 验证环境：单服务器 docker-compose（hummingbird-core + mqtt-broker + ekuiper + postgres + tdengine [+ caddy/nginx]）

## 1. 一键部署

```bash
git clone <repo> && cd hummingbird
# 首次构建镜像（或使用 registry 已推送版本）
make build

cd manifest/docker
docker compose up -d
```

容器清单与暴露面（安全约定 §9：除以下端口外全部仅内网）：

| 容器 | 宿主端口 | 说明 |
|---|---|---|
| hummingbird-core | 3000 / 58081 / 57081 | Web 控制台 / API / gRPC |
| mqtt-broker | 58090 | 设备接入（生产建议限设备出口 IP 或上 8883/TLS） |
| ekuiper | 127.0.0.1:9081 | 告警规则引擎 |
| postgres | 无 | 元数据（生产改强密码） |
| tdengine | 无 | 时序（6041 内网） |
| caddy/nginx（可选） | 80/443 | TLS 反代：443→58081（小程序 HTTPS 必须）+ 备案域名 |

## 2. 首次初始化顺序

1. `curl http://<host>:58081/api/v1/auth/initInfo` → `isInit:false`
2. `POST /api/v1/auth/init-password {"newPassword":"<强密码>"}` → 创建 admin（自动生成 JWT/OpenAPI 密钥）
3. `POST /api/v1/auth/login` 获取管理端 token（x-token 头）
4. 种子流程（可用 M3 验证脚本方式）：
   - `POST /product`（水质监测终端）→ `POST /thingmodel` × 7（temperature/dissolved_oxygen/ph/turbidity/salinity/battery/signal）→ `POST /product-release/:id`
   - `POST /tenants`（生成邀请码）→ `POST /farm` → `POST /pond` → `POST /pond/:id/devices`（绑定设备）→ `POST /device`（建设备，SN 即设备协议路由键）
   - `POST /pond/:id/thresholds`（池塘阈值 → 自动展开为设备级告警规则并启动 eKuiper）
5. 小程序端：`POST /api/v1/app/auth/login`（dev 模式未配 AppId 时 openid=dev_+code）→ `POST /app/auth/bind`（邀请码）→ 业务接口

## 3. 设备接入（docs/device-protocol.md）

- 设备侧配置：broker 地址（58090）+ 三元组（管理后台「设备详情 → MQTT 接入信息」查看）
- 上报验证：`kingfisher-sim -once -sn WATER001 -do 3.5`（模拟低氧触发告警）
- 未注册 SN 上报：仅告警日志不入库；SN 重复：后到拒绝

## 4. 备份

| 数据 | 方式 | 频率 |
|---|---|---|
| PostgreSQL（元数据+业务表） | `pg_dump -Fc hummingbird > /backup/pg-$(date +%F).dump`，保留 7 份 | 每日 |
| TDengine（时序） | `taosdump -D hummingbird -o /backup/td-$(date +%F)` | 每周 + 月底全量 |
| 配置 | configuration.toml + compose 入库 | 变更时 |

## 5. 自监控（平台自身）

- 磁盘水位：`GET /api/v1/metrics/system`（system_metrics）→ 配一条 eKuiper/告警规则推送 webhook 给运维群
- 容器状态：`restart: unless-stopped` + 外部探活（HTTP GET /api/v1/auth/initInfo）
- eKuiper 规则状态：`GET :9081/rules`（平台每 5s 自动同步告警规则启停）

## 6. 升级与回滚

- 镜像 tag 版本化；回滚 = 回退镜像 tag
- 元数据 schema 变更走 AutoMigrate（单向）：**只加列不删列**；删列两阶段（先停用再下版删除）
- TDengine schema：物模型加列由平台自动 `ALTER STABLE ADD COLUMN`；类型变更受 TDengine 限制（NCHAR 只可加长）
- 升级前备份 §4 全部数据

## 7. 关键配置项（configuration.toml）

```toml
[ApplicationSettings]
EkuiperAlertCallbackUrl = '...'   # ekuiper 告警回调覆盖（宿主机部署/跨网），空=服务名约定
WeChatAppId / WeChatAppSecret    # 微信小程序（空=登录 dev 模式）
WeChatTemplateId                 # 订阅消息模板 ID
WeChatTemplateMode               # oneoff（默认）/ longterm
WeChatNotifyPage                 # 订阅消息跳转页

[Databases.Metadata.Primary]     # postgres/mysql/sqlite
[Databases.Data.Primary]         # tdengine/leveldb/tstorage
```

## 8. 契约文档

- 设备端 / 小程序端接口变更：更新对应 `docs/*.md` 并递增修订号（上线前允许破坏性变更）
- 本文档对应里程碑：M1~M7（feature/water-platform 分支）
