package biz

import (
	"time"

	"gorm.io/gorm"

	"github.com/winc-link/hummingbird/internal/models"
	"github.com/winc-link/hummingbird/internal/pkg/constants"
	"github.com/winc-link/hummingbird/internal/pkg/errort"
	"github.com/winc-link/hummingbird/internal/pkg/logger"
	"github.com/winc-link/hummingbird/internal/pkg/utils"
)

// BizApp 翠鸟业务层：租户/养殖场/池塘/绑定/微信用户/订阅配额。
// 业务表独立于 hummingbird 核心表，通过核心 DBClient 的 gorm 句柄访问（元数据库与核心一致）。
type BizApp struct {
	lc logger.LoggingClient
	db *gorm.DB
}

func NewBizApp(lc logger.LoggingClient, db *gorm.DB) (*BizApp, error) {
	app := &BizApp{lc: lc, db: db}
	if err := db.AutoMigrate(
		&models.Tenant{},
		&models.Farm{},
		&models.Pond{},
		&models.PondDevice{},
		&models.WxUser{},
		&models.WxSubscribeRecord{},
		&models.AppSetting{},
	); err != nil {
		return nil, err
	}
	// appJWT 签名密钥：首次生成并落库，重启不变
	if err := app.ensureSetting(constants.AppSettingKeyAppJwtSignKey, utils.GenUUID); err != nil {
		return nil, err
	}
	return app, nil
}

func (b *BizApp) AppJwtSignKey() string {
	v, _ := b.getSetting(constants.AppSettingKeyAppJwtSignKey)
	return v
}

// ---------- 租户 ----------

func (b *BizApp) CreateTenant(name string) (models.Tenant, error) {
	t := models.Tenant{
		Id:         utils.RandomNum(),
		Name:       name,
		InviteCode: utils.RandomNum(),
		Status:     "active",
	}
	if err := b.db.Create(&t).Error; err != nil {
		return models.Tenant{}, err
	}
	return t, nil
}

func (b *BizApp) TenantById(id string) (models.Tenant, error) {
	var t models.Tenant
	if err := b.db.First(&t, "id = ?", id).Error; err != nil {
		return models.Tenant{}, errort.NewCommonEdgeX(errort.DefaultResourcesNotFound, "tenant not found", err)
	}
	return t, nil
}

func (b *BizApp) Tenants() ([]models.Tenant, error) {
	var list []models.Tenant
	err := b.db.Order("modified desc").Find(&list).Error
	return list, err
}

func (b *BizApp) TenantByInviteCode(code string) (models.Tenant, error) {
	var t models.Tenant
	if err := b.db.First(&t, "invite_code = ?", code).Error; err != nil {
		return models.Tenant{}, errort.NewCommonEdgeX(errort.DefaultResourcesNotFound, "invite code invalid", err)
	}
	return t, nil
}

func (b *BizApp) RegenerateInviteCode(id string) (string, error) {
	code := utils.RandomNum()
	if err := b.db.Model(&models.Tenant{}).Where("id = ?", id).Update("invite_code", code).Error; err != nil {
		return "", err
	}
	return code, nil
}

// ---------- 养殖场 ----------

func (b *BizApp) CreateFarm(tenantId, name, location string) (models.Farm, error) {
	f := models.Farm{
		Id:       utils.RandomNum(),
		TenantId: tenantId,
		Name:     name,
		Location: location,
	}
	if err := b.db.Create(&f).Error; err != nil {
		return models.Farm{}, err
	}
	return f, nil
}

func (b *BizApp) FarmsByTenant(tenantId string) ([]models.Farm, error) {
	var list []models.Farm
	err := b.db.Order("modified desc").Find(&list, "tenant_id = ?", tenantId).Error
	return list, err
}

func (b *BizApp) FarmById(id string) (models.Farm, error) {
	var f models.Farm
	if err := b.db.First(&f, "id = ?", id).Error; err != nil {
		return models.Farm{}, errort.NewCommonEdgeX(errort.DefaultResourcesNotFound, "farm not found", err)
	}
	return f, nil
}

func (b *BizApp) DeleteFarm(id string) error {
	return b.db.Transaction(func(tx *gorm.DB) error {
		var pondIds []string
		if err := tx.Model(&models.Pond{}).Where("farm_id = ?", id).Pluck("id", &pondIds).Error; err != nil {
			return err
		}
		if len(pondIds) > 0 {
			if err := tx.Where("pond_id IN ?", pondIds).Delete(&models.PondDevice{}).Error; err != nil {
				return err
			}
			if err := tx.Where("id IN ?", pondIds).Delete(&models.Pond{}).Error; err != nil {
				return err
			}
		}
		return tx.Where("id = ?", id).Delete(&models.Farm{}).Error
	})
}

// ---------- 池塘 ----------

func (b *BizApp) CreatePond(tenantId, farmId, name string, areaM2 float64) (models.Pond, error) {
	p := models.Pond{
		Id:       utils.RandomNum(),
		FarmId:   farmId,
		TenantId: tenantId,
		Name:     name,
		AreaM2:   areaM2,
	}
	if err := b.db.Create(&p).Error; err != nil {
		return models.Pond{}, err
	}
	return p, nil
}

func (b *BizApp) PondById(id string) (models.Pond, error) {
	var p models.Pond
	if err := b.db.First(&p, "id = ?", id).Error; err != nil {
		return models.Pond{}, errort.NewCommonEdgeX(errort.DefaultResourcesNotFound, "pond not found", err)
	}
	return p, nil
}

func (b *BizApp) PondsByFarm(farmId string) ([]models.Pond, error) {
	var list []models.Pond
	err := b.db.Order("modified desc").Find(&list, "farm_id = ?", farmId).Error
	return list, err
}

func (b *BizApp) DeletePond(id string) error {
	return b.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("pond_id = ?", id).Delete(&models.PondDevice{}).Error; err != nil {
			return err
		}
		return tx.Where("id = ?", id).Delete(&models.Pond{}).Error
	})
}

// ---------- 池塘-设备绑定 ----------

func (b *BizApp) BindDevices(pondId string, deviceIds []string) error {
	return b.db.Transaction(func(tx *gorm.DB) error {
		for _, deviceId := range deviceIds {
			var exist models.PondDevice
			err := tx.First(&exist, "pond_id = ? AND device_id = ?", pondId, deviceId).Error
			if err == nil {
				continue
			}
			binding := models.PondDevice{Id: utils.RandomNum(), PondId: pondId, DeviceId: deviceId}
			if err := tx.Create(&binding).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (b *BizApp) UnbindDevice(pondId, deviceId string) error {
	return b.db.Where("pond_id = ? AND device_id = ?", pondId, deviceId).Delete(&models.PondDevice{}).Error
}

func (b *BizApp) DeviceIdsByPond(pondId string) ([]string, error) {
	var ids []string
	err := b.db.Model(&models.PondDevice{}).Where("pond_id = ?", pondId).Pluck("device_id", &ids).Error
	return ids, err
}

// PondIdByDeviceId 反查设备绑定的池塘（取最早绑定；一设备绑多塘时告警归属按此判定）
func (b *BizApp) PondIdByDeviceId(deviceId string) (string, error) {
	var binding models.PondDevice
	if err := b.db.First(&binding, "device_id = ?", deviceId).Error; err != nil {
		return "", err
	}
	return binding.PondId, nil
}

// DeviceIdsByTenant 租户全部绑定设备（小程序租户过滤链的末端）
func (b *BizApp) DeviceIdsByTenant(tenantId string) ([]string, error) {
	var ids []string
	err := b.db.Model(&models.PondDevice{}).
		Joins("JOIN pond ON pond.id = pond_device.pond_id").
		Where("pond.tenant_id = ?", tenantId).
		Pluck("device_id", &ids).Error
	return ids, err
}

// TenantIdByDeviceId 设备反查租户（告警通知分发用）
func (b *BizApp) TenantIdByDeviceId(deviceId string) (string, error) {
	var tenantId string
	err := b.db.Raw(`SELECT farm.tenant_id FROM pond_device
		JOIN pond ON pond.id = pond_device.pond_id
		JOIN farm ON farm.id = pond.farm_id
		WHERE pond_device.device_id = ? LIMIT 1`, deviceId).Scan(&tenantId).Error
	return tenantId, err
}

// WxUsersByTenant 租户全部激活用户
func (b *BizApp) WxUsersByTenant(tenantId string) ([]models.WxUser, []int, error) {
	var users []models.WxUser
	err := b.db.Find(&users, "tenant_id = ? AND status = ?", tenantId, constants.WxUserStatusActive).Error
	quotas := make([]int, len(users))
	return users, quotas, err
}

// ---------- 微信用户 ----------

func (b *BizApp) UpsertWxUser(openId, unionId, nickname, avatar string) (models.WxUser, error) {
	var u models.WxUser
	err := b.db.First(&u, "open_id = ?", openId).Error
	if err == nil {
		updates := map[string]interface{}{}
		if nickname != "" && nickname != u.Nickname {
			updates["nickname"] = nickname
		}
		if avatar != "" && avatar != u.Avatar {
			updates["avatar"] = avatar
		}
		if len(updates) > 0 {
			updates["modified"] = time.Now().Unix()
			if err = b.db.Model(&u).Updates(updates).Error; err != nil {
				return models.WxUser{}, err
			}
		}
		return u, nil
	}
	u = models.WxUser{
		Id:       utils.GenUUID(),
		OpenId:   openId,
		UnionId:  unionId,
		Nickname: nickname,
		Avatar:   avatar,
		Role:     constants.WxUserRoleMember,
		Status:   constants.WxUserStatusActive,
	}
	if err := b.db.Create(&u).Error; err != nil {
		return models.WxUser{}, err
	}
	return u, nil
}

func (b *BizApp) WxUserById(id string) (models.WxUser, error) {
	var u models.WxUser
	if err := b.db.First(&u, "id = ?", id).Error; err != nil {
		return models.WxUser{}, errort.NewCommonEdgeX(errort.DefaultResourcesNotFound, "wx user not found", err)
	}
	return u, nil
}

// BindWxUserToTenant 用户绑定租户（唯一入口：邀请码）
func (b *BizApp) BindWxUserToTenant(userId, tenantId string) error {
	return b.db.Model(&models.WxUser{}).Where("id = ?", userId).Updates(map[string]interface{}{
		"tenant_id": tenantId,
		"role":      constants.WxUserRoleMember,
		"modified":  time.Now().Unix(),
	}).Error
}

// ---------- 订阅配额 ----------

func (b *BizApp) IncrSubscribeQuota(userId, templateId string) (int, error) {
	var record models.WxSubscribeRecord
	err := b.db.First(&record, "user_id = ? AND template_id = ?", userId, templateId).Error
	if err != nil {
		record = models.WxSubscribeRecord{
			Id:         utils.GenUUID(),
			UserId:     userId,
			TemplateId: templateId,
			Quota:      1,
		}
		if err = b.db.Create(&record).Error; err != nil {
			return 0, err
		}
		return record.Quota, nil
	}
	if err = b.db.Model(&record).Update("quota", record.Quota+1).Error; err != nil {
		return 0, err
	}
	return record.Quota + 1, nil
}

// ConsumeSubscribeQuota 消耗一次配额（发送成功后调用），返回剩余配额
func (b *BizApp) ConsumeSubscribeQuota(userId, templateId string) (int, error) {
	result := b.db.Model(&models.WxSubscribeRecord{}).
		Where("user_id = ? AND template_id = ? AND quota > 0", userId, templateId).
		Update("quota", gorm.Expr("quota - 1"))
	if result.Error != nil {
		return 0, result.Error
	}
	if result.RowsAffected == 0 {
		return 0, errort.NewCommonEdgeX(errort.DefaultResourcesNotFound, "no subscribe quota", nil)
	}
	var record models.WxSubscribeRecord
	if err := b.db.First(&record, "user_id = ? AND template_id = ?", userId, templateId).Error; err != nil {
		return 0, err
	}
	return record.Quota, nil
}

// WxUsersWithQuota 租户内对指定模板仍有配额的用户（告警通知分发用）
func (b *BizApp) WxUsersWithQuota(tenantId, templateId string) ([]models.WxUser, []int, error) {
	var users []models.WxUser
	if err := b.db.Find(&users, "tenant_id = ? AND status = ?", tenantId, constants.WxUserStatusActive).Error; err != nil {
		return nil, nil, err
	}
	var result []models.WxUser
	var quotas []int
	for _, u := range users {
		var record models.WxSubscribeRecord
		if err := b.db.First(&record, "user_id = ? AND template_id = ?", u.Id, templateId).Error; err != nil {
			continue
		}
		if record.Quota > 0 {
			result = append(result, u)
			quotas = append(quotas, record.Quota)
		}
	}
	return result, quotas, nil
}

// ClearSubscribeQuota 43101（用户已取消订阅）时清零配额
func (b *BizApp) ClearSubscribeQuota(userId, templateId string) error {
	return b.db.Model(&models.WxSubscribeRecord{}).
		Where("user_id = ? AND template_id = ?", userId, templateId).
		Update("quota", 0).Error
}

// ---------- 通用 setting ----------

func (b *BizApp) getSetting(key string) (string, error) {
	var s models.AppSetting
	if err := b.db.First(&s, "key = ?", key).Error; err != nil {
		return "", err
	}
	return s.Value, nil
}

func (b *BizApp) ensureSetting(key string, gen func() string) error {
	var s models.AppSetting
	if err := b.db.First(&s, "key = ?", key).Error; err == nil {
		return nil
	}
	return b.db.Create(&models.AppSetting{Key: key, Value: gen()}).Error
}
