# 翠鸟平台 · Rust 重写计划（v3）

> 状态：**已归档（2026-09-13）——决策回归 hummingbird 增量路线（water-platform-plan v2 的 M1~M7）**。本文档保留作为未来 W11+ 平台化路线的参考（进程内告警引擎规格、EMQX HTTP 认证、平台化迭代顺序仍可复用）。
> 日期：2026-09-13
> 决策记录：放弃 hummingbird 增量扩展路线，改为 Rust 全量重写平台（终态对标 hummingbird 全部能力）；告警采用进程内评估（弃 eKuiper）；资源 1 人，预期 8~10 周。
> 契约基线不变：`device-protocol.md`（设备协议 v0.9）与 `api-contract.md`（小程序 API v0.9）继续作为双端唯一契约，重写只是更换实现载体。

---

## 1. 人力数学（必须先对齐）

| 事实 | 数字 |
|---|---|
| 平台全量重写（对标 hummingbird：驱动体系/模板库/开放平台/完整管理台） | 3~6 人月 |
| 本次可用资源 | 1 人 × 8~10 周 ≈ 2~2.5 人月 |
| **结论** | **8~10 周内无法交付"全量平台"**，只能交付"平台地基 + 翠鸟产品全功能" |

因此本计划采用**分期终态**策略：
- **本期（W1~W10）**：Rust 平台骨架 + 翠鸟产品所需全部能力（设备接入/存储/告警/业务层/小程序 API/极简管理端）——产品可联调可演示
- **后续迭代（W11+，按季）**：驱动体系（gRPC + Docker DMI）、物模型模板库、开放平台 API、WebSocket 推送、完整管理台 UI、i18n——逐步逼近 hummingbird 全量终态
- 若要求一次性全量：需加人，或接受 3~6 个月周期

## 2. 架构（4+1 容器）

```
水质监测终端 ──MQTT──> emqx(58090/8883) ──> kingfisher-core(Rust, 单二进制)
                                              │  ├─ ingest     设备订阅+SN路由+标准化
                                              │  ├─ alert      进程内规则评估（替代 eKuiper）
                                              │  ├─ storage    sqlx(PG元数据) + taos(TDengine时序)
                                              │  ├─ biz        租户/场/塘/阈值映射
                                              │  └─ api        axum: /admin/* + /api/v1/app/*
                        caddy(443 TLS) <──────┘
微信小程序 ──HTTPS──> caddy ──> kingfisher-core
```

| 容器 | 镜像 | 变化 vs 旧方案 |
|---|---|---|
| kingfisher-core | 本仓库构建（rust:1.8x 多阶段） | 替代 hummingbird-core + ekuiper 两个容器 |
| **emqx 5.x** | emqx/emqx:5.x | **换掉 winc-link broker：启用 HTTP 认证插件对接平台 `/internal/mqtt/auth`**——一次性解决旧栈"broker 是否校验三元组"的未决风险 |
| postgres 16 | 同前 | 元数据（含业务表） |
| tdengine 3.1.x | 同前 | 时序 |
| caddy | 同前 | TLS 反代 |

## 3. Rust 技术栈（锁定清单）

| 用途 | Crate | 备注 |
|---|---|---|
| 运行时 | tokio（full） | 单二进制内多任务 |
| HTTP | axum + tower-http（trace/limit/cors） | /admin + /api/v1/app + /internal（mqtt auth 回调） |
| 元数据 | sqlx（postgres, migrations, 编译期校验） | PG16 |
| 时序 | taos（WebSocket 连接池） | TDengine 3.1.x，W2 首日验证版本配对 |
| MQTT | rumqttc | 订阅 water/+/report、water/+/status；自动重连 |
| 序列化/校验 | serde / serde_json / validator | — |
| 认证 | jsonwebtoken | admin JWT + appJWT 双密钥 |
| 通知外呼 | reqwest | 微信/钉钉/飞书/企微/短信/webapi |
| 可观测 | tracing + tracing-subscriber | 结构化日志 |
| 工程 | thiserror / anyhow / config(figment) | 错误分层；配置注入 |

工程形态：**单 crate 起步、模块内分层**（1 人不为过早的 workspace 买单），目录即模块：`src/{ingest,alert,storage,biz,api,notify,common}`；规模上来后再拆 workspace。

## 4. 告警引擎规格（进程内，替代 eKuiper）

以现有 `BuildEkuiperSql` 语义为规格照抄（保证行为等价、告警规则可迁移）：

- **规则模型**：`规则 = 设备 × 指标code × 判定条件 × 聚合类型 × 窗口`。聚合类型 original/avg/max/min/sum；窗口 1/5/15/30/60 分钟（聚合型必填，original 不开窗）；判定条件 `> >= < <= = != 数值`；另含报警级别、生效时段（start/end）、静默时长、通知配置（多渠道）
- **评估实现**：每 (device, code) 维护环形缓冲（ts, value）；report 到达时遍历该设备激活规则——original 直接判定；聚合型按滚动窗口计算后判定（HIVING 语义 = 窗口闭合时条件命中则触发）
- **防重复**：静默期内同规则不重复触发；`alert_list` 记录（当前值/阈值/触发时间/级别），状态机 pending → treated（带处理意见）/ ignored
- **生效时段**：触发前检查，窗口外不评估不告警
- **状态恢复（重写版特有，必须做）**：进程重启后，按每条激活规则的窗口时长从 TDengine 回放最近数据重建窗口缓冲——旧方案 eKuiper 自带此能力，进程内引擎要自己实现，列入 W4 验收
- **迁移**：hummingbird 的 alert_rule 表结构兼容读取（迁移脚本直接转新库）

## 5. 里程碑（1 人 × 10 周，含缓冲）

| 周 | 里程碑 | 交付/验收 |
|---|---|---|
| W1 | 地基：cargo 工程、axum 骨架、配置/tracing/错误码体系、sqlx+PG 迁移（核心元数据表）、admin JWT 登录、响应包络=api-contract | 空库起服务，登录/CRUD 冒烟 |
| W2 | 设备接入：rumqttc 订阅（协议= device-protocol.md）、SN 路由、emqx HTTP 认证对接、TDengine 写入（超级表/子表/动态列）、LWT+超时上下线 | 设备模拟器上报 → TDengine 出数；拔线 → 离线 |
| W3 | 物模型 + 查询：产品/物模型 CRUD（属性/事件/服务基础版）、实时/历史查询 API（today/7d 原始，30d 日聚合）、日聚合写入路径（定时任务幂等重算） | 契约 §3.3~3.5 可跑 |
| W4 | 告警引擎：规则 CRUD + 进程内评估器（窗口/静默/生效时段）+ 重启状态回放 | 规则命中→落库；重启后窗口不丢 |
| W5 | 通知渠道：webapi/钉钉/飞书/企微/短信 + **微信订阅消息（配额闭环）** + 分发与静默 | 端到端：DO<4 → 报警记录 → 微信收到订阅消息 |
| W6 | 业务层：tenant/farm/pond/pond_device/wx_user 表、邀请码绑定、appJWT、池塘阈值映射层（展开为设备规则） | 管理端建租户→绑设备→配阈值全流程 |
| W7 | 小程序 API：实现 api-contract.md 全部端点（含 30d 聚合读取、device_status、home/summary）+ 极简管理页（设备/塘/阈值/告警 4~5 页，轻量前端或 rust-embed 静态页） | 契约逐接口 diff 通过 |
| W8 | 硬化：限流、参数校验全量、SQL 全参数绑定审查、错误路径、多租户越权专项测试 | 安全约定清单逐条过 |
| W9 | 集成验证：设备模拟器压测（100/500 台）、告警引擎规模测试（数百规则内存/CPU）、api-contract 定稿 v1.0 | 压测报告 |
| W10 | 交付：docker-compose 一键起全栈（5 容器）、部署/运维手册（备份/自监控/重启策略/升级回滚）、代码与文档收尾 | 全新机器 30 分钟内从零到可演示 |

**W11+ 平台化路线（终态补齐，按季迭代）**：驱动体系（gRPC 协议 + Docker DMI 驱动容器管理）→ 物模型模板库/品类库 → 开放平台 API（/v1.0/openapi + refresh token）→ WebSocket 实时推送 → 完整管理台 → i18n/多端 → 对标 hummingbird 全量。

## 6. 复用资产（重写不重做）

- `docs/device-protocol.md`：设备协议基线（固件零改动）
- `docs/api-contract.md`：小程序契约基线（前端零改动；W7 逐接口 diff 验收）
- `docs/function-modules-mapping.md`：四个功能模块的功能规格（ingest/alert/status/api 的职责定义原样成立，实现载体换成 Rust 模块）
- 容量预算与 eKuiper 规模分析（§4 容量表照用；eKuiper 瓶颈随进程内评估消失）
- hummingbird 的 `BuildEkuiperSql` 语义 = 告警引擎规格来源；alert_rule/alert_list 表结构 = 迁移蓝本

## 7. 风险

| # | 风险 | 应对 |
|---|---|---|
| 1 | **单人单点**：1 人 10 周，任何生病/假期直接顺延 | 里程碑按周交付可裁剪；W10 是缓冲；文档随周更新 |
| 2 | 告警引擎语义踩坑（窗口/静默/回放等价性） | 规格唯一来源锁定 BuildEkuiperSql + 旧告警规则数据做对照回放测试 |
| 3 | taos crate 与 TDengine 3.1.x 配对不成熟 | W2 首日验证；备选：经 REST/WebSocket 自封装或降级 TimescaleDB（datadb 抽象保留切换点） |
| 4 | 管理台缺口（旧 React 控制台不可复用） | 本期只做极简管理页；完整管理台在 W11+ 迭代，期间运营靠页面+API |
| 5 | Rust 熟练度影响节奏 | 栈全部主流 crate；W1 先跑通全链路骨架再铺量，问题前暴露 |
| 6 | hummingbird 存量部署的迁移 | 本期为新产品线，hummingbird 归档不删；旧 alert_rule 写迁移脚本（W6） |

## 8. 与旧方案（water-platform-plan v2）的关系

- 旧计划的 §1/§4/§5/§6/§7/§9/§11（需求/容量/协议/数据模型/API/安全/演进）**继续有效**，被本文档引用而非替代
- 旧计划的实现载体（M1~M7 的 Go 增量）**作废**，由本文 W1~W10 取代
- `feature/water-platform` 分支上已产生的 PG 驱动文件不再推进，保留归档
