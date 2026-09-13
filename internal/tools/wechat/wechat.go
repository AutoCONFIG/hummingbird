package wechat

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/winc-link/hummingbird/internal/pkg/logger"
)

// 微信小程序服务端能力封装：code2session 登录 + 订阅消息发送（docs/api-contract.md §2/§3.9）

const (
	code2SessionURL = "https://api.weixin.qq.com/sns/jscode2session"
	stableTokenURL  = "https://api.weixin.qq.com/cgi-bin/stable_token"
	subscribeSendURL = "https://api.weixin.qq.com/cgi-bin/message/subscribe/send?access_token="
)

type Client struct {
	appId     string
	secret    string
	lc        logger.LoggingClient
	http      *http.Client
	mu        sync.Mutex
	token     string
	tokenExp  time.Time
}

func NewWechatClient(appId, secret string, lc logger.LoggingClient) *Client {
	return &Client{
		appId:  appId,
		secret: secret,
		lc:     lc,
		http:   &http.Client{Timeout: 10 * time.Second},
	}
}

type Code2SessionResponse struct {
	OpenId     string `json:"openid"`
	UnionId    string `json:"unionid"`
	SessionKey string `json:"session_key"`
	ErrCode    int    `json:"errcode"`
	ErrMsg     string `json:"errmsg"`
}

// Code2Session wx.login 临时 code 换 openid。AppId 未配置时走开发模式（openid=dev_+code），
// 便于契约联调；生产必须配置 AppId/Secret。
func (c *Client) Code2Session(code string) (*Code2SessionResponse, error) {
	if c.appId == "" {
		return &Code2SessionResponse{OpenId: "dev_" + code}, nil
	}
	url := fmt.Sprintf("%s?appid=%s&secret=%s&js_code=%s&grant_type=authorization_code",
		code2SessionURL, c.appId, c.secret, code)
	body, err := c.get(url)
	if err != nil {
		return nil, err
	}
	var resp Code2SessionResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	if resp.ErrCode != 0 {
		return nil, fmt.Errorf("code2session failed: %d %s", resp.ErrCode, resp.ErrMsg)
	}
	return &resp, nil
}

type stableTokenRequest struct {
	GrantType string `json:"grant_type"`
	AppId     string `json:"appid"`
	Secret    string `json:"secret"`
}

type wechatBaseResponse struct {
	ErrCode int    `json:"errcode"`
	ErrMsg  string `json:"errmsg"`
}

type accessTokenResponse struct {
	wechatBaseResponse
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
}

// cachedAccessToken stable_token 接口，本地缓存提前 5 分钟过期
func (c *Client) cachedAccessToken() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && time.Now().Before(c.tokenExp) {
		return c.token, nil
	}
	payload, _ := json.Marshal(stableTokenRequest{
		GrantType: "client_credential",
		AppId:     c.appId,
		Secret:    c.secret,
	})
	resp, err := c.http.Post(stableTokenURL, "application/json", strings.NewReader(string(payload)))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var token accessTokenResponse
	if err = json.NewDecoder(resp.Body).Decode(&token); err != nil {
		return "", err
	}
	if token.ErrCode != 0 {
		return "", fmt.Errorf("stable_token failed: %d %s", token.ErrCode, token.ErrMsg)
	}
	c.token = token.AccessToken
	c.tokenExp = time.Now().Add(time.Duration(token.ExpiresIn-300) * time.Second)
	return c.token, nil
}

type subscribeMessage struct {
	ToUser     string                       `json:"touser"`
	TemplateId string                       `json:"template_id"`
	Page       string                       `json:"page,omitempty"`
	Data       map[string]subscribeDataItem `json:"data"`
}

type subscribeDataItem struct {
	Value string `json:"value"`
}

type subscribeSendResponse struct {
	wechatBaseResponse
}

// SubscribeSend 发送订阅消息。返回值：
//   - (true, nil)  发送成功（调用方扣减配额）
//   - (false, nil) 用户未订阅/已取消（43101，调用方清零配额）
//   - (false, err) 其他错误（配额保留，待重试）
func (c *Client) SubscribeSend(openId, templateId, page string, data map[string]string) (bool, error) {
	token, err := c.cachedAccessToken()
	if err != nil {
		return false, err
	}
	msg := subscribeMessage{ToUser: openId, TemplateId: templateId, Page: page, Data: map[string]subscribeDataItem{}}
	for k, v := range data {
		msg.Data[k] = subscribeDataItem{Value: v}
	}
	payload, _ := json.Marshal(msg)
	resp, err := c.http.Post(subscribeSendURL+token, "application/json", strings.NewReader(string(payload)))
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	var result subscribeSendResponse
	if err = json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return false, err
	}
	switch {
	case result.ErrCode == 0:
		return true, nil
	case result.ErrCode == 43101:
		return false, nil
	default:
		return false, fmt.Errorf("subscribe send failed: %d %s", result.ErrCode, result.ErrMsg)
	}
}

func (c *Client) get(url string) ([]byte, error) {
	resp, err := c.http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body := make([]byte, 0)
	buf := make([]byte, 1024)
	for {
		n, err := resp.Body.Read(buf)
		body = append(body, buf[:n]...)
		if err != nil {
			break
		}
	}
	return body, nil
}
