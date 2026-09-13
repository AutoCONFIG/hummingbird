package mqttgateway

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/winc-link/edge-driver-proto/thingmodel"

	"github.com/winc-link/hummingbird/internal/dtos"
	coreContainer "github.com/winc-link/hummingbird/internal/hummingbird/core/container"
	interfaces "github.com/winc-link/hummingbird/internal/hummingbird/core/interface"
	"github.com/winc-link/hummingbird/internal/models"
	pkgContainer "github.com/winc-link/hummingbird/internal/pkg/container"
	"github.com/winc-link/hummingbird/internal/pkg/constants"
	"github.com/winc-link/hummingbird/internal/pkg/di"
	"github.com/winc-link/hummingbird/internal/pkg/logger"
	"github.com/winc-link/hummingbird/internal/pkg/utils"
	pkgMQTT "github.com/winc-link/hummingbird/internal/tools/mqttclient"
)

// 直连设备接入协议（docs/device-protocol.md）：
//   上行  water/{deviceSn}/report  扁平 JSON 水质数据
//   状态  water/{deviceSn}/status  {"status":"online|offline"}（设备侧 LWT 为 retained offline）
// 网关职责：订阅 → SN 路由 → 报文转换为平台物模型消息 → messageApp（入库 + eKuiper 告警流）

const (
	// ReportPeriodSec 默认上报周期（秒），与设备协议基线一致；离线判定 = 3×该值
	ReportPeriodSec = 300
	// clientId 网关在 broker 上的标识（mqtt_auth 体系之外，仅平台内部连接）
	gatewayClientId = "hummingbird-mqtt-gateway"
)

// offlineThreshold 设备离线超时（防 LWT 丢失的兜底判定）
func offlineThreshold() time.Duration {
	return 3 * ReportPeriodSec * time.Second
}

type deviceState struct {
	deviceId string
	online   bool
	lastSeen time.Time
}

type GatewayApp struct {
	dic        *di.Container
	lc         logger.LoggingClient
	dbClient   interfaces.DBClient
	messageItf interfaces.MessageItf
	mqttClient pkgMQTT.MQTTClient

	mu       sync.RWMutex
	states   map[string]*deviceState // sn -> state
	sn2Id    map[string]string       // sn -> deviceId 缓存
	lastWarn map[string]time.Time    // 未注册 SN 告警日志节流
}

// NewGatewayApp 启动内置 MQTT 设备网关（订阅 + 离线兜底检查），随进程生命周期退出
func NewGatewayApp(ctx context.Context, wg *sync.WaitGroup, dic *di.Container) {
	lc := pkgContainer.LoggingClientFrom(dic.Get)
	configuration := coreContainer.ConfigurationFrom(dic.Get)
	g := &GatewayApp{
		dic:        dic,
		lc:         lc,
		dbClient:   coreContainer.DBClientFrom(dic.Get),
		messageItf: coreContainer.MessageItfFrom(dic.Get),
		states:     make(map[string]*deviceState),
		sn2Id:      make(map[string]string),
		lastWarn:   make(map[string]time.Time),
	}

	broker := fmt.Sprintf("tcp://%s:%v", configuration.MessageQueue.Host, configuration.MessageQueue.Port)
	client, err := pkgMQTT.NewMQTTClient(dtos.NewMQTTClient{
		Broker:    broker,
		ClientId:  gatewayClientId,
		SubTopics: []string{"water/+/report", "water/+/status"},
	}, lc, g.onMessage, nil, nil)
	if err != nil {
		lc.Errorf("mqttgateway init client failed: %v", err)
		return
	}
	g.mqttClient = client
	lc.Infof("mqttgateway started, broker=%s topics=water/+/report,water/+/status", broker)

	wg.Add(1)
	go func() {
		defer wg.Done()
		<-ctx.Done()
		g.mqttClient.Close()
		lc.Info("mqttgateway stopped")
	}()

	// 离线兜底：周期检查 lastSeen 超时设备
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				g.checkOffline()
			case <-ctx.Done():
				return
			}
		}
	}()
}

func (g *GatewayApp) onMessage(_ mqtt.Client, msg mqtt.Message) {
	topic := msg.Topic()
	parts := strings.Split(topic, "/")
	if len(parts) != 3 || parts[0] != "water" {
		return
	}
	sn, kind := parts[1], parts[2]
	device, ok := g.deviceBySn(sn)
	if !ok {
		g.warnUnknownSn(sn)
		return
	}
	switch kind {
	case "report":
		g.handleReport(device, msg.Payload())
	case "status":
		g.handleStatus(device, msg.Payload())
	}
}

type waterReport struct {
	Temperature     *float64 `json:"temperature"`
	DissolvedOxygen *float64 `json:"dissolved_oxygen"`
	Ph              *float64 `json:"ph"`
	Turbidity       *float64 `json:"turbidity"`
	Salinity        *float64 `json:"salinity"`
	Battery         *float64 `json:"battery"`
	Signal          *int64   `json:"signal"`
	TS              *int64   `json:"ts"`
}

func (g *GatewayApp) handleReport(device models.Device, payload []byte) {
	var report waterReport
	if err := json.Unmarshal(payload, &report); err != nil {
		g.lc.Errorf("mqttgateway invalid report from sn=%s: %v", device.DeviceSn, err)
		return
	}
	ts := time.Now().UnixMilli()
	if report.TS != nil && *report.TS > 0 {
		ts = *report.TS
	}
	data := make(map[string]interface{})
	add := func(code string, v interface{}) {
		data[code] = map[string]interface{}{"value": v, "time": ts}
	}
	if report.Temperature != nil {
		add("temperature", *report.Temperature)
	}
	if report.DissolvedOxygen != nil {
		add("dissolved_oxygen", *report.DissolvedOxygen)
	}
	if report.Ph != nil {
		add("ph", *report.Ph)
	}
	if report.Turbidity != nil {
		add("turbidity", *report.Turbidity)
	}
	if report.Salinity != nil {
		add("salinity", *report.Salinity)
	}
	if report.Battery != nil {
		add("battery", *report.Battery)
	}
	if report.Signal != nil {
		add("signal", *report.Signal)
	}
	if len(data) == 0 {
		return
	}

	if g.markSeen(device.DeviceSn) {
		g.setOnline(device, true)
	}

	dataJSON, _ := json.Marshal(map[string]interface{}{
		"msgId":   utils.GenUUID(),
		"version": "1.0",
		"data":    data,
	})
	msg := dtos.ThingModelMessage{
		Cid:    device.Id,
		OpType: int32(thingmodel.OperationType_PROPERTY_REPORT),
		Data:   string(dataJSON),
	}
	if _, err := g.messageItf.ThingModelMsgReport(context.Background(), msg); err != nil {
		g.lc.Errorf("mqttgateway ingest deviceId=%s error: %v", device.Id, err)
	}
}

func (g *GatewayApp) handleStatus(device models.Device, payload []byte) {
	var st struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(payload, &st); err != nil {
		g.lc.Errorf("mqttgateway invalid status from sn=%s: %v", device.DeviceSn, err)
		return
	}
	switch st.Status {
	case constants.DeviceOnline:
		g.markSeen(device.DeviceSn)
		g.setOnline(device, true)
	case constants.DeviceOffline:
		g.setOnline(device, false)
	}
}

// deviceBySn 带缓存的 SN→设备 解析；未注册 SN 返回 false
func (g *GatewayApp) deviceBySn(sn string) (models.Device, bool) {
	g.mu.RLock()
	if id, ok := g.sn2Id[sn]; ok {
		g.mu.RUnlock()
		return models.Device{Id: id, DeviceSn: sn}, true
	}
	g.mu.RUnlock()

	device, err := g.dbClient.DeviceBySn(sn)
	if err != nil {
		return models.Device{}, false
	}
	g.mu.Lock()
	g.sn2Id[sn] = device.Id
	if _, ok := g.states[sn]; !ok {
		g.states[sn] = &deviceState{deviceId: device.Id}
	}
	g.mu.Unlock()
	return device, true
}

func (g *GatewayApp) warnUnknownSn(sn string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if last, ok := g.lastWarn[sn]; ok && time.Since(last) < time.Minute {
		return
	}
	g.lastWarn[sn] = time.Now()
	g.lc.Warnf("mqttgateway report from unregistered sn=%s, ignored", sn)
}

// markSeen 记录最近活跃时间，返回是否需要标记上线（由离线转在线）
func (g *GatewayApp) markSeen(sn string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	st, ok := g.states[sn]
	if !ok {
		return false
	}
	st.lastSeen = time.Now()
	if !st.online {
		st.online = true
		return true
	}
	return false
}

func (g *GatewayApp) setOnline(device models.Device, online bool) {
	ctx := context.Background()
	if online {
		if err := g.dbClient.DeviceOnlineById(device.Id); err != nil {
			g.lc.Errorf("mqttgateway DeviceOnlineById %s: %v", device.Id, err)
		}
		g.messageItf.DeviceStatusToMessageBus(ctx, device.Id, constants.DeviceOnline)
		g.lc.Infof("mqttgateway device online sn=%s id=%s", device.DeviceSn, device.Id)
		return
	}
	if err := g.dbClient.DeviceOfflineById(device.Id); err != nil {
		g.lc.Errorf("mqttgateway DeviceOfflineById %s: %v", device.Id, err)
	}
	g.messageItf.DeviceStatusToMessageBus(ctx, device.Id, constants.DeviceOffline)
	g.lc.Infof("mqttgateway device offline sn=%s id=%s", device.DeviceSn, device.Id)
}

func (g *GatewayApp) checkOffline() {
	g.mu.Lock()
	var toOffline []string
	for _, st := range g.states {
		if st.online && !st.lastSeen.IsZero() && time.Since(st.lastSeen) > offlineThreshold() {
			st.online = false
			toOffline = append(toOffline, st.deviceId)
		}
	}
	g.mu.Unlock()
	for _, id := range toOffline {
		g.setOnline(models.Device{Id: id}, false)
	}
}
