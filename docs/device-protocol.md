# 翠鸟平台 · 设备接入协议 v1（MQTT）

> 读者：水质监测终端固件开发
> 状态：**基线版 v0.9（暂定）——接口仍在定义中，预期有较大调整**。与 `docs/api-contract.md` 同为双阶段契约基线：第一阶段（华为云 FunctionGraph 消费）与第二阶段（hummingbird 内置网关）均按本协议实现。
> **变更策略（两阶段）**：
> ① 当前阶段（未量产、未烧录固件、未对接前端）：**允许破坏性变更**——直接改本文档并在标题递增修订号（v0.x），通知硬件/后端即可；
> ② 量产联调后：字段只增不删、语义不变更，破坏性变更新开版本号并走设备固件升级。
>
> **开放项（预期会变的点，见 §9）**：上报周期定档、指标字段集与量程、signal 单位口径、下行 cmd 协议等。

---

## 1. 接入模型

```
水质监测终端 ──MQTT(TCP 58090 / TLS 8883 预留)──> 平台 MQTT Broker ──> 平台内置网关 ──> 入库/告警
```

- 每台终端使用**独立三元组**认证：`ClientId` / `UserName` / `Password`，由平台在设备创建时生成，运营方在管理后台查看后烧录进设备（或产线工具写入）。
- 终端**只发布**自己 SN 对应的 topic（`water/{deviceSn}/...`），不得订阅/发布他人 topic；下行通道 v1 预留不实现。
- 协议：MQTT 3.1.1（兼容 5.0）；报文为 UTF-8 JSON。

## 2. 连接规范

| 参数 | 要求 | 说明 |
|---|---|---|
| Broker 地址/端口 | 部署时下发（默认 58090） | 随三元组一并写入设备配置 |
| ClientId | 平台分配，全局唯一 | 与三元组表一致 |
| UserName / Password | 平台分配 | 同上 |
| KeepAlive | **120 秒**（必须 < 上报周期） | 心跳由 MQTT 协议层维持，无需业务心跳 |
| CleanSession | **false**（推荐） | 为 v2 下行命令的离线消息预留 |
| QoS | 上报/状态均为 **1**（at least once） | 平台按时间戳取值，重复投递无害 |
| 断线重连 | 指数退避（建议 30s 起，上限 5min） | 重连成功后无需重放历史数据，直接按周期续报 |
| 遗嘱（LWT） | **必须设置**，见 §5 | 平台依赖 LWT 判定离线 |

## 3. Topic 规范

| Topic | 方向 | QoS | Retain | 用途 |
|---|---|---|---|---|
| `water/{deviceSn}/report` | 设备 → 平台 | 1 | 否 | 水质数据上报（周期性） |
| `water/{deviceSn}/status` | 设备 → 平台 | 1 | **是（LWT 消息）** | 上下线状态 |
| `water/{deviceSn}/cmd` | 平台 → 设备 | 1 | 否 | **v1 不实现，预留**（v2 下行：配置/控制） |
| `water/{deviceSn}/cmd_ack` | 设备 → 平台 | 1 | 否 | **v1 不实现，预留** |

## 4. 上行报文：`water/{deviceSn}/report`

### 4.1 字段定义

| 字段 | 类型 | 单位 | 必填 | 有效范围 | 物模型 code |
|---|---|---|---|---|---|
| `temperature` | float | ℃ | 是 | -20 ~ 80 | `temperature` |
| `dissolved_oxygen` | float | mg/L | 是 | 0 ~ 20 | `dissolved_oxygen` |
| `ph` | float | pH | 是 | 0 ~ 14 | `ph` |
| `turbidity` | float | NTU | 是 | 0 ~ 1000 | `turbidity` |
| `salinity` | float | ppt | 是 | 0 ~ 50 | `salinity` |
| `battery` | float | V | 否 | 0 ~ 24 | `battery` |
| `signal` | int | dBm | 否 | -100 ~ 0 | `signal` |
| `ts` | int64 | — | 否 | Unix 毫秒时间戳 | — |

- 数值建议保留 2 位小数；浮点必须带小数点（如 `27.5`，不可传 `"27.5"` 字符串）。
- **`ts` 缺省时平台以服务器接收时间补齐**（固件无 RTC 时可省略，但推荐带上）。
- `battery`/`signal` 可选；不上报时平台保留上一次值。
- 单帧报文 ≤ 1KB。

### 4.2 示例

```json
{
  "temperature": 27.5,
  "dissolved_oxygen": 6.8,
  "ph": 7.9,
  "turbidity": 12.5,
  "salinity": 28.6,
  "battery": 3.9,
  "signal": -67,
  "ts": 1731500000000
}
```

### 4.3 上报周期

- 默认 **300 秒（5 分钟）**，可在 60 ~ 300 秒间配置（v1 由固件配置项决定，v2 支持云端下发调整）。
- 建议整分钟对齐，便于平台聚合统计。

## 5. 状态上报与遗嘱（LWT）

### 5.1 遗嘱（必须）

连接时注册 LWT，用于异常掉电/断网时平台感知离线：

- Topic：`water/{deviceSn}/status`
- Payload：`{"status":"offline","ts":1731500000000}`
- QoS：1，**Retain：true**

paho（C/Python/嵌入式库通用参数）示意：

```
will_topic   = "water/DEV001/status"
will_payload = '{"status":"offline","ts":...}'
will_qos     = 1
will_retain  = true
```

### 5.2 主动上线通知（推荐，可选）

连接建立后立即发布（QoS 1，Retain **false**，覆盖残留的 retained offline）：

```json
{"status":"online","ts":1731500000000}
```

### 5.3 平台侧判定语义（固件无需实现，供联调理解）

- 在线：收到 `online` 状态或该 SN 的任意一帧 report；
- 离线：收到 LWT `offline`，或 **3× 上报周期**内无任何消息（超时兜底，防遗嘱丢失）；
- 状态变化会触发平台的"设备离线告警"规则与小程序端设备状态展示。

## 6. 平台侧行为约定（联调对齐用）

| 场景 | 平台行为 |
|---|---|
| 未注册的 SN 上报 | 仅记安全日志，不入库、不产生告警 |
| JSON 非法 / 字段类型错误 | 丢弃该帧并记日志，不影响后续帧 |
| 同一 SN 重复接入（第二连接） | 平台按 broker 会话规则处理；固件应避免双连接 |
| report 命中告警规则 | 数据入库 + 进入 eKuiper 规则引擎，告警经小程序报警中心 + 微信订阅消息两级通知 |

## 7. 联调自测

```bash
# 正常上报（替换三元组与 SN）
mosquitto_pub -h <broker地址> -p 58090 \
  -i <ClientId> -u <UserName> -P <Password> \
  -t "water/DEV001/report" -q 1 \
  -m '{"temperature":27.5,"dissolved_oxygen":6.8,"ph":7.9,"turbidity":12.5,"salinity":28.6,"battery":3.9,"signal":-67}'

# 模拟遗嘱（验证离线判定）：直接向 status 发 retained offline
mosquitto_pub -h <broker地址> -p 58090 \
  -i <ClientId> -u <UserName> -P <Password> \
  -t "water/DEV001/status" -q 1 -r \
  -m '{"status":"offline","ts":1731500000000}'
```

认证被拒时（返回 CONNACK 非零），检查三元组；平台侧三元组在管理后台「设备详情 → MQTT 接入信息」查看。

## 8. v1 明确不做（预留）

- 下行命令 / 配置下发（`cmd`/`cmd_ack` topic 已预留，协议 v2 定义）
- 固件 OTA（走驱动/终端自身通道）
- 批量补传（断网期间数据缓存重传；当前平台语义为"实时值 + 告警"，断网期数据缺失可接受）

## 9. 开放项（预期会变的点）

| # | 开放项 | 当前基线 | 待定因素 |
|---|---|---|---|
| 1 | 上报周期 | 300s 默认，60~300s 可配 | 甲方对实时性要求；告警联动是否需要秒级突发上报 |
| 2 | 指标字段集 | 5 水质 + battery/signal | 传感器最终选型与量程（浊度上限、盐度 ppt vs PSU）；是否增加水质等级等派生字段 |
| 3 | signal 口径 | dBm（-100~0） | 模组厂商上报原始值（CSQ 档位需换算） |
| 4 | ts 约定 | Unix 毫秒、缺省服务端补齐 | 固件是否有 RTC |
| 5 | LWT 载荷 | 仅 status 字段 | 是否需要携带离线原因（掉电/主动断开） |
| 6 | 下行 cmd | 预留未定义 | v2 按远程配置/控制需求定义 |
| 7 | 三元组烧录方式 | 管理后台查看后烧录 | 产线工具直连平台 API，或设备配网页面 |

**大改的隔离面**：本协议的字段/语义变化只影响两处实现——hummingbird 的 `mqttgateway` 包（报文解析与转换层，约 200 行）+ 种子物模型脚本；平台核心（入库链路/告警/存储）不感知协议细节。
