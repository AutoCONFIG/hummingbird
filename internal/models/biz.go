package models

import "github.com/winc-link/hummingbird/internal/pkg/constants"

// 翠鸟业务模型（docs/water-platform-plan.md §6）：
// 租户过滤链 wx_user.tenant_id → farm → pond → pond_device → device，核心平台表不加字段

type Tenant struct {
	Timestamps
	Id         string `gorm:"primaryKey;type:string;size:255;comment:主键"`
	Name       string `gorm:"type:string;size:255;comment:租户名称"`
	InviteCode string `gorm:"uniqueIndex;type:string;size:64;comment:小程序绑定邀请码"`
	Status     string `gorm:"type:string;size:32;comment:状态"`
}

func (Tenant) TableName() string { return constants.TableTenant }

type Farm struct {
	Timestamps
	Id       string `gorm:"primaryKey;type:string;size:255;comment:主键"`
	TenantId string `gorm:"index;type:string;size:255;comment:所属租户"`
	Name     string `gorm:"type:string;size:255;comment:养殖场名称"`
	Location string `gorm:"type:string;size:255;comment:位置"`
}

func (Farm) TableName() string { return constants.TableFarm }

type Pond struct {
	Timestamps
	Id       string  `gorm:"primaryKey;type:string;size:255;comment:主键"`
	FarmId   string  `gorm:"index;type:string;size:255;comment:所属养殖场"`
	TenantId string  `gorm:"index;type:string;size:255;comment:所属租户"`
	Name     string  `gorm:"type:string;size:255;comment:池塘名称"`
	AreaM2   float64 `gorm:"comment:面积（平方米）"`
}

func (Pond) TableName() string { return constants.TablePond }

type PondDevice struct {
	Timestamps
	Id       string `gorm:"primaryKey;type:string;size:255;comment:主键"`
	PondId   string `gorm:"index;type:string;size:255;comment:池塘"`
	DeviceId string `gorm:"index;type:string;size:255;comment:设备"`
}

func (PondDevice) TableName() string { return constants.TablePondDevice }

type WxUser struct {
	Timestamps
	Id       string `gorm:"primaryKey;type:string;size:255;comment:主键"`
	OpenId   string `gorm:"uniqueIndex;type:string;size:128;comment:微信openid"`
	UnionId  string `gorm:"type:string;size:128;comment:微信unionid"`
	Nickname string `gorm:"type:string;size:255;comment:昵称"`
	Avatar   string `gorm:"type:string;size:512;comment:头像"`
	TenantId string `gorm:"index;type:string;size:255;comment:所属租户（空=未绑定）"`
	Role     string `gorm:"type:string;size:32;comment:角色（一期统一 member）"`
	Status   string `gorm:"type:string;size:32;comment:状态"`
}

func (WxUser) TableName() string { return constants.TableWxUser }

type WxSubscribeRecord struct {
	Timestamps
	Id         string `gorm:"primaryKey;type:string;size:255;comment:主键"`
	UserId     string `gorm:"index;type:string;size:255;comment:小程序用户"`
	TemplateId string `gorm:"type:string;size:128;comment:订阅消息模板"`
	Quota      int    `gorm:"comment:剩余可发送次数（一次性订阅）"`
}

func (WxSubscribeRecord) TableName() string { return constants.TableWxSubscribeRecord }

type AppSetting struct {
	Key   string `gorm:"primaryKey;type:string;size:64;comment:配置键"`
	Value string `gorm:"type:string;size:512;comment:配置值"`
}

func (AppSetting) TableName() string { return constants.TableAppSetting }
