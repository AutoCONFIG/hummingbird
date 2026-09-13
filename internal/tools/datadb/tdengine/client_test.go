package tdengine

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/winc-link/hummingbird/internal/dtos"
	interfaces "github.com/winc-link/hummingbird/internal/hummingbird/core/interface"
	"github.com/winc-link/hummingbird/internal/models"
	"github.com/winc-link/hummingbird/internal/pkg/constants"
	"github.com/winc-link/hummingbird/internal/pkg/logger"
)

// 集成测试：需真实 TDengine 实例，通过环境变量开启
// TDENGINE_TEST_DSN='root:taosdata@ws(127.0.0.1:6041)/hummingbird' go test ./internal/tools/datadb/tdengine/ -v
func newTestClient(t *testing.T) interfaces.DataDBClient {
	dsn := os.Getenv("TDENGINE_TEST_DSN")
	if dsn == "" {
		t.Skip("TDENGINE_TEST_DSN not set; skip TDengine integration test")
	}
	lc := logger.NewClient("tdengine-test", "DEBUG", "")
	client, err := NewClient(dtos.Configuration{Dsn: dsn}, lc)
	if err != nil {
		t.Fatalf("connect TDengine failed (check version pairing driver-go v3.5.0 ↔ TDengine 3.1.x): %v", err)
	}
	return client
}

func newTestProduct() models.Product {
	floatProp := func(code, name string) models.Properties {
		return models.Properties{
			ProductId: "kfprodtest",
			Name:      name,
			Code:      code,
			TypeSpec:  models.TypeSpec{Type: constants.SpecsTypeFloat},
		}
	}
	return models.Product{
		Id:   "kfprodtest",
		Name: "水质监测终端",
		Properties: []models.Properties{
			floatProp("temperature", "温度"),
			floatProp("dissolved_oxygen", "溶解氧"),
			floatProp("ph", "pH"),
			floatProp("turbidity", "浊度"),
			floatProp("salinity", "盐度"),
		},
	}
}

func TestTDengineDataPath(t *testing.T) {
	client := newTestClient(t)
	ctx := context.Background()
	const productID = "kfprodtest"
	const deviceID = "kfdevtest"

	// 清理上次运行的残留，保证幂等
	_ = client.DropTable(ctx, deviceID)
	_ = client.DropStable(ctx, productID)

	// 建超级表（产品级，含 5 个 float 指标列）
	if err := client.CreateStable(ctx, newTestProduct()); err != nil {
		t.Fatalf("CreateStable: %v", err)
	}
	// 建子表（设备级）
	if err := client.CreateTable(ctx, productID, deviceID); err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	// 动态加列（物模型扩展）
	if err := client.AddDatabaseField(ctx, productID, constants.SpecsTypeFloat, "battery", "电池电压"); err != nil {
		t.Fatalf("AddDatabaseField: %v", err)
	}

	// 写入两帧属性数据（间隔 1.1s 保证 ts 可区分）
	for _, v := range []float64{6.8, 3.8} {
		if err := client.Insert(ctx, constants.DB_PREFIX+deviceID, map[string]interface{}{
			"temperature":      27.5,
			"dissolved_oxygen": v,
			"ph":               7.9,
		}); err != nil {
			t.Fatalf("Insert: %v", err)
		}
		time.Sleep(1100 * time.Millisecond)
	}

	now := time.Now()
	rangeReq := dtos.ThingModelPropertyDataRequest{
		BaseSearchConditionQuery: dtos.BaseSearchConditionQuery{IsAll: true},
		ThingModelDataBaseRequest: dtos.ThingModelDataBaseRequest{
			Range: []int64{now.Add(-time.Hour).UnixMilli(), now.Add(time.Hour).UnixMilli()},
		},
		DeviceId: deviceID,
		Code:     "dissolved_oxygen",
	}
	rows, count, err := client.GetDeviceProperty(rangeReq, models.Device{Id: deviceID})
	if err != nil {
		t.Fatalf("GetDeviceProperty(range): %v", err)
	}
	if count < 2 || len(rows) < 2 {
		t.Fatalf("expected >=2 rows, got rows=%d count=%d", len(rows), count)
	}
	if rows[0].Value != "3.8" {
		t.Errorf("latest in range should be 3.8 (desc order), got %v", rows[0].Value)
	}

	// Last 语义（实时值）
	lastReq := dtos.ThingModelPropertyDataRequest{
		ThingModelDataBaseRequest: dtos.ThingModelDataBaseRequest{Last: true},
		DeviceId:                  deviceID,
		Code:                      "dissolved_oxygen",
	}
	lastRows, _, err := client.GetDeviceProperty(lastReq, models.Device{Id: deviceID})
	if err != nil {
		t.Fatalf("GetDeviceProperty(last): %v", err)
	}
	if len(lastRows) != 1 || lastRows[0].Value != "3.8" {
		t.Fatalf("expected last=3.8, got %+v", lastRows)
	}

	// 计数接口
	cntReq := rangeReq
	propertyCount, err := client.GetDevicePropertyCount(cntReq)
	if err != nil || propertyCount < 2 {
		t.Fatalf("GetDevicePropertyCount: %v %d", err, propertyCount)
	}
	msgCount, err := client.GetDeviceMsgCountByGiveTime(deviceID, now.Add(-time.Hour).Unix(), now.Add(time.Hour).Unix())
	if err != nil || msgCount < 2 {
		t.Fatalf("GetDeviceMsgCountByGiveTime: %v %d", err, msgCount)
	}

	// 清理
	_ = client.DropTable(ctx, deviceID)
	_ = client.DropStable(ctx, productID)
}
