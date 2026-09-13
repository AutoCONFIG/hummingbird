# FunctionGraph 四功能模块 → hummingbird 实现映射

> 甲方材料第一阶段定义了 4 个云函数：`iot_data_handler`（数据接收处理）、`alarm_handler`（报警判断）、`device_status_handler`（设备状态处理）、`api_handler`（业务 API）。
> 本文档说明四个模块在 hummingbird（翠鸟）中的**完整等价实现**——不实现 FunctionGraph 框架本身，只实现其业务功能。
> 配套文档：`docs/water-platform-plan.md`（总方案）、`docs/device-protocol.md`（设备协议）、`docs/api-contract.md`（小程序契约）。

## 0. 等价性原理：FunctionGraph 的宿主形态 → hummingbird 的宿主形态

| FunctionGraph 概念 | hummingbird 对应物 | 代码位置 |
|---|---|---|
| 函数（业务逻辑单元） | application 包（DI 注册的应用服务） | `internal/hummingbird/core/application/<name>/` |
| MQTT 触发器（IoTDA 转发） | `tools/mqttclient` 订阅回调（paho OnConnect 自动重订阅） | `internal/tools/mqttclient/` |
| HTTP 触发器（API Gateway） | gin 路由 + controller | `internal/hummingbird/core/route/` + `controller/http/` |
| 定时触发器 | crontab | `internal/pkg/crontab/` |
| 函数注册/部署 | DI 容器注册（initialize/init.go）+ BootstrapHandler 启动 goroutine | `internal/hummingbird/core/initialize/init.go` |
| 环境变量/配置 | configuration.toml | `cmd/hummingbird-core/res/` |

## 1. 总映射表

| # | 云函数 | hummingbird 实现 | 复用/新增 | 里程碑 |
|---|---|---|---|---|
| 1 | iot_data_handler<br>数据接收与处理 | **内置 MQTT 网关模块**（新）+ 消息入库链路（现成） | 新增网关包；入库/标准化全复用 | M3 |
| 2 | alarm_handler<br>报警判断 | **eKuiper 规则引擎 + 告警中心**（全部现成）+ 微信通知渠道（新增）+ 池塘阈值映射层（新增） | 引擎零代码；只补通知与映射 | M4/M6 |
| 3 | device_status_handler<br>设备状态处理 | **网关 LWT/超时判定**（新增）+ 上下线登记链路（现成） | 判定逻辑新增；状态登记复用 | M3 |
| 4 | api_handler<br>业务 API | **waterapp + /api/v1/app/* 路由组**（新增）+ 数据查询/告警服务（现成） | 新增薄业务层；查询全复用 | M4/M5 |

数据流对照：

```
华为云版：设备 → IoTDA → 转发 → FunctionGraph(①②③) → DB → API Gateway → FunctionGraph(④) → 小程序
翠鸟版：  设备 → broker → 网关模块(①③) → messageApp ─┬→ eKuiper(②) ─→ 告警中心(②) → 通知
                                                      └→ TDengine(①) → waterapp(④) → 小程序
```

## 2. ① iot_data_handler → 内置 MQTT 网关 + 现成入库链路

**云函数职责**：接收 IoTDA 转发的设备属性 → 校验 → 识别设备 → 格式标准化 → 写入数据库。

**hummingbird 实现**（M3，`internal/hummingbird/core/application/mqttgateway/`）：

| 云函数步骤 | hummingbird 载体 | 状态 |
|---|---|---|
| 接收数据 | `tools/mqttclient.NewMQTTClient` 订阅 `water/+/report`，consumeCallback 收帧 | 复用客户端，网关包新增 |
| 校验 | 网关内 JSON 解析 + 字段类型/范围检查；非法帧丢弃记日志 | 新增（~50 行） |
| 识别设备 | SN→设备：dbclient 新增 `DeviceBySn` + 网关内存缓存（失效重建；未注册 SN 拒收告警日志） | `DeviceBySn` 新增；缓存新增 |
| 格式标准化 | 扁平 JSON → `dtos.ThingModelMessage{Cid: deviceId, OpType: PROPERTY_REPORT, Data: {"data":{code:{value,time}}}}`（与 BuildEkuiperSql 报文契约严格对齐） | 新增（~80 行） |
| 写入数据库 | 调 `messageApp.ThingModelMsgReport`：内部推 `eventbus/in`（供告警流）+ `persistItf.SaveDeviceThingModelData` → TDengine（产品超级表/设备子表/动态字段，全部现成） | **零新增** |
| 附加 | 同帧更新 battery/signal/last_report_ts（localcache） | 新增（~30 行） |

## 3. ② alarm_handler → eKuiper + 告警中心（几乎零代码）

**云函数职责**：按池塘阈值判断（如 DO<4）→ 生成报警记录 → 保存 → 通知养殖户。

**hummingbird 实现**：整条判断/记录/分发链路平台已有，只补两块：

| 云函数步骤 | hummingbird 载体 | 状态 |
|---|---|---|
| 阈值判断（含 1/5/15/30/60 分钟聚合窗口、原始/均值/最大/最小） | eKuiper 流计算：告警规则即 SQL（`alertcentreapp` 的 `BuildEkuiperSql` 生成），消费 `eventbus/in` 流 | **零新增** |
| 按池塘配置阈值 | M4 映射层：`POST /pond/:id/thresholds` 按 pond_device 展开**批量生成设备级 alert_rule**（命名 `pond:{pondId}:{code}`），同塘多设备规则一致性由映射层保证 | 新增（M4） |
| 生成/保存报警记录 | eKuiper 触发 → 回调 `/api/v1/ekuiper/alert` → `alertcentreapp.AddAlert` 写 `alert_list`（含当前值/阈值/时间） | **零新增** |
| 通知（第一级：应用内报警中心） | `alert_list` 经小程序 API `/app/alarms` 展示；忽略/处理流程现成 | **零新增** |
| 通知（第二级：微信订阅消息） | `internal/tools/notify/wechat/` 新渠道（第 6 种，仿现成 5 渠道模式）+ AddAlert 分发 case + 配额闭环 | 新增（M6） |
| 通知（兜底：短信/钉钉/飞书/企微/webhook） | 现成 5 渠道，规则配置即用 | **零新增** |

## 4. ③ device_status_handler → 网关状态判定 + 现成登记链路

**云函数职责**：接收 IoTDA 设备状态变化 → 更新设备在线状态 → 触发离线告警。

**hummingbird 实现**（M3，在网关包内）：

| 云函数步骤 | hummingbird 载体 | 状态 |
|---|---|---|
| 感知上下线 | 网关订阅 `water/{sn}/status`：LWT `offline` 判离线；`online`/首帧 report 判上线 | 新增（~60 行） |
| 超时兜底 | crontab/内存 ticker：3×上报周期无消息判离线（防遗嘱丢失） | 新增 |
| 更新设备状态 | `messageApp.DeviceStatusToMessageBus(DEVICE_STATUS)` + `dbClient.DeviceOnlineById/DeviceOfflineById`（仿 cloudability.go:29-92） | **零新增** |
| 离线/上线告警 | eKuiper 现成规则类型：`messageType="DEVICE_STATUS"`（设备离线告警开箱即用） | **零新增** |
| 状态暴露小程序 | device.Status（DB）+ localcache（battery/signal/last_report_ts）→ `/app` 接口 `device_status` 块 | 新增（在 ④） |

## 5. ④ api_handler → waterapp 业务层 + /api/v1/app/* 路由组

**云函数职责**：给小程序提供登录、池塘、实时/历史数据、报警等业务 API。

**hummingbird 实现**（M4/M5）：

| 云函数步骤 | hummingbird 载体 | 状态 |
|---|---|---|
| 微信登录 | `tools/wechat`（code2session）+ appJWTAuth 中间件（独立签名密钥，claims 带 wx_user_id）+ 邀请码绑定流程 | 新增 |
| 业务模型（租户/场/塘/绑定） | 新增 PG 业务表 + waterapp（租户过滤链在此层强制） | 新增 |
| 池塘/设备列表 | pond_device 映射 + device 列表查询（复用 dbclient） | 新增薄封装 |
| 实时数据 | `persistItf.SearchDeviceThingModelPropertyData`（Last 语义） | **零新增** |
| 历史曲线 | `persistItf.SearchDeviceThingModelHistoryPropertyData`（Range 毫秒时间戳）；30d 读日聚合表 | 复用 + 聚合读取新增 |
| 报警列表/确认 | `alertcentreapp.AlertSearch` / `TreatedIgnore`（按租户设备集合过滤） | **零新增** |
| 契约 | `docs/api-contract.md` 为唯一契约源，实现后逐接口 diff 验收 | — |

## 6. 工作量小结

四个模块中，**②报警判断的引擎与 ③状态登记链路是平台现成能力（零代码）**；真正要写的只有三块新代码：

1. **mqttgateway 包**（①+③ 的判定/标准化部分，约 300~400 行）—— 唯一的"核心"新增
2. **waterapp + /api/v1/app/***（④，含 wechat 客户端与 appJWT，常规业务开发约 1000~1500 行）
3. **业务表与映射层**（租户/场/塘/阈值展开，M4）+ **微信通知渠道**（M6，约 200 行）

全部挂在 hummingbird 现有架构的扩展点上（application 包 + DI 注册 + gin 路由组），不动核心代码。
