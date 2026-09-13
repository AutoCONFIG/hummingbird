# 智慧水产物联网平台落地方案（hummingbird 单服务器形态）· v2（双审修订版）

> 项目代号：**翠鸟（Kingfisher）** —— 与基础平台蜂鸟（Hummingbird）构成鸟类家族命名。代号仅用于文档与口头沟通：代码中项目名将集中在 `internal/pkg/constants/` 单一常量定义，docker-compose project name 同源引用，后续改名只动一处；对外品牌（小程序名称/Logo）在微信平台侧配置，与代码解耦。
>
> 状态：v2 修订稿（吸收评审 A/B 全部 P0/P1/P2 问题，双审通过）
> 日期：2026-09-12
> 分支：`feature/water-platform`
>
> **文档体系**（本文档为总计划，其余为专项契约/映射）：
> | 文档 | 内容 | 服务对象 |
> |---|---|---|
> | 本文 `water-platform-plan.md` | 架构、选型、里程碑 M1~M7、风险、演进 | 评审/实施总纲 |
> | `device-protocol.md` | 设备接入协议基线 v0.9（MQTT topic/报文/LWT/联调；**暂定，预期调整**） | 固件团队 |
> | `api-contract.md` | 小程序/APP API 契约基线 v0.9（端点/错误码/页面映射；**暂定，预期调整**） | 前端团队，双阶段契约基线 |
> | `function-modules-mapping.md` | FunctionGraph 四函数 → hummingbird 等价实现映射 | 甲方对齐/开发指引 |
>
> **接口变更预期管理**：两份契约文档当前为基线版（v0.9），接口未与硬件/前端团队对齐前允许破坏性变更（直接改文档递增修订号）；量产/上线后才进入"字段只增不删"纪律。大改的隔离面已在两份文档尾部写明——设备协议变化只触及 `mqttgateway` 解析层，API 变化只触及 `waterapp` 业务层，平台核心不感知。

---

## 1. 背景与定位

甲方需求（智慧水产养殖水质监测）：

- **监测对象**：温度（℃）、溶解氧（mg/L）、pH、浊度（NTU）、盐度（ppt）五项水质指标 + 设备状态（在线/电量/信号/最后上报时间）
- **规模**：初期 <100 台水质监测终端，后续扩展到数百台，单台 1~5 分钟上报一次
- **用户侧**：微信小程序/APP（6 页面：登录/首页汇总/池塘列表/实时监测/历史曲线/报警中心；历史曲线支持今天/7天/30天）
- **报警**：**阈值按池塘独立配置**（云端判定），两级通知（应用内报警中心 + 微信订阅消息）
- **组织模型**：用户 → 养殖场 → 池塘 → 设备 → 传感器，多租户隔离

整体策略：第一阶段可用华为云 serverless 快速验证业务（接入路径见 §11）；**本方案覆盖第二阶段——用 hummingbird 单服务器替代 IoTDA、FunctionGraph 和数据库全家桶**。设备端协议从第一天按 §5 实现，两阶段切换只改设备侧 MQTT 地址配置（见 §11 契约保障机制）。

## 2. 总体架构

一台服务器，docker-compose 跑 **6 个容器**（无 Redis——hummingbird 缓存层是未接线的预留代码，go.mod 无 redis 依赖）：

```
N个水质监测终端 ──MQTT──> mqtt-broker(58090)
                             │ 订阅（内置网关模块，本项目新增）
                             ▼
                      hummingbird-core ──eventbus/in──> ekuiper（告警规则引擎）
                       │      │       │
                       │      │       └── REST API(58081)/Web(3000)/gRPC(57081)
                       │      │              ├─ /api/v1/*        管理后台（运营方）
                       │      │              └─ /api/v1/app/*   小程序/APP（多租户）
                       │      │
             PostgreSQL(元数据) TDengine(时序)
                       ▲
        caddy/nginx(443 TLS) <── 微信小程序（强制 HTTPS+备案域名）
```

| 容器 | 镜像 | 端口 | 职责 |
|---|---|---|---|
| hummingbird-core | 本仓库构建 | 内网 3000/58081/57081 | 业务核心（API、内置 MQTT 网关、告警管理） |
| mqtt-broker | winc-link/mqtt-broker:2.6 | **宿主 58090（仅设备网段可达）** | 设备接入 |
| ekuiper | winc-link/ekuiper:1.10 | 仅内网 | 告警规则流计算 |
| postgres | postgres:16 | 仅内网 | 元数据（用户/产品/设备/告警规则/租户业务表） |
| tdengine | tdengine/tdengine:3.1.x（与 driver-go v3.5.0 对齐，M2 首日验证） | 仅内网 | 时序数据（传感器历史） |
| **reverse-proxy** | caddy 或 nginx | **宿主 80/443** | 终结 TLS：443→58081（小程序 API）、→3000（运营控制台）。**微信小程序生产强制 HTTPS+备案域名，域名与证书纳入交付清单，域名配置进小程序 request 合法域名** |

## 3. 技术选型（定案）

| 决策项 | 选择 | 关键理由 | 放弃项及原因 |
|---|---|---|---|
| 时序数据库 | **TDengine 3.1.x** | ① 性能优先：每设备子表模型与主访问模式（按设备查历史曲线/最新值）完全匹配，单节点百万点/秒、百亿行天花板覆盖商业化阶段；② 压缩率 10–30x（可零成本接入候选中最优）；③ **hummingbird 已完整实现全部 16 个 DataDBClient 方法，零开发**；④ 团队有实际维护经验 | TimescaleDB（需约1天新写客户端，规模天花板低于 TDengine）；VictoriaMetrics（标签模型与接口形状不合）；QuestDB/GreptimeDB（需新客户端，无性能增益）；InfluxDB（生态收缩）；IoTDB（JVM 重） |
| 元数据库 | **PostgreSQL 16** | GORM 抽象下新增 PG = 复制 mysql 驱动包 + switch 加分支（PG upsert 方言已现成）；空库 + AutoMigrate 自动建表 | MySQL（用户偏好 PG）；SQLite（仅保留作本地开发） |
| 设备接入 | **内置 MQTT 网关模块**（hummingbird-core 新增订阅模块） | 设备协议完全自定义、部署少一层、不依赖外部驱动仓库。**订阅直接复用 `tools/mqttclient.NewMQTTClient`（支持 SubTopics+consumeCallback，paho OnConnect 重连后自动重订阅），无需 paho 直写**。复用 `messageapp.ThingModelMsgReport` 单一入口，入库+告警全链路零改动 | 驱动容器模式（协议受驱动限制、多一层容器、驱动代码在外部仓库） |
| 告警引擎 | **保留 eKuiper** | 阈值规则（原始值/均值/最大/最小 × 1/5/15/30/60 分钟窗口）、生效时段、启停状态机现成可用；设备上下线告警（DEVICE_STATUS）现成支持 | 进程内自研评估（重复造轮子，无收益） |
| 微信通知 | 新增微信订阅消息渠道 | 仿照现有 notify 五渠道模式（dingding/feishu/qiyeweixin/sms/webapi） | — |
| 多租户 | **业务层隔离** | 租户过滤全部在小程序 API 层实现；hummingbird 核心表不加字段，管理后台保持运营方全局视角 | 核心表加 tenant_id 全链路改造（伤筋动骨） |

**关键架构依据**（已代码验证，含评审修正）：

- `messageapp.ThingModelMsgReport`（messageapp.go:107）是 **PROPERTY_REPORT/EVENT_REPORT 设备上报数据的唯一入口**（推 `eventbus/in` 给 eKuiper + `SaveDeviceThingModelData` 落库；服务执行结果落库存在旁路直调 `persistItf.SaveDeviceThingModelData`，不影响本链路）。内置网关把设备报文转成 `dtos.ThingModelMessage` 调它即可。
- 告警规则 = eKuiper 在 `eventbus/in` 流上的 SQL，依赖报文结构 `messageType=PROPERTY_REPORT`、`$.{code}.value`（dtos/alertrule.go `BuildEkuiperSql`）——网关报文必须组装为 `{"deviceId":...,"messageType":"PROPERTY_REPORT","data":{"{code}":{"value":..,"time":..}}}`。
- 设备上下线现状：依赖驱动 gRPC 上报，无 broker LWT 监听——内置网关需自行实现（见 §5/§8-M3）。

## 4. 容量与性能预算

按每台 7 个指标（5 水质 + 电量 + 信号）：

| 规模（@1min 周期） | 写入速率 | TDengine 年存储（压缩后） |
|---|---|---|
| 100 台 | ~12 行/秒 | ~1–3 GB |
| 500 台 | ~58 行/秒 | ~5–15 GB |
| 1000 台 | ~117 行/秒 | ~10–30 GB |

- 写入余量 3~4 个数量级；压缩+保留策略：原始数据 TTL 180 天，**日聚合数据永久保留（写入路径在 M2 落定：主选 TDengine 流计算聚合到 daily 超级表，降级为平台内每日 00:05 定时任务 DELETE+INSERT 幂等重算）**，磁盘占用渐近有界
- **真实规模瓶颈在 eKuiper**（非数据库）：告警规则按"每设备×每指标"建 eKuiper 规则，数百台 = 数百~上千条规则，且 `alertcentreapp/monitor.go` 每 5 秒轮询全部规则状态。M7 压测实测，缓解手段现成（拉长轮询间隔/规则合并）
- **重新评估阈值**：设备过万或上报到秒级时，回头复评存储与 eKuiper 架构

## 5. 设备协议 v1（自定义）

| 项 | 约定 |
|---|---|
| 上行 topic | `water/{deviceSn}/report` |
| 上行报文 | `{"temperature":27.5,"dissolved_oxygen":6.8,"ph":7.9,"turbidity":12.5,"salinity":28.6,"battery":3.9,"signal":20,"ts":1700000000000}` |
| 上报周期 | 1~5 分钟（可配） |
| 状态 topic | `water/{deviceSn}/status`，设备注册 **LWT 遗嘱** `{"status":"offline"}`（retained）；上线后首条 report 视为 online |
| 离线兜底 | 网关按"3× 上报周期"超时判定离线（防 LWT 丢失） |
| 认证 | 复用 `mqtt_auth` 三元组表（ClientId/UserName/Password），设备创建时生成 |
| 下行 | 本期不实现，预留 `water/{deviceSn}/cmd`（阈值在云端判定，设备端无需下行） |
| 语义约定 | `ts` 缺省时网关补服务器时间；数值字段全部 float/int；未注册 SN 只记日志不入库；**battery/signal/last_report_ts（服务端记录）在平台侧随上报更新**，经 §7 `/app/pond/:id/latest` 的 `device_status` 块暴露给小程序 |

## 6. 数据模型

**hummingbird 核心表（不改动）**：product、device、thing model、alert_rule（设备级）、alert_list、mqtt_auth、user（管理端）。

**新增业务表（PG，AutoMigrate + 迁移 SQL）**：

```
tenant(id, name, invite_code UNIQUE, status)
farm(id, tenant_id, name, location, created, modified)
pond(id, farm_id, tenant_id, name, area_m2, created, modified)
pond_device(id, pond_id, device_id)
wx_user(id, openid UNIQUE, unionid, nickname, avatar, tenant_id, role, status, created, modified)
wx_subscribe_record(id, user_id, template_id, quota, updated)
```

- `openid` 建唯一索引（防并发首登竞态；upsert 用 ON CONFLICT）
- 租户过滤链：`wx_user.tenant_id → farm → pond → pond_device → device_id`，集中在小程序 API 层实现
- 第一版租户内**不分权**（role 字段预留，全部可看）

## 7. API 设计

**响应契约**（沿用 `httphelper.CommonResponse`）：`{"success":true,"errorCode":0,"errorMsg":"","result":...}`；分页 `{"list":...,"total":...,"page":...,"pageSize":...}`。小程序只访问 `/api/v1/app/*`。

**小程序/APP API 组**（新中间件 appJWTAuth，独立签名密钥，与管理端 token 互不相认；claims 带 wx_user_id；access token 7 天，过期 401 → 小程序静默 `wx.login` 重新登录，无感续期）：

| 端点 | 说明 | 底层复用 |
|---|---|---|
| `POST /app/auth/login` | 微信 code → code2session → upsert wx_user（openid ON CONFLICT）→ 签发 token。**未绑定租户的用户**：登录成功但 result 返回 `{need_bind:true}`，后续业务接口返回错误码 `NeedTenantBind(403)` | 新增 tools/wechat 客户端 |
| `POST /app/auth/bind` | `{invite_code}` → 校验 tenant.invite_code → 写入 wx_user.tenant_id。租户进入的唯一入口（邀请码由管理端生成） | — |
| `GET /app/farms`、`GET /app/ponds`、`GET /app/pond/:id` | 租户→塘→绑定设备 | 新增 waterapp |
| `GET /app/pond/:id/latest` | 5 项水质当前值 + 告警状态 + **`device_status{online, battery, signal, last_report_ts}`** | persistApp 最新值查询 |
| `GET /app/pond/:id/history?code=&range=today\|7d\|30d` | 历史曲线；today/7d 读原始表，**30d 读日聚合表** | SearchDeviceThingModelHistoryPropertyData |
| `GET /app/alarms`、`PUT /app/alarms/:id/confirm` | 报警列表/确认，按租户设备集合过滤 | alertcentreapp |
| `GET /app/home/summary` | 在线/离线/报警数汇总 | — |
| `POST /app/subscribe/quota` | 小程序 `wx.requestSubscribeMessage` 成功后上报，配额 +1（见 M6 配额闭环） | — |
| 池塘阈值编辑（小程序端） | **二期**；第一版由运营方在管理后台按池塘配置（见 M4 映射层） | — |

**管理端新增**（挂现有 v1Auth 组）：farm/pond CRUD、设备-池塘绑定、**`POST /pond/:id/thresholds`（池塘级阈值，批量展开为设备级 alert_rule）**、wx_user 查询与租户邀请码管理。运营方用内置 Web 后台（:3000）管理产品/设备/告警规则。`login` 与 `subscribe/quota` 接口用 `internal/pkg/limit` 现有限流机制防刷。

## 8. 实施里程碑

### M1 PostgreSQL 元数据库支持（约 1–2 天）
1. `internal/pkg/constants/db.go` 加 `Postgres MetadataType = "postgres"`；go.mod 加 **`gorm.io/driver/postgres v1.3.10`（pin：v1.4+ 依赖 pgx/v5 需 go≥1.19，与 go 1.18 构建镜像不兼容；v1.3.10 依赖 pgx/v4 + gorm v1.23.x，完全兼容）**
2. 新建 `internal/tools/sqldb/postgres/`：gorm.go 换 `postgres.Open`；batch.go 启用现成 `constructPGSQL` + engineJudge 的 pg case；**common.go 反引号改双引号**。注：包内 `postgresclient.go`（接口定义文件）已存在，复用
3. 新建 `internal/hummingbird/core/infrastructure/postgres/`（20 文件）：package 改名 + client.go import 调整 + **启用 AutoMigrate 块并删除 4 个已废弃模型（RuleEngine/DataResource/Scene/SceneLog），实际 23 个**；**全部 infra 文件内嵌的反引号 SQL 改双引号；对 `sqlite.Xxx` 助手的引用（MakeLikeParams/BuildCommonCondition/CreateBatchSize 等 10+ 处）改指向 `sqldb/postgres`**
4. `bootstrap/database/database.go` 加 case；两份 configuration.toml 加 PG Dsn（强密码占位；保留 SQLite 配置供本地开发）
5. **验收**：空 PG 上 `docker compose up` → 首次 init-password 建管理员 → 登录 → 设备/产品 CRUD 全通

### M2 TDengine 启用验证（约 1–2 天）
1. constants 加 `TDengine`（已有）；configuration.toml 切 `Type='tdengine'`、Dsn `账号:强密码@ws(tdengine:6041)/hummingbird`
2. **建库步骤（评审 A 发现 CreateDatabase 未接线）**：TDengine 容器 init 脚本执行 `CREATE DATABASE hummingbird`（或在 bootstrap 首连时执行，二选一，M2 定）
3. **版本对齐**：go.mod `driver-go/v3 v3.5.0` ↔ TDengine 3.1.x 镜像，首日实测连通
4. 建库/超级表/子表/动态字段链路验证（创建产品→建超级表，创建设备→建子表，上报→入库，曲线查询→出数）；DataDBClient **16** 个方法全通过
5. TTL 保留策略（原始数据 180 天）+ **日聚合写入路径落地**（主选流计算→daily 超级表；降级进程内定时任务，二选一定案）
6. **验收**：模拟上报 → TDengine CLI 查数 → 平台历史 API 出数一致；日聚合表可查

### M3 内置 MQTT 设备网关（约 2–3 天，核心新增）
1. 新建 `internal/hummingbird/core/application/mqttgateway/`：**复用 `tools/mqttclient.NewMQTTClient`**（SubTopics=`water/+/report`、`water/+/status` + consumeCallback；paho OnConnect 自动重订阅）。如后续上 MQTT TLS，先修 `mqttclient.go:66-70` KeyLogWriter→os.Stdout 的调试残留
2. **dbclient 新增 `DeviceBySn(sn)`**（接口 + 两份 infra 实现）；SN 唯一性在设备创建时应用层校验；网关内存缓存 SN→设备（失效重建；重复 SN 后到者拒绝并告警日志）。不改动 device 核心表结构
3. 收到 report → 组装 `ThingModelMessage{Cid: deviceId, OpType: PROPERTY_REPORT, Data: {"data":{code:{value,time}}}}` → `messageApp.ThingModelMsgReport`；同步更新 battery/signal/last_report_ts（可存 localcache，不新增核心表字段）
4. 上下线：LWT/3×周期超时 → `DeviceStatusToMessageBus(DEVICE_STATUS)` + DeviceOnlineById/DeviceOfflineById（仿 cloudability.go:29-92）
5. 种子脚本：「水质监测终端」产品 + 7 个物模型属性（codes: temperature/dissolved_oxygen/ph/turbidity/salinity/battery/signal）
6. **验收**：mosquitto_pub 模拟单设备 → 数据入库、eKuiper 告警可配可触发、拔掉模拟器→离线告警触发；broker 三元组校验行为实测（风险 #1）

### M4 业务模型 + 多租户 + 池塘阈值映射（约 2–3 天）
1. §6 全部表 + dbclient + 管理端 CRUD（farm/pond/绑定/邀请码/wx_user 查询）
2. **池塘阈值映射层（甲方需求，非可选）**：`POST /pond/:id/thresholds` → 按 pond_device 展开批量创建/更新/删除设备级 alert_rule（规则命名 `pond:{pondId}:{code}`），同塘多设备规则一致性由该映射层保证
3. **验收**：管理端建租户/塘/邀请码/绑定设备/配阈值全流程；池塘阈值展开后 eKuiper 规则生效

### M5 小程序 API 组（约 3 天）
1. §7 全部端点 + appJWTAuth 中间件 + wechat code2session 客户端
2. 30d 曲线读日聚合表；租户过滤链全接口生效
3. **验收**：租户 A 的用户看不到租户 B 的任何数据（专项测试）；未绑定用户流程（need_bind → bind → 正常访问）；契约对照 `docs/api-contract.md`（见 §11）逐接口 diff

### M6 微信订阅消息（约 2 天）
1. `internal/tools/notify/wechat/`（stable access_token + subscribe/send）；constants.AlertWay 加 `WechatMini`；alertapp.go AddAlert 分发处加 case
2. **配额闭环**：一次性订阅模板每条告警消耗 1 次配额（发送成功扣 1；43101 未订阅错误清零该模板配额；其他发送失败保留配额待重试）；小程序 `wx.requestSubscribeMessage` 成功后 `POST /app/subscribe/quota` +1；若申请到长期订阅模板则免配额（配置区分 template_mode）
3. 模板 ID/appid/secret 进配置；前置条件：注册小程序 + 申请订阅消息模板（外部依赖，不阻塞开发）

### M7 集成验证与交付（约 3 天）
1. 模拟器：Go 脚本模拟 100~500 台按周期上报，实测：入库量、TDengine 磁盘增长、eKuiper 规则数与内存、monitor 轮询压力
2. 一键部署：全新环境 `docker compose up -d` 起全栈（含初始化顺序：PG 建表 → TDengine 建库 → 种子数据 → 改密）
3. **运维手册（评审 B 缺口）**：pg_dump 每日备份（保留 7 份）+ taosdump 每周备份，落 /backup 卷；平台自监控（磁盘水位/容器状态用平台自身 system metrics + alert_rule 告警到运营方 webhook）；全部容器 `restart: unless-stopped`；日志轮转（core 自带 lumberjack + compose json-file max-size）；升级回滚流程（镜像 tag 版本化；AutoMigrate 单向 → schema 变更只加不删、删列两阶段；回滚=回退镜像 tag）
4. 全量 `go build` + 关键路径 `go test`；**`docs/api-contract.md`（OpenAPI 3）定稿**交付小程序端

## 9. 安全约定（验收条件）

- **SQL**：所有查询参数绑定（占位符），禁止拼接/format 组装 SQL——包括 TDengine DDL/查询、PG 业务查询、模拟器脚本
- **凭据**：所有容器强密码（部署脚本生成），禁止 `root:taosdata`/PG 默认口令入生产配置；JWT/appJWT 独立签名密钥
- **网络**：PG/TDengine/Ekuiper 端口不映射宿主机（仅 compose 内网）；公网仅暴露 80/443（反代）与 58090（MQTT，限设备出口 IP 或走 8883/TLS——上 TLS 前先修 KeyLogWriter 残留）
- **API**：login/subscribe-quota 限流（internal/pkg/limit）；wx_user.openid 唯一索引防并发竞态；appJWT 7 天 + 静默重登

## 10. 风险与验证项

| # | 风险 | 应对 |
|---|---|---|
| 1 | winc-link mqtt-broker:2.6 是否强制校验 mqtt_auth 三元组 | M3 首日实测；不校验则文档化网络隔离要求，或换 mosquitto+插件 |
| 2 | driver-go v3.5.0 与 TDengine 镜像版本错配 | M2 首日对齐验证，锁定 3.1.x tag |
| 3 | TDengine 建库未接线（CreateDatabase 有实现无调用方） | M2 步骤 2 显式建库 |
| 4 | eKuiper 数百规则时的内存/轮询压力 | M7 压测实测；缓解：拉长 monitor 轮询间隔、规则合并 |
| 5 | PG AutoMigrate 与 mysqldump 模式差异 | 对照 init.sql 逐表核对 AutoMigrate 结果 |
| 6 | 微信订阅消息依赖用户主动订阅+模板审核（外部依赖） | 不阻塞开发；产品层引导订阅；短信兜底可选 |
| 7 | 微信 code2session 需要小程序 appid/secret | 前置申请；配置注入，代码先行 |
| 8 | 设备野外部署，MQTT 明文被扫描 | 安全约定：IP 白名单/8883/TLS；M3 实测三元组行为后定 |

## 11. 演进路线与契约保障

- **第一阶段接入路径（如实标注）**：推荐 Phase1 设备直连通用 MQTT broker（如 EMQX Cloud/自建）按 §5 协议上报，FunctionGraph 经 MQTT 触发器消费——设备端与第二阶段**完全一致**，切换仅改 broker 地址与三元组。若甲方坚持华为云 IoTDA，其 clientId 签名算法与 `$oc/...` topic 体系与本协议不同，切换需适配固件认证段（成本如实告知）
- **契约保障机制（三原则落地手段）**：第 0 天产出 `docs/api-contract.md`（OpenAPI 3，§7 全部端点）作为**双阶段唯一契约源**：第一阶段 FunctionGraph 与第二阶段 hummingbird 各自对照契约实现，M5 验收含契约 diff 比对——"小程序零修改"由机制保证而非口号
- **第三阶段触发点**（设备过万或秒级上报）：复评 TDengine 集群化 / VictoriaMetrics；eKuiper 规则合并或替换进程内评估；datadb 抽象保证存储层可替换
- **下行控制**（增氧机等）：预留 `water/{sn}/cmd` topic 与网关下行通道，作为独立增量
