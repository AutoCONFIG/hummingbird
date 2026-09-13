package gateway

import (
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/winc-link/hummingbird/internal/dtos"
	"github.com/winc-link/hummingbird/internal/hummingbird/core/application/biz"
	coreContainer "github.com/winc-link/hummingbird/internal/hummingbird/core/container"
	"github.com/winc-link/hummingbird/internal/pkg/constants"
	"github.com/winc-link/hummingbird/internal/pkg/errort"
	"github.com/winc-link/hummingbird/internal/pkg/httphelper"
)

// 翠鸟管理端接口（docs/water-platform-plan.md §7 管理端新增）：
// 租户/养殖场/池塘/设备绑定/邀请码/池塘阈值展开

func (ctl *controller) getBizApp() *biz.BizApp {
	return coreContainer.BizAppFrom(ctl.dic.Get)
}

type TenantCreateRequest struct {
	Name string `json:"name"`
}

// TenantAdd 创建租户（返回邀请码）
func (ctl *controller) TenantAdd(c *gin.Context) {
	var req TenantCreateRequest
	if err := c.ShouldBind(&req); err != nil {
		httphelper.RenderFail(c, errort.NewCommonErr(errort.DefaultReqParamsError, err), c.Writer, ctl.lc)
		return
	}
	if req.Name == "" {
		httphelper.RenderFail(c, errort.NewCommonEdgeX(errort.DefaultReqParamsError, "name is required", nil), c.Writer, ctl.lc)
		return
	}
	t, err := ctl.getBizApp().CreateTenant(req.Name)
	if err != nil {
		httphelper.RenderFail(c, err, c.Writer, ctl.lc)
		return
	}
	httphelper.ResultSuccess(t, c.Writer, ctl.lc)
}

func (ctl *controller) TenantsSearch(c *gin.Context) {
	list, err := ctl.getBizApp().Tenants()
	if err != nil {
		httphelper.RenderFail(c, err, c.Writer, ctl.lc)
		return
	}
	httphelper.ResultSuccess(list, c.Writer, ctl.lc)
}

// TenantInviteCodeReset 重新生成邀请码
func (ctl *controller) TenantInviteCodeReset(c *gin.Context) {
	id := c.Param(UrlParamTenantId)
	if id == "" {
		httphelper.RenderFail(c, errort.NewCommonEdgeX(errort.DefaultReqParamsError, "tenant id is required", nil), c.Writer, ctl.lc)
		return
	}
	code, err := ctl.getBizApp().RegenerateInviteCode(id)
	if err != nil {
		httphelper.RenderFail(c, err, c.Writer, ctl.lc)
		return
	}
	httphelper.ResultSuccess(map[string]string{"invite_code": code}, c.Writer, ctl.lc)
}

type FarmCreateRequest struct {
	TenantId string `json:"tenant_id"`
	Name     string `json:"name"`
	Location string `json:"location"`
}

func (ctl *controller) FarmAdd(c *gin.Context) {
	var req FarmCreateRequest
	if err := c.ShouldBind(&req); err != nil || req.TenantId == "" || req.Name == "" {
		httphelper.RenderFail(c, errort.NewCommonEdgeX(errort.DefaultReqParamsError, "tenant_id/name required", nil), c.Writer, ctl.lc)
		return
	}
	f, err := ctl.getBizApp().CreateFarm(req.TenantId, req.Name, req.Location)
	if err != nil {
		httphelper.RenderFail(c, err, c.Writer, ctl.lc)
		return
	}
	httphelper.ResultSuccess(f, c.Writer, ctl.lc)
}

func (ctl *controller) FarmsSearch(c *gin.Context) {
	tenantId := c.Query("tenant_id")
	if tenantId == "" {
		httphelper.RenderFail(c, errort.NewCommonEdgeX(errort.DefaultReqParamsError, "tenant_id required", nil), c.Writer, ctl.lc)
		return
	}
	list, err := ctl.getBizApp().FarmsByTenant(tenantId)
	if err != nil {
		httphelper.RenderFail(c, err, c.Writer, ctl.lc)
		return
	}
	httphelper.ResultSuccess(list, c.Writer, ctl.lc)
}

func (ctl *controller) FarmDelete(c *gin.Context) {
	id := c.Param(UrlParamFarmId)
	if err := ctl.getBizApp().DeleteFarm(id); err != nil {
		httphelper.RenderFail(c, err, c.Writer, ctl.lc)
		return
	}
	httphelper.ResultSuccess(nil, c.Writer, ctl.lc)
}

type PondCreateRequest struct {
	FarmId string  `json:"farm_id"`
	Name   string  `json:"name"`
	AreaM2 float64 `json:"area_m2"`
}

func (ctl *controller) PondAdd(c *gin.Context) {
	var req PondCreateRequest
	if err := c.ShouldBind(&req); err != nil || req.FarmId == "" || req.Name == "" {
		httphelper.RenderFail(c, errort.NewCommonEdgeX(errort.DefaultReqParamsError, "farm_id/name required", nil), c.Writer, ctl.lc)
		return
	}
	farm, err := ctl.getBizApp().FarmById(req.FarmId)
	if err != nil {
		httphelper.RenderFail(c, err, c.Writer, ctl.lc)
		return
	}
	p, err := ctl.getBizApp().CreatePond(farm.TenantId, req.FarmId, req.Name, req.AreaM2)
	if err != nil {
		httphelper.RenderFail(c, err, c.Writer, ctl.lc)
		return
	}
	httphelper.ResultSuccess(p, c.Writer, ctl.lc)
}

func (ctl *controller) PondsSearch(c *gin.Context) {
	farmId := c.Query("farm_id")
	if farmId == "" {
		httphelper.RenderFail(c, errort.NewCommonEdgeX(errort.DefaultReqParamsError, "farm_id required", nil), c.Writer, ctl.lc)
		return
	}
	list, err := ctl.getBizApp().PondsByFarm(farmId)
	if err != nil {
		httphelper.RenderFail(c, err, c.Writer, ctl.lc)
		return
	}
	httphelper.ResultSuccess(list, c.Writer, ctl.lc)
}

func (ctl *controller) PondDelete(c *gin.Context) {
	id := c.Param(UrlParamPondId)
	if err := ctl.getBizApp().DeletePond(id); err != nil {
		httphelper.RenderFail(c, err, c.Writer, ctl.lc)
		return
	}
	httphelper.ResultSuccess(nil, c.Writer, ctl.lc)
}

type PondDeviceBindRequest struct {
	DeviceIds []string `json:"device_ids"`
}

func (ctl *controller) PondDeviceBind(c *gin.Context) {
	pondId := c.Param(UrlParamPondId)
	var req PondDeviceBindRequest
	if err := c.ShouldBind(&req); err != nil || len(req.DeviceIds) == 0 {
		httphelper.RenderFail(c, errort.NewCommonEdgeX(errort.DefaultReqParamsError, "device_ids required", nil), c.Writer, ctl.lc)
		return
	}
	if err := ctl.getBizApp().BindDevices(pondId, req.DeviceIds); err != nil {
		httphelper.RenderFail(c, err, c.Writer, ctl.lc)
		return
	}
	httphelper.ResultSuccess(nil, c.Writer, ctl.lc)
}

func (ctl *controller) PondDeviceUnbind(c *gin.Context) {
	pondId := c.Param(UrlParamPondId)
	deviceId := c.Param(UrlParamDeviceId)
	if err := ctl.getBizApp().UnbindDevice(pondId, deviceId); err != nil {
		httphelper.RenderFail(c, err, c.Writer, ctl.lc)
		return
	}
	httphelper.ResultSuccess(nil, c.Writer, ctl.lc)
}

func (ctl *controller) PondDevicesSearch(c *gin.Context) {
	pondId := c.Param(UrlParamPondId)
	ids, err := ctl.getBizApp().DeviceIdsByPond(pondId)
	if err != nil {
		httphelper.RenderFail(c, err, c.Writer, ctl.lc)
		return
	}
	httphelper.ResultSuccess(ids, c.Writer, ctl.lc)
}

// ---------- 池塘阈值：展开为设备级告警规则 ----------

type PondThresholdItem struct {
	Code      string               `json:"code"`      // 物模型属性 code
	Condition string               `json:"condition"` // 判定条件，如 "< 4.0"
	Level     constants.AlertLevel `json:"level"`     // 告警级别
}

type PondThresholdRequest struct {
	Items  []PondThresholdItem `json:"items"`
	Notify []dtos.Notify       `json:"notify"` // 通知配置（可选，透传给告警规则）
}

const pondRuleNamePrefix = "pond:"

func pondRuleName(pondId, deviceId, code string) string {
	return pondRuleNamePrefix + pondId + ":" + deviceId + ":" + code
}

// PondThresholdUpsert 按 pond_device 展开为设备级告警规则（原实现名 pond:{pondId}:{deviceId}:{code}），
// 同塘多设备规则一致性由本映射层保证
func (ctl *controller) PondThresholdUpsert(c *gin.Context) {
	pondId := c.Param(UrlParamPondId)
	var req PondThresholdRequest
	if err := c.ShouldBind(&req); err != nil {
		httphelper.RenderFail(c, errort.NewCommonErr(errort.DefaultReqParamsError, err), c.Writer, ctl.lc)
		return
	}
	bizApp := ctl.getBizApp()
	deviceIds, err := bizApp.DeviceIdsByPond(pondId)
	if err != nil || len(deviceIds) == 0 {
		httphelper.RenderFail(c, errort.NewCommonEdgeX(errort.DefaultResourcesNotFound, "pond has no bound devices", err), c.Writer, ctl.lc)
		return
	}
	alertApp := ctl.getAlertRuleApp()
	prefix := pondRuleNamePrefix + pondId + ":"

	// 现有本塘规则 → 便于差量删除
	existing, _, err := alertApp.AlertRulesSearch(c, dtos.AlertRuleSearchQueryRequest{
		BaseSearchConditionQuery: dtos.BaseSearchConditionQuery{IsAll: true, Page: 1, PageSize: 1000},
	})
	if err != nil {
		httphelper.RenderFail(c, err, c.Writer, ctl.lc)
		return
	}
	existingByName := make(map[string]dtos.AlertRuleSearchQueryResponse)
	for _, rule := range existing {
		if strings.HasPrefix(rule.Name, prefix) {
			existingByName[rule.Name] = rule
		}
	}

	// 展开：每设备 × 每阈值项
	keep := make(map[string]bool)
	for _, deviceId := range deviceIds {
		device, dErr := ctl.getDeviceApp().DeviceById(c, deviceId)
		if dErr != nil {
			httphelper.RenderFail(c, dErr, c.Writer, ctl.lc)
			return
		}
		for _, item := range req.Items {
			name := pondRuleName(pondId, deviceId, item.Code)
			keep[name] = true
			rule, ok := existingByName[name]
			if !ok {
				ruleId, addErr := alertApp.AddAlertRule(c, dtos.RuleAddRequest{
					Name:        name,
					AlertLevel:  item.Level,
					Description: "池塘阈值规则（由管理端按池塘展开）",
				})
				if addErr != nil {
					httphelper.RenderFail(c, addErr, c.Writer, ctl.lc)
					return
				}
				rule = dtos.AlertRuleSearchQueryResponse{Id: ruleId}
				existingByName[name] = rule
			}
			if uErr := alertApp.UpdateAlertRule(c, dtos.RuleUpdateRequest{
				Id:        rule.Id,
				Condition: constants.WorkerConditionAnyone,
				SubRule: []dtos.SubRule{{
					Trigger:   constants.DeviceDataTrigger,
					ProductId: device.ProductId,
					DeviceId:  device.Id,
					Option: map[string]string{
						"code":             item.Code,
						"value_type":       constants.Original,
						"decide_condition": item.Condition,
					},
				}},
				Notify:      req.Notify,
				SilenceTime: 600,
			}); uErr != nil {
				httphelper.RenderFail(c, uErr, c.Writer, ctl.lc)
				return
			}
		}
	}

	// 删除请求中已移除的规则
	for name := range existingByName {
		if !keep[name] {
			_ = alertApp.AlertRulesDelete(c, existingByName[name].Id)
		}
	}
	httphelper.ResultSuccess(nil, c.Writer, ctl.lc)
}
