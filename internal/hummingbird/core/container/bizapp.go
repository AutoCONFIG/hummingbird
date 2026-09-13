package container

import (
	"github.com/winc-link/hummingbird/internal/hummingbird/core/application/biz"
	"github.com/winc-link/hummingbird/internal/pkg/di"
)

var BizAppName = di.TypeInstanceToName((*biz.BizApp)(nil))

func BizAppFrom(get di.Get) *biz.BizApp {
	return get(BizAppName).(*biz.BizApp)
}
