# 翠鸟平台 · 小程序/APP API 契约 v1

> 读者：小程序/APP 前端开发；第一阶段华为云 FunctionGraph 与第二阶段 hummingbird 后端均以本文档为**唯一契约基线**实现（甲方三原则：小程序只访问本契约、后端切换零修改）。
> 状态：**基线版 v0.9（暂定）——接口仍在定义中，预期有较大调整**
> **变更策略（两阶段）**：
> ① 当前阶段（未对接甲方前端、未上线）：**允许破坏性变更**——直接修改本文档并递增修订号（v0.x），同步通知前端与两阶段后端实现方；
> ② 小程序提审/上线后：字段只增不删，破坏性变更升版本 `/api/v2`。
>
> **开放项（预期会变的点，见 §5）**：页面字段集、历史粒度、报警处置流程、登录细节、阈值编辑归属等。
> 基础 URL：`https://<域名>/api/v1/app`（生产强制 HTTPS，域名需备案并加入小程序 request 合法域名）

---

## 1. 基础约定

### 1.1 响应包络

所有接口（含错误）HTTP 状态码均为 200（认证失败除外，见 1.4），业务结果由包络表达：

```json
{"success": true, "errorCode": 0, "errorMsg": "", "result": {}}
```

失败：`{"success": false, "errorCode": 60302, "errorMsg": "邀请码无效", "result": []}`

### 1.2 分页包络

列表类接口 `result` 为：

```json
{"list": [], "total": 0, "page": 1, "pageSize": 20}
```

### 1.3 约定

- 时间戳一律 **Unix 毫秒**（int64）
- 历史曲线时间维度：`today` / `7d` / `30d`（对应甲方"今天/最近7天/最近30天"）
- **多租户语义**：所有业务接口自动限定在当前用户 `tenant_id` 内，跨租户资源一律按不存在处理（返回 60401）
- 本契约 v1 字段**只增不删**；破坏性变更升版本 `/api/v2`

### 1.4 认证

| 项 | 约定 |
|---|---|
| 凭证头 | `x-token: <token>`（除 `/auth/login`、`/auth/bind` 外全部接口必带） |
| 有效期 | 7 天；过期后接口返回 **HTTP 401**（body 为标准包络、errorCode 60101），前端静默调用 `wx.login` 重新走 §2.1 登录即可 |
| 未绑定租户 | 登录成功但 `need_bind=true` 时，业务接口返回 errorCode **60301**；前端引导输入邀请码走 §2.2 |

### 1.5 错误码

| errorCode | 含义 | 前端处理建议 |
|---|---|---|
| 0 | 成功 | — |
| 40001 | 参数错误（errorMsg 带字段说明） | toast 提示 |
| 60101 | token 缺失/无效/过期（HTTP 401） | 静默重登 |
| 60301 | 未绑定租户 | 引导输入邀请码 |
| 60302 | 邀请码无效 | toast 提示 |
| 60401 | 资源不存在或无权访问 | 返回列表页 |
| 60429 | 请求过于频繁（登录/配额接口限流） | 提示稍后重试 |
| 60000 | 服务器内部错误 | toast 提示 |
| — | 其余平台通用错误码（如 20101） | 透传 errorMsg |

> 实现注：6xxxx 段为本契约新增码，M5 实施时注册进 `internal/pkg/errort`；与平台既有码不冲突。

---

## 2. 认证

### 2.1 微信登录

`POST /auth/login`

| 参数 | 类型 | 必填 | 说明 |
|---|---|---|---|
| code | string | 是 | `wx.login()` 获取的临时 code |

```json
// 请求
{"code": "0a3Xxx..."}
// 响应（未绑定租户）
{"success": true, "errorCode": 0, "result": {
  "token": "eyJhbGciOi...", "expires_in": 604800,
  "need_bind": true,
  "profile": {"nickname": "微信用户", "avatar": ""}
}}
// 响应（已绑定）
{"success": true, "errorCode": 0, "result": {
  "token": "eyJhbGciOi...", "expires_in": 604800,
  "need_bind": false,
  "profile": {"nickname": "张三", "avatar": "https://...", "tenant_name": "示范养殖场"}
}}
```

后端行为：code → 微信 code2session → 按 openid upsert 用户 → 签发 token。微信侧 session_key 不下发前端。

### 2.2 绑定租户

`POST /auth/bind`（需 x-token）

| 参数 | 类型 | 必填 | 说明 |
|---|---|---|---|
| invite_code | string | 是 | 运营方在管理后台生成的租户邀请码 |

```json
// 响应
{"success": true, "errorCode": 0, "result": {"tenant_name": "示范养殖场"}}
```

---

## 3. 业务接口

### 3.1 养殖场列表

`GET /farms`（需 x-token）

```json
{"success": true, "errorCode": 0, "result": {"list": [
  {"id": "f001", "name": "1号养殖场", "location": "湛江·坡头",
   "pond_total": 12, "pond_alarm": 1, "pond_offline": 2}
]}}
```

### 3.2 池塘列表

`GET /farms/{farmId}/ponds`（需 x-token）

```json
{"success": true, "errorCode": 0, "result": {"list": [
  {"id": "p001", "name": "1号池塘", "area_m2": 3000,
   "status": "alarm",           // normal | alarm | offline
   "metrics": {"temperature": 27.5, "dissolved_oxygen": 3.8, "ph": 7.9},
   "online": true}
]}}
```

`status` 优先级：该塘有未处理告警 → `alarm`；绑定设备全部离线 → `offline`；否则 `normal`。`metrics` 为最近值快照（与 3.4 同源），供列表页直接渲染。

### 3.3 池塘详情

`GET /ponds/{pondId}`（需 x-token）

```json
{"success": true, "errorCode": 0, "result": {
  "id": "p001", "name": "1号池塘", "area_m2": 3000,
  "farm": {"id": "f001", "name": "1号养殖场"},
  "devices": [
    {"id": "d001", "sn": "DEV001", "name": "1号塘水质终端",
     "device_status": {"online": true, "battery": 3.9, "signal": -67, "last_report_ts": 1731500000000}}
  ]
}}
```

### 3.4 实时监测（甲方页面③）

`GET /ponds/{pondId}/latest`（需 x-token）

```json
{"success": true, "errorCode": 0, "result": {
  "metrics": [
    {"code": "temperature",       "name": "温度",   "value": 27.5, "unit": "℃",   "ts": 1731500000000, "status": "normal"},
    {"code": "dissolved_oxygen",  "name": "溶解氧", "value": 3.8,  "unit": "mg/L", "ts": 1731500000000, "status": "alarm"},
    {"code": "ph",                "name": "pH",    "value": 7.9,  "unit": "",     "ts": 1731500000000, "status": "normal"},
    {"code": "turbidity",         "name": "浊度",   "value": 12.5, "unit": "NTU",  "ts": 1731500000000, "status": "normal"},
    {"code": "salinity",          "name": "盐度",   "value": 28.6, "unit": "ppt",  "ts": 1731500000000, "status": "normal"}
  ],
  "device_status": {"online": true, "battery": 3.9, "signal": -67, "last_report_ts": 1731500000000}
}}
```

`metrics[].status`：该指标存在未处理告警 → `alarm`，否则 `normal`。

### 3.5 历史曲线（甲方页面④）

`GET /ponds/{pondId}/history?code=dissolved_oxygen&range=7d`（需 x-token）

| 参数 | 类型 | 必填 | 说明 |
|---|---|---|---|
| code | string | 是 | 指标 code（见 §4 附录） |
| range | string | 是 | `today` / `7d` / `30d` |

| range | 返回粒度 | 点数上限 | 数据源 |
|---|---|---|---|
| today | 原始点 | ≤ 1440 | 原始表 |
| 7d | 按小时聚合（均值） | ≤ 168 | 原始表（服务端聚合） |
| 30d | 按日聚合（均值） | ≤ 30 | 日聚合表 |

```json
{"success": true, "errorCode": 0, "result": {
  "code": "dissolved_oxygen", "name": "溶解氧", "unit": "mg/L", "granularity": "hour",
  "points": [{"ts": 1731427200000, "value": 6.2}, {"ts": 1731430800000, "value": 6.1}]
}}
```

### 3.6 报警列表（甲方页面⑤）

`GET /alarms?status=pending&page=1&pageSize=20`（需 x-token）

| 参数 | 类型 | 必填 | 说明 |
|---|---|---|---|
| status | string | 否 | `pending`（默认）/ `treated` / 空=全部 |
| page / pageSize | int | 否 | 默认 1 / 20，最大 100 |

```json
{"success": true, "errorCode": 0, "result": {"list": [
  {"id": "al001", "pond": {"id": "p001", "name": "1号池塘"},
   "rule_name": "溶解氧过低", "level": "serious",
   "metric": {"code": "dissolved_oxygen", "name": "溶解氧", "unit": "mg/L",
              "current_value": 3.8, "threshold": "4.0", "condition": "< 4.0"},
   "trigger_ts": 1731500000000, "status": "pending",
   "treated_time": 0, "message": ""}
], "total": 1, "page": 1, "pageSize": 20}}
```

### 3.7 确认报警

`PUT /alarms/{alarmId}/confirm`（需 x-token）

```json
// 请求
{"message": "已开增氧机，10分钟后恢复"}
// 响应
{"success": true, "errorCode": 0, "result": []}
```

### 3.8 首页汇总（甲方页面①）

`GET /home/summary`（需 x-token）

```json
{"success": true, "errorCode": 0, "result": {
  "farm_total": 1, "pond_total": 12,
  "device_online": 10, "device_offline": 2,
  "alarm_pending": 3, "updated_ts": 1731500000000
}}
```

### 3.9 上报订阅配额（配合微信订阅消息）

`POST /subscribe/quota`（需 x-token）

| 参数 | 类型 | 必填 | 说明 |
|---|---|---|---|
| template_id | string | 是 | 订阅消息模板 ID |

调用时机：前端 `wx.requestSubscribeMessage` 返回 accept 后调用一次，服务端为该用户该模板配额 +1。每次报警通知消耗 1 次配额；配额不足时报警仅进应用内报警中心（第二级微信通知不发送）。

```json
{"success": true, "errorCode": 0, "result": {"quota": 3}}
```

---

## 4. 附录

### 4.1 指标 code 字典（与设备协议 `docs/device-protocol.md` §4.1 一致）

| code | 名称 | 单位 | 设备上报 |
|---|---|---|---|
| temperature | 温度 | ℃ | 必报 |
| dissolved_oxygen | 溶解氧 | mg/L | 必报 |
| ph | pH | — | 必报 |
| turbidity | 浊度 | NTU | 必报 |
| salinity | 盐度 | ppt | 必报 |
| battery | 电池电压 | V | 可选 |
| signal | 信号强度 | dBm | 可选 |

### 4.2 甲方 6 页面 ↔ 端点映射

| 页面 | 端点 |
|---|---|
| ① 登录 | §2.1、§2.2 |
| ② 首页（设备/报警汇总） | §3.8 |
| ③ 池塘列表 | §3.1、§3.2 |
| ④ 实时监测 | §3.3、§3.4 |
| ⑤ 历史曲线 | §3.5 |
| ⑥ 报警中心 | §3.6、§3.7 |
| 订阅消息引导 | §3.9 |

### 4.3 契约管理

- 本文档为双阶段唯一契约基线：第一阶段 FunctionGraph 与第二阶段 hummingbird 均按此实现，各自对照本文档做接口测试
- 后端切换验收 = 前端零修改跑通本契约全部用例
- 变更策略见文档头部（当前阶段允许破坏性变更，直接改文档递增 v0.x；上线后字段只增不删，破坏性变更升 `/api/v2`）

## 5. 开放项（预期会变的点）

| # | 开放项 | 当前基线 | 待定因素 |
|---|---|---|---|
| 1 | 页面字段集 | §3 各端点现有字段 | 甲方 UI 设计稿定稿后调整（尤其首页汇总、池塘列表卡片字段） |
| 2 | 历史粒度策略 | today 原始 / 7d 小时聚合 / 30d 日聚合 | 甲方对曲线形态的偏好，可能要求 7d 也看原始点 |
| 3 | 报警处置流程 | 仅文本 message 确认 | 是否需要处置照片、处置分类、多级处理 |
| 4 | 登录与账号 | 微信 code2session + 邀请码绑定 | 是否增加手机号快捷登录、账号合并 |
| 5 | 多租户角色 | 一期单角色（全可见） | 二期分权（场长/塘长/访客） |
| 6 | 池塘阈值编辑归属 | 一期运营方管理后台配置 | 是否提前到小程序端（影响 M4/M5 范围） |
| 7 | 错误码段 | 6xxxx 暂定 | M5 实施注册进 errort 时最终确定 |
| 8 | 订阅消息模板 | 一次性订阅 + 配额 | 申请到长期订阅模板后简化（template_mode 切换） |

**大改的隔离面**：本契约的字段/端点变化只影响两处实现——`waterapp` 业务层 + controller 路由组（M4/M5 的全部范围）；平台核心（设备接入/告警引擎/存储）与设备协议互不感知。
