// kingfisher-sim 水质监测终端模拟器（docs/device-protocol.md 协议）
// 用法：
//   单次上报:  kingfisher-sim -broker tcp://127.0.0.1:58090 -sn WATER001 -once
//   持续上报:  kingfisher-sim -sn WATER001 -interval 300s
//   N 台设备:  kingfisher-sim -sn WATER -count 100 -interval 60s
//   模拟离线:  kingfisher-sim -sn WATER001 -offline （retained LWT）
package main

import (
	"flag"
	"fmt"
	"math/rand"
	"strconv"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

func main() {
	broker := flag.String("broker", "tcp://127.0.0.1:58090", "MQTT broker 地址")
	sn := flag.String("sn", "WATER001", "设备 SN 前缀（count>1 时自动追加序号）")
	count := flag.Int("count", 1, "模拟设备数量")
	interval := flag.Duration("interval", 300*time.Second, "上报周期")
	once := flag.Bool("once", false, "每台只上报一帧后退出")
	offline := flag.Bool("offline", false, "发布 retained 离线遗嘱后退出")
	online := flag.Bool("online", false, "发布上线状态后退出")
	flag.Parse()

	rand.Seed(time.Now().UnixNano())
	clients := make([]mqtt.Client, 0, *count)
	for i := 0; i < *count; i++ {
		deviceSn := *sn
		if *count > 1 {
			deviceSn = *sn + "-" + strconv.Itoa(i+1)
		}
		opts := mqtt.NewClientOptions().
			AddBroker(*broker).
			SetClientID("kfsim-" + deviceSn).
			SetUsername("sim").
			SetPassword("sim").
			SetAutoReconnect(true).
			SetConnectRetry(true).
			SetConnectRetryInterval(2 * time.Second)
		client := mqtt.NewClient(opts)
		if token := client.Connect(); token.Wait() && token.Error() != nil {
			fmt.Printf("[%s] connect failed: %v\n", deviceSn, token.Error())
			continue
		}
		clients = append(clients, client)

		if *offline {
			publish(client, deviceSn, "status", `{"status":"offline"}`, true)
			continue
		}
		if *online {
			publish(client, deviceSn, "status", `{"status":"online"}`, false)
			continue
		}

		report := func() {
			payload := genPayload()
			publish(client, deviceSn, "report", payload, false)
		}
		report()
		if *once {
			continue
		}
		go func(c mqtt.Client, s string) {
			ticker := time.NewTicker(*interval)
			for range ticker.C {
				payload := genPayload()
				publish(c, s, "report", payload, false)
			}
		}(client, deviceSn)
	}
	if !*once {
		select {} // 常驻
	}
	fmt.Printf("done, %d device(s)\n", *count)
}

func publish(client mqtt.Client, sn, kind, payload string, retain bool) {
	topic := fmt.Sprintf("water/%s/%s", sn, kind)
	token := client.Publish(topic, 1, retain, payload)
	token.Wait()
	if token.Error() != nil {
		fmt.Printf("[%s/%s] publish failed: %v\n", sn, kind, token.Error())
		return
	}
	fmt.Printf("[%s/%s] %s\n", sn, kind, payload)
}

// genPayload 生成一帧水质数据（溶解氧模拟 3.5~7.5，便于触发低氧告警测试）
func genPayload() string {
	v := func(min, max float64) string {
		return strconv.FormatFloat(min+rand.Float64()*(max-min), 'f', 2, 64)
	}
	return fmt.Sprintf(`{"temperature":%s,"dissolved_oxygen":%s,"ph":%s,"turbidity":%s,"salinity":%s,"battery":3.9,"signal":-67}`,
		v(24, 30), v(3.5, 7.5), v(7.0, 8.5), v(5, 40), v(25, 31))
}
