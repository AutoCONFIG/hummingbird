package constants

// 翠鸟业务表名（models/biz.go）
const (
	TableTenant            = "tenant"
	TableFarm              = "farm"
	TablePond              = "pond"
	TablePondDevice        = "pond_device"
	TableWxUser            = "wx_user"
	TableWxSubscribeRecord = "wx_subscribe_record"
	TableAppSetting        = "app_setting"
)

// 小程序用户状态/角色
const (
	WxUserStatusActive  = "active"
	WxUserStatusBlocked = "blocked"
	WxUserRoleMember    = "member"
)

// AppSetting 键名
const (
	AppSettingKeyAppJwtSignKey = "app_jwt_sign_key"
)
