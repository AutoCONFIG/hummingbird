package app

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	jwtgo "github.com/dgrijalva/jwt-go"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/winc-link/hummingbird/internal/hummingbird/core/application/biz"
	coreContainer "github.com/winc-link/hummingbird/internal/hummingbird/core/container"
	interfaces "github.com/winc-link/hummingbird/internal/hummingbird/core/interface"
	"github.com/winc-link/hummingbird/internal/dtos"
	"github.com/winc-link/hummingbird/internal/models"
	"github.com/winc-link/hummingbird/internal/pkg/constants"
	"github.com/winc-link/hummingbird/internal/pkg/container"
	"github.com/winc-link/hummingbird/internal/pkg/di"
	"github.com/winc-link/hummingbird/internal/pkg/logger"
	"github.com/winc-link/hummingbird/internal/pkg/middleware"
	jwt2 "github.com/winc-link/hummingbird/internal/tools/jwt"
	"github.com/winc-link/hummingbird/internal/tools/wechat"
)

// 翠鸟小程序/APP API（docs/api-contract.md，双阶段唯一契约基线）。
// 多租户隔离在本层强制：所有业务数据按 wx_user.tenant_id → farm/pond → pond_device → device 过滤。

// 契约错误码（v0.9 基线，M7 定稿时在 errort 注册）
const (
	errParam            uint32 = 40001
	errTokenInvalid     uint32 = 60101
	errNeedTenantBind   uint32 = 60301
	errInviteCodeBad    uint32 = 60302
	errResourceNotFound uint32 = 60401
	errInternal         uint32 = 60000
)

const wxUserIdPrefix = "wx_"

type appController struct {
	dic      *di.Container
	lc       logger.LoggingClient
	biz      *biz.BizApp
	wechat   *wechat.Client
	alertApp interfaces.AlertRuleApp
	persist  interfaces.PersistItf
	dataDB   interfaces.DataDBClient
	db       *gorm.DB
}

// RegisterAppRoutes 小程序/APP API 路由注册入口（route.LoadRestRoutes 调用）
func RegisterAppRoutes(engine *gin.Engine, dic *di.Container) {
	NewAppController(engine, dic)
}

func NewAppController(engine *gin.Engine, dic *di.Container) {
	lc := container.LoggingClientFrom(dic.Get)
	cfg := coreContainer.ConfigurationFrom(dic.Get)
	bizApp := coreContainer.BizAppFrom(dic.Get)
	ctl := &appController{
		dic:      dic,
		lc:       lc,
		biz:      bizApp,
		wechat:   wechat.NewWechatClient(cfg.ApplicationSettings.WeChatAppId, cfg.ApplicationSettings.WeChatAppSecret, lc),
		alertApp: coreContainer.AlertRuleAppNameFrom(dic.Get),
		persist:  coreContainer.PersistItfFrom(dic.Get),
		dataDB:   coreContainer.DataDBClientFrom(dic.Get),
		db:       coreContainer.DBClientFrom(dic.Get).GetDBInstance(),
	}

	app := engine.Group("/api/v1/app")
	app.POST("/auth/login", ctl.Login)

	authed := app.Group("", ctl.authMiddleware())
	authed.POST("/auth/bind", ctl.Bind)
	authed.GET("/farms", ctl.Farms)
	authed.GET("/farms/:farmId/ponds", ctl.FarmPonds)
	authed.GET("/ponds/:pondId", ctl.PondDetail)
	authed.GET("/ponds/:pondId/latest", ctl.PondLatest)
	authed.GET("/ponds/:pondId/history", ctl.PondHistory)
	authed.GET("/alarms", ctl.AlarmList)
	authed.PUT("/alarms/:alarmId/confirm", ctl.AlarmConfirm)
	authed.GET("/home/summary", ctl.HomeSummary)
	authed.POST("/subscribe/quota", ctl.SubscribeQuota)

	lc.Infof("kingfisher app api registered under /api/v1/app")
}

// ---------- 认证 ----------

func (a *appController) authMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		token := c.GetHeader("x-token")
		if token == "" {
			a.abort401(c)
			return
		}
		claims, err := jwt2.NewJWT(a.biz.AppJwtSignKey()).ParseToken(token)
		if err != nil || claims == nil || !strings.HasPrefix(claims.Username, wxUserIdPrefix) {
			a.abort401(c)
			return
		}
		c.Set("wx_user_id", strings.TrimPrefix(claims.Username, wxUserIdPrefix))
		c.Next()
	}
}

func (a *appController) abort401(c *gin.Context) {
	c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
		"success": false, "errorCode": errTokenInvalid, "errorMsg": "token invalid or expired", "result": []interface{}{},
	})
}

func (a *appController) wxUser(c *gin.Context) (models.WxUser, bool) {
	u, err := a.biz.WxUserById(c.GetString("wx_user_id"))
	if err != nil {
		a.abort401(c)
		return models.WxUser{}, false
	}
	return u, true
}

// requireTenant 未绑定租户时返回 60301，返回 false 表示已中断
func (a *appController) requireTenant(c *gin.Context) (models.WxUser, bool) {
	u, ok := a.wxUser(c)
	if !ok {
		return u, false
	}
	if u.TenantId == "" {
		c.JSON(http.StatusOK, gin.H{"success": false, "errorCode": errNeedTenantBind, "errorMsg": "tenant not bound", "result": gin.H{"need_bind": true}})
		return u, false
	}
	return u, true
}

func (a *appController) fail(c *gin.Context, code uint32, msg string, err error) {
	if err == nil {
		err = fmt.Errorf("%s", msg)
	}
	a.lc.Errorf("app api error: code=%d msg=%s detail=%v", code, msg, err)
	c.JSON(http.StatusOK, gin.H{"success": false, "errorCode": code, "errorMsg": msg, "result": []interface{}{}})
}

type AppLoginRequest struct {
	Code string `json:"code"`
}

type AppProfile struct {
	Nickname   string `json:"nickname"`
	Avatar     string `json:"avatar"`
	TenantName string `json:"tenant_name,omitempty"`
}

// Login 契约 §2.1：code → openid upsert 用户 → 签发 appJWT
func (a *appController) Login(c *gin.Context) {
	var req AppLoginRequest
	if err := c.ShouldBind(&req); err != nil || req.Code == "" {
		a.fail(c, errParam, "code is required", err)
		return
	}
	session, err := a.wechat.Code2Session(req.Code)
	if err != nil {
		a.fail(c, errInternal, "wechat login failed", err)
		return
	}
	user, err := a.biz.UpsertWxUser(session.OpenId, session.UnionId, "微信用户", "")
	if err != nil {
		a.fail(c, errInternal, "upsert user failed", err)
		return
	}
	claims := middleware.CustomClaims{
		Username: wxUserIdPrefix + user.Id,
		StandardClaims: jwtgo.StandardClaims{
			ExpiresAt: time.Now().Add(7 * 24 * time.Hour).Unix(),
			Issuer:    "kingfisher-app",
		},
	}
	token, err := jwt2.NewJWT(a.biz.AppJwtSignKey()).CreateToken(claims)
	if err != nil {
		a.fail(c, errInternal, "sign token failed", err)
		return
	}
	profile := AppProfile{Nickname: user.Nickname, Avatar: user.Avatar}
	if user.TenantId != "" {
		if t, err := a.biz.TenantById(user.TenantId); err == nil {
			profile.TenantName = t.Name
		}
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "errorCode": 0, "errorMsg": "success", "result": gin.H{
		"token":     token,
		"expires_in": 7 * 24 * 3600,
		"need_bind": user.TenantId == "",
		"profile":   profile,
	}})
}

type AppBindRequest struct {
	InviteCode string `json:"invite_code"`
}

// Bind 契约 §2.2：邀请码绑定租户
func (a *appController) Bind(c *gin.Context) {
	user, ok := a.wxUser(c)
	if !ok {
		return
	}
	var req AppBindRequest
	if err := c.ShouldBind(&req); err != nil || req.InviteCode == "" {
		a.fail(c, errParam, "invite_code is required", err)
		return
	}
	tenant, err := a.biz.TenantByInviteCode(req.InviteCode)
	if err != nil {
		a.fail(c, errInviteCodeBad, "邀请码无效", nil)
		return
	}
	if err = a.biz.BindWxUserToTenant(user.Id, tenant.Id); err != nil {
		a.fail(c, errInternal, "bind failed", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "errorCode": 0, "errorMsg": "success", "result": gin.H{"tenant_name": tenant.Name}})
}

// ---------- 业务查询（租户过滤） ----------

// Farms 契约 §3.1
func (a *appController) Farms(c *gin.Context) {
	user, ok := a.requireTenant(c)
	if !ok {
		return
	}
	farms, err := a.biz.FarmsByTenant(user.TenantId)
	if err != nil {
		a.fail(c, errInternal, "query farms failed", err)
		return
	}
	list := make([]gin.H, 0, len(farms))
	for _, f := range farms {
		ponds, _ := a.biz.PondsByFarm(f.Id)
		alarmCount, offlineCount := 0, 0
		for _, p := range ponds {
			status := a.pondStatus(p.Id)
			if status == "alarm" {
				alarmCount++
			} else if status == "offline" {
				offlineCount++
			}
		}
		list = append(list, gin.H{
			"id": f.Id, "name": f.Name, "location": f.Location,
			"pond_total": len(ponds), "pond_alarm": alarmCount, "pond_offline": offlineCount,
		})
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "errorCode": 0, "errorMsg": "success", "result": gin.H{"list": list}})
}

// FarmPonds 契约 §3.2 池塘列表（含最近值快照与状态）
func (a *appController) FarmPonds(c *gin.Context) {
	user, ok := a.requireTenant(c)
	if !ok {
		return
	}
	farmId := c.Param("farmId")
	farm, err := a.biz.FarmById(farmId)
	if err != nil || farm.TenantId != user.TenantId {
		a.fail(c, errResourceNotFound, "farm not found", nil)
		return
	}
	ponds, _ := a.biz.PondsByFarm(farmId)
	list := make([]gin.H, 0, len(ponds))
	for _, p := range ponds {
		metrics, deviceOnline, deviceTotal := a.pondSnapshot(p.Id)
		list = append(list, gin.H{
			"id": p.Id, "name": p.Name, "area_m2": p.AreaM2,
			"status": a.pondStatus(p.Id),
			"metrics": metrics,
			"online":  deviceOnline > 0,
			"device_total": deviceTotal, "device_online": deviceOnline,
		})
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "errorCode": 0, "errorMsg": "success", "result": gin.H{"list": list}})
}

// PondDetail 契约 §3.3
func (a *appController) PondDetail(c *gin.Context) {
	user, ok := a.requireTenant(c)
	if !ok {
		return
	}
	pond, err := a.biz.PondById(c.Param("pondId"))
	if err != nil || pond.TenantId != user.TenantId {
		a.fail(c, errResourceNotFound, "pond not found", nil)
		return
	}
	farm, _ := a.biz.FarmById(pond.FarmId)
	deviceIds, _ := a.biz.DeviceIdsByPond(pond.Id)
	devices := make([]gin.H, 0, len(deviceIds))
	for _, deviceId := range deviceIds {
		device, err := a.dbClient().DeviceById(deviceId)
		if err != nil {
			continue
		}
		devices = append(devices, gin.H{
			"id": device.Id, "sn": device.DeviceSn, "name": device.Name,
			"device_status": a.deviceStatus(device),
		})
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "errorCode": 0, "errorMsg": "success", "result": gin.H{
		"id": pond.Id, "name": pond.Name, "area_m2": pond.AreaM2,
		"farm": gin.H{"id": farm.Id, "name": farm.Name},
		"devices": devices,
	}})
}

// PondLatest 契约 §3.4 实时监测
func (a *appController) PondLatest(c *gin.Context) {
	user, ok := a.requireTenant(c)
	if !ok {
		return
	}
	pond, err := a.biz.PondById(c.Param("pondId"))
	if err != nil || pond.TenantId != user.TenantId {
		a.fail(c, errResourceNotFound, "pond not found", nil)
		return
	}
	deviceIds, _ := a.biz.DeviceIdsByPond(pond.Id)
	metrics := make([]gin.H, 0)
	var lastReport int64
	var deviceOnline bool
	var battery, signal interface{}
	for _, deviceId := range deviceIds {
		device, err := a.dbClient().DeviceById(deviceId)
		if err != nil {
			continue
		}
		if onlineOf(device) {
			deviceOnline = true
		}
		for _, d := range a.realtime(deviceId) {
			metrics = append(metrics, gin.H{
				"code": d.Code, "name": d.Name, "value": d.Value, "unit": d.Unit,
				"ts": d.Time, "status": a.metricStatus(pond.Id, deviceId, d.Code),
			})
			if d.Time > lastReport {
				lastReport = d.Time
			}
			if d.Code == "battery" {
				battery = d.Value
			}
			if d.Code == "signal" {
				signal = d.Value
			}
		}
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "errorCode": 0, "errorMsg": "success", "result": gin.H{
		"metrics": metrics,
		"device_status": gin.H{
			"online": deviceOnline, "battery": battery, "signal": signal, "last_report_ts": lastReport,
		},
	}})
}

// PondHistory 契约 §3.5 历史曲线：today/7d 原始，30d 走按天聚合
func (a *appController) PondHistory(c *gin.Context) {
	user, ok := a.requireTenant(c)
	if !ok {
		return
	}
	pond, err := a.biz.PondById(c.Param("pondId"))
	if err != nil || pond.TenantId != user.TenantId {
		a.fail(c, errResourceNotFound, "pond not found", nil)
		return
	}
	code := c.Query("code")
	rangeKey := c.Query("range")
	if code == "" {
		a.fail(c, errParam, "code is required", nil)
		return
	}
	now := time.Now()
	var from time.Time
	granularity := "raw"
	switch rangeKey {
	case "today":
		from = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	case "7d":
		from = now.AddDate(0, 0, -7)
		granularity = "hour"
	case "30d":
		from = now.AddDate(0, 0, -30)
		granularity = "day"
	default:
		a.fail(c, errParam, "range must be today|7d|30d", nil)
		return
	}

	deviceIds, _ := a.biz.DeviceIdsByPond(pond.Id)
	if len(deviceIds) == 0 {
		a.fail(c, errResourceNotFound, "pond has no device", nil)
		return
	}

	var points []gin.H
	if rangeKey == "30d" {
		aggReq := interfacesRealtimeReq(deviceIds[0])
		aggReq.Code = code
		aggReq.Range = []int64{from.UnixMilli(), now.UnixMilli()}
		rows, _, err := a.dataDB.GetDevicePropertyDailyAgg(aggReq, models.Device{Id: deviceIds[0]})
		points = make([]gin.H, 0, len(rows))
		if err != nil || len(rows) == 0 {
			// 不支持按天聚合的实现：回退原始数据 + 服务端日降采样
			points = a.rawDownsample(deviceIds[0], code, from, now, "day")
		} else {
			for _, r := range rows {
				points = append(points, gin.H{"ts": r.Time, "value": r.Value})
			}
		}
	} else {
		raw := a.rawPoints(deviceIds[0], code, from, now)
		if rangeKey == "7d" {
			points = downsample(raw, time.Hour)
		} else {
			points = raw
		}
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "errorCode": 0, "errorMsg": "success", "result": gin.H{
		"code": code, "granularity": granularity, "points": points,
	}})
}

// AlarmList 契约 §3.6 报警列表（租户过滤：alert_rule.device_id ∈ 租户设备集合）
func (a *appController) AlarmList(c *gin.Context) {
	user, ok := a.requireTenant(c)
	if !ok {
		return
	}
	deviceIds, _ := a.biz.DeviceIdsByTenant(user.TenantId)
	if len(deviceIds) == 0 {
		c.JSON(http.StatusOK, gin.H{"success": true, "errorCode": 0, "errorMsg": "success", "result": gin.H{"list": []gin.H{}, "total": 0, "page": 1, "pageSize": 20}})
		return
	}
	status := constants.Untreated
	if s := c.Query("status"); s == "treated" || s == "忽略" {
		status = constants.AlertListStatus(s)
	} else if s == "" || s == "all" {
		status = ""
	}
	page, pageSize := 1, 20
	if v := c.Query("page"); v != "" {
		fmt.Sscanf(v, "%d", &page)
	}
	if v := c.Query("pageSize"); v != "" {
		fmt.Sscanf(v, "%d", &pageSize)
	}
	if pageSize > 100 || pageSize <= 0 {
		pageSize = 20
	}

	query := a.db.Table("alert_list").
		Select("alert_list.id, alert_list.trigger_time, alert_list.alert_result, alert_list.status, alert_list.treated_time, alert_list.message, alert_rule.name as rule_name, alert_rule.alert_level, alert_rule.device_id").
		Joins("JOIN alert_rule ON alert_rule.id = alert_list.alert_rule_id").
		Where("alert_rule.device_id IN ?", deviceIds)
	if status != "" {
		query = query.Where("alert_list.status = ?", status)
	}
	var total int64
	query.Count(&total)
	var rows []map[string]interface{}
	query.Order("alert_list.trigger_time desc").Offset((page - 1) * pageSize).Limit(pageSize).Scan(&rows)

	list := make([]gin.H, 0, len(rows))
	for _, r := range rows {
		deviceId, _ := r["device_id"].(string)
		ruleName, _ := r["rule_name"].(string)
		pond, _ := a.biz.PondIdByDeviceId(deviceId)
		pondName := ""
		if pond != "" {
			if p, err := a.biz.PondById(pond); err == nil {
				pondName = p.Name
			}
		}
		code := ""
		if parts := strings.Split(ruleName, ":"); len(parts) >= 4 {
			code = parts[3]
		}
		list = append(list, gin.H{
			"id": r["id"],
			"pond": gin.H{"id": pond, "name": pondName},
			"rule_name":  ruleName,
			"level":      r["alert_level"],
			"metric": gin.H{"code": code, "alert_result": r["alert_result"]},
			"trigger_ts": r["trigger_time"],
			"status":     r["status"],
			"treated_time": r["treated_time"],
			"message":    r["message"],
		})
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "errorCode": 0, "errorMsg": "success", "result": gin.H{
		"list": list, "total": total, "page": page, "pageSize": pageSize,
	}})
}

// AlarmConfirm 契约 §3.7 确认报警（校验告警归属租户）
func (a *appController) AlarmConfirm(c *gin.Context) {
	user, ok := a.requireTenant(c)
	if !ok {
		return
	}
	alarmId := c.Param("alarmId")
	var body struct {
		Message string `json:"message"`
	}
	_ = c.ShouldBind(&body)

	var row struct {
		DeviceId string
	}
	if err := a.db.Table("alert_list").
		Select("alert_rule.device_id as device_id").
		Joins("JOIN alert_rule ON alert_rule.id = alert_list.alert_rule_id").
		Where("alert_list.id = ?", alarmId).Scan(&row).Error; err != nil || row.DeviceId == "" {
		a.fail(c, errResourceNotFound, "alarm not found", err)
		return
	}
	tenantDeviceIds, _ := a.biz.DeviceIdsByTenant(user.TenantId)
	found := false
	for _, id := range tenantDeviceIds {
		if id == row.DeviceId {
			found = true
			break
		}
	}
	if !found {
		a.fail(c, errResourceNotFound, "alarm not found", nil)
		return
	}
	if err := a.alertApp.TreatedIgnore(c, alarmId, body.Message); err != nil {
		a.fail(c, errInternal, "confirm failed", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "errorCode": 0, "errorMsg": "success", "result": []interface{}{}})
}

// HomeSummary 契约 §3.8
func (a *appController) HomeSummary(c *gin.Context) {
	user, ok := a.requireTenant(c)
	if !ok {
		return
	}
	deviceIds, _ := a.biz.DeviceIdsByTenant(user.TenantId)
	online, offline := 0, 0
	for _, id := range deviceIds {
		device, err := a.dbClient().DeviceById(id)
		if err != nil {
			continue
		}
		if onlineOf(device) {
			online++
		} else {
			offline++
		}
	}
	var farms, ponds, pending int64
	a.db.Model(&models.Farm{}).Where("tenant_id = ?", user.TenantId).Count(&farms)
	a.db.Model(&models.Pond{}).Where("tenant_id = ?", user.TenantId).Count(&ponds)
	if len(deviceIds) > 0 {
		a.db.Table("alert_list").
			Joins("JOIN alert_rule ON alert_rule.id = alert_list.alert_rule_id").
			Where("alert_rule.device_id IN ? AND alert_list.status = ?", deviceIds, constants.Untreated).
			Count(&pending)
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "errorCode": 0, "errorMsg": "success", "result": gin.H{
		"farm_total": farms, "pond_total": ponds,
		"device_online": online, "device_offline": offline,
		"alarm_pending": pending, "updated_ts": time.Now().UnixMilli(),
	}})
}

// SubscribeQuota 契约 §3.9 订阅配额 +1
func (a *appController) SubscribeQuota(c *gin.Context) {
	user, ok := a.wxUser(c)
	if !ok {
		return
	}
	var body struct {
		TemplateId string `json:"template_id"`
	}
	if err := c.ShouldBind(&body); err != nil || body.TemplateId == "" {
		a.fail(c, errParam, "template_id is required", err)
		return
	}
	quota, err := a.biz.IncrSubscribeQuota(user.Id, body.TemplateId)
	if err != nil {
		a.fail(c, errInternal, "quota incr failed", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "errorCode": 0, "errorMsg": "success", "result": gin.H{"quota": quota}})
}

// ---------- 内部辅助 ----------

// pondStatus 池塘状态：alarm > offline > normal
func (a *appController) pondStatus(pondId string) string {
	deviceIds, _ := a.biz.DeviceIdsByPond(pondId)
	if len(deviceIds) == 0 {
		return "normal"
	}
	online := false
	for _, deviceId := range deviceIds {
		device, err := a.dbClient().DeviceById(deviceId)
		if err == nil && onlineOf(device) {
			online = true
		}
		if a.hasUntreatedAlarm(pondId, deviceId, "") {
			return "alarm"
		}
	}
	if !online {
		return "offline"
	}
	return "normal"
}

func (a *appController) hasUntreatedAlarm(pondId, deviceId, code string) bool {
	name := pondRuleNamePrefix + pondId + ":" + deviceId
	q := a.db.Table("alert_list").
		Joins("JOIN alert_rule ON alert_rule.id = alert_list.alert_rule_id").
		Where("alert_list.status = ? AND alert_rule.name LIKE ?", constants.Untreated, name+"%")
	if code != "" {
		q = q.Where("alert_rule.name LIKE ?", name+":"+code)
	}
	var count int64
	q.Count(&count)
	return count > 0
}

func (a *appController) metricStatus(pondId, deviceId, code string) string {
	if a.hasUntreatedAlarm(pondId, deviceId, code) {
		return "alarm"
	}
	return "normal"
}

// pondSnapshot 池塘最近值快照（列表页用）
func (a *appController) pondSnapshot(pondId string) (map[string]interface{}, int, int) {
	deviceIds, _ := a.biz.DeviceIdsByPond(pondId)
	metrics := map[string]interface{}{}
	online, total := 0, len(deviceIds)
	for _, deviceId := range deviceIds {
		device, err := a.dbClient().DeviceById(deviceId)
		if err == nil && onlineOf(device) {
			online++
		}
		for _, d := range a.realtime(deviceId) {
			if _, exists := metrics[d.Code]; !exists {
				metrics[d.Code] = d.Value
			}
		}
	}
	return metrics, online, total
}

func (a *appController) deviceStatus(device models.Device) gin.H {
	return gin.H{"online": onlineOf(device)}
}

func (a *appController) dbClient() interfaces.DBClient {
	return coreContainer.DBClientFrom(a.dic.Get)
}

func interfacesRealtimeReq(deviceId string) dtos.ThingModelPropertyDataRequest {
	return dtos.ThingModelPropertyDataRequest{
		ThingModelDataBaseRequest: dtos.ThingModelDataBaseRequest{Last: true},
		DeviceId:                  deviceId,
	}
}

func onlineOf(device models.Device) bool {
	return device.Status == constants.DeviceOnline
}

func (a *appController) realtime(deviceId string) []dtos.ThingModelDataResponse {
	data, _ := a.persist.SearchDeviceThingModelPropertyData(interfacesRealtimeReq(deviceId))
	if rows, ok := data.([]dtos.ThingModelDataResponse); ok {
		return rows
	}
	return nil
}

const pondRuleNamePrefix = "pond:"

// rawPoints 原始点序列（升序）
func (a *appController) rawPoints(deviceId, code string, from, to time.Time) []gin.H {
	req := dtos.ThingModelPropertyDataRequest{
		BaseSearchConditionQuery: dtos.BaseSearchConditionQuery{IsAll: true},
		ThingModelDataBaseRequest: dtos.ThingModelDataBaseRequest{
			Range: []int64{from.UnixMilli(), to.UnixMilli()},
		},
		DeviceId: deviceId,
		Code:     code,
	}
	rows, _, _ := a.dataDB.GetDeviceProperty(req, models.Device{Id: deviceId})
	points := make([]gin.H, 0, len(rows))
	// GetDeviceProperty 按 ts desc 返回，翻转为升序便于画曲线
	for i := len(rows) - 1; i >= 0; i-- {
		points = append(points, gin.H{"ts": rows[i].Time, "value": rows[i].Value})
	}
	return points
}

// rawDownsample 原始数据按桶取均值（datadb 不支持聚合时的回退路径）
func (a *appController) rawDownsample(deviceId, code string, from, to time.Time, bucket string) []gin.H {
	step := 24 * time.Hour
	if bucket == "hour" {
		step = time.Hour
	}
	points := a.rawPoints(deviceId, code, from, to)
	if len(points) == 0 {
		return points
	}
	buckets := map[int64][]float64{}
	var keys []int64
	for _, p := range points {
		ts, _ := p["ts"].(int64)
		val, _ := p["value"].(string)
		var f float64
		fmt.Sscanf(val, "%f", &f)
		t := time.UnixMilli(ts)
		var bucketStart time.Time
		if bucket == "hour" {
			bucketStart = time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), 0, 0, 0, t.Location())
		} else {
			bucketStart = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
		}
		key := bucketStart.UnixMilli()
		if _, ok := buckets[key]; !ok {
			keys = append(keys, key)
		}
		buckets[key] = append(buckets[key], f)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	result := make([]gin.H, 0, len(keys))
	for _, key := range keys {
		sum := 0.0
		for _, v := range buckets[key] {
			sum += v
		}
		result = append(result, gin.H{"ts": key, "value": fmt.Sprintf("%.2f", sum/float64(len(buckets[key])))})
	}
	_ = step
	return result
}

func downsample(points []gin.H, step time.Duration) []gin.H {
	if len(points) == 0 {
		return points
	}
	buckets := map[int64][]float64{}
	var keys []int64
	for _, p := range points {
		ts, _ := p["ts"].(int64)
		val, _ := p["value"].(string)
		var f float64
		fmt.Sscanf(val, "%f", &f)
		t := time.UnixMilli(ts)
		bucketStart := time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), 0, 0, 0, t.Location()).UnixMilli()
		if _, ok := buckets[bucketStart]; !ok {
			keys = append(keys, bucketStart)
		}
		buckets[bucketStart] = append(buckets[bucketStart], f)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	result := make([]gin.H, 0, len(keys))
	for _, key := range keys {
		sum := 0.0
		for _, v := range buckets[key] {
			sum += v
		}
		result = append(result, gin.H{"ts": key, "value": fmt.Sprintf("%.2f", sum/float64(len(buckets[key])))})
	}
	_ = step
	return result
}


