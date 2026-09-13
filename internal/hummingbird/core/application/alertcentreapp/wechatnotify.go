package alertcentreapp

import (
	"fmt"
	"time"

	coreContainer "github.com/winc-link/hummingbird/internal/hummingbird/core/container"
	"github.com/winc-link/hummingbird/internal/models"
	"github.com/winc-link/hummingbird/internal/tools/wechat"
)

// dispatchWechatMini 微信小程序订阅消息通知（M6，docs/api-contract.md §3.9 配额闭环）：
//   - 一次性订阅模板：每条告警消耗 1 次配额；发送成功扣 1；43101（用户未订阅）清零配额；其他失败保留待重试
//   - 长期订阅模板（WeChatTemplateMode=longterm）：不消耗配额
func (p alertApp) dispatchWechatMini(notify models.SubNotify, alertRule models.AlertRule, device models.Device, req map[string]interface{}) {
	cfg := coreContainer.ConfigurationFrom(p.dic.Get)
	templateId := cfg.ApplicationSettings.WeChatTemplateId
	if cfg.ApplicationSettings.WeChatAppId == "" || templateId == "" {
		p.lc.Debug("wechat mini notify skipped: appid/template not configured")
		return
	}
	bizApp := coreContainer.BizAppFrom(p.dic.Get)
	tenantId, err := bizApp.TenantIdByDeviceId(device.Id)
	if err != nil || tenantId == "" {
		p.lc.Errorf("wechat mini notify: tenant not found for device %s", device.Id)
		return
	}
	longterm := cfg.ApplicationSettings.WeChatTemplateMode == "longterm"
	wc := wechat.NewWechatClient(cfg.ApplicationSettings.WeChatAppId, cfg.ApplicationSettings.WeChatAppSecret, p.lc)

	var users []models.WxUser
	var quotas []int
	if longterm {
		users, _, err = bizApp.WxUsersByTenant(tenantId)
		if err != nil {
			p.lc.Errorf("wechat mini notify: list users failed: %v", err)
			return
		}
	} else {
		users, quotas, err = bizApp.WxUsersWithQuota(tenantId, templateId)
		if err != nil {
			p.lc.Errorf("wechat mini notify: list quota users failed: %v", err)
			return
		}
	}
	if len(users) == 0 {
		p.lc.Debug("wechat mini notify: no subscribed users")
		return
	}

	currentValue := fmt.Sprintf("%v", req["value"])
	data := map[string]string{
		"thing1": truncate(alertRule.Name, 20),
		"thing2": truncate(fmt.Sprintf("%s %s", device.Name, currentValue), 20),
		"time3":  time.Now().Format("2006-01-02 15:04"),
	}
	page := cfg.ApplicationSettings.WeChatNotifyPage

	go func() {
		for i, u := range users {
			quota := 0
			if !longterm && i < len(quotas) {
				quota = quotas[i]
			}
			sent, err := wc.SubscribeSend(u.OpenId, templateId, page, data)
			switch {
			case err != nil:
				p.lc.Errorf("wechat mini notify send failed user=%s: %v", u.OpenId, err)
			case sent:
				if !longterm {
					if _, err = bizApp.ConsumeSubscribeQuota(u.Id, templateId); err != nil {
						p.lc.Errorf("wechat mini notify consume quota failed user=%s: %v", u.OpenId, err)
					}
				}
			default:
				// 43101 用户未订阅/已取消：清零配额
				if !longterm {
					if err = bizApp.ClearSubscribeQuota(u.Id, templateId); err != nil {
						p.lc.Errorf("wechat mini notify clear quota failed user=%s: %v", u.OpenId, err)
					}
				}
			}
			_ = quota
		}
	}()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
