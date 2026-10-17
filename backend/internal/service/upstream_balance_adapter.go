package service

// New API and Sub2API request/field mappings are adapted from CC Switch's
// UsageScriptModal.tsx (MIT). See docs/licenses/cc-switch.txt.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

type UpstreamBalanceData struct {
	Scope     string   `json:"scope"` // account, token, or subscription; never interchangeable
	Remaining *float64 `json:"remaining,omitempty"`
	Used      *float64 `json:"used,omitempty"`
	Unit      string   `json:"unit"`
	Unlimited bool     `json:"unlimited,omitempty"`
	Source    string   `json:"source"`
}

func upstreamBalanceRoot(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && u.Scheme != "http") {
		return "", errors.New("无效的上游地址")
	}
	u.Host = strings.ToLower(u.Host)
	u.Path = strings.TrimSuffix(strings.TrimRight(u.Path, "/"), "/v1")
	u.RawPath = ""
	return strings.TrimRight(u.String(), "/"), nil
}

func balanceNumber(v gjson.Result) *float64 {
	if v.Type != gjson.Number && v.Type != gjson.String {
		return nil
	}
	f, err := strconv.ParseFloat(v.String(), 64)
	if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
		return nil
	}
	return &f
}

func parseSub2APIBalance(body []byte) (*UpstreamBalanceData, error) {
	if !json.Valid(body) {
		return nil, errors.New("上游未返回 JSON")
	}
	r := gjson.ParseBytes(body)
	if r.Get("isValid").Exists() && !r.Get("isValid").Bool() {
		return nil, errors.New("上游密钥无效")
	}
	unit := r.Get("unit").String()
	if unit == "" {
		unit = r.Get("quota.unit").String()
	}
	if unit == "" {
		unit = "USD"
	}
	// A subscription response can also contain a balance. Its spendable quota
	// belongs to the subscription, not a shared wallet.
	if r.Get("mode").String() == "quota_limited" || r.Get("quota.limit").Float() > 0 || r.Get("subscription").IsObject() || r.Get("mode").String() == "subscription" {
		scope := "token"
		if r.Get("subscription").IsObject() || r.Get("mode").String() == "subscription" {
			scope = "subscription"
		}
		d := &UpstreamBalanceData{Scope: scope, Unit: unit, Source: "sub2api"}
		d.Remaining = balanceNumber(r.Get("remaining"))
		d.Used = balanceNumber(r.Get("quota.used"))
		if d.Remaining == nil {
			d.Remaining = balanceNumber(r.Get("quota.remaining"))
		}
		if d.Remaining == nil {
			return nil, errors.New("上游未返回剩余额度")
		}
		if scope == "subscription" && *d.Remaining == -1 {
			d.Remaining = nil
			d.Unlimited = true
		}
		return d, nil
	}
	balance := balanceNumber(r.Get("balance"))

	if balance == nil || !r.Get("isValid").Exists() && !r.Get("mode").Exists() {
		return nil, errors.New("未识别到 Sub2API 余额")
	}
	return &UpstreamBalanceData{Scope: "account", Remaining: balance, Unit: unit, Source: "sub2api"}, nil
}

func parseNewAPIBalance(body, status []byte, panel bool) (*UpstreamBalanceData, error) {
	if !json.Valid(body) {
		return nil, errors.New("上游未返回 JSON")
	}
	r := gjson.ParseBytes(body)
	if !r.Get("success").Bool() {
		return nil, errors.New("上游 New API 认证失败，请检查访问令牌及用户 ID")
	}
	var remaining, used *float64
	d := &UpstreamBalanceData{Scope: "account", Source: "new_api", Unit: "QUOTA"}
	if panel {
		remaining, used = balanceNumber(r.Get("data.quota")), balanceNumber(r.Get("data.used_quota"))
	} else if r.Get("data.user_quota").IsObject() {
		remaining, used = balanceNumber(r.Get("data.user_quota.total_available")), balanceNumber(r.Get("data.user_quota.total_used"))
	} else {
		d.Scope = "token"
		d.Unlimited = r.Get("data.unlimited_quota").Bool()
		remaining, used = balanceNumber(r.Get("data.total_available")), balanceNumber(r.Get("data.total_used"))
	}
	if remaining == nil && !d.Unlimited {
		return nil, errors.New("上游 New API 未返回有效余额字段")
	}
	// Read the site's conversion metadata; CC Switch's fixed 500000 would be
	// incorrect on customized installations. Unknown units stay raw quota.
	meta := gjson.ParseBytes(status).Get("data")
	divisor := balanceNumber(meta.Get("quota_per_unit"))
	if divisor != nil && *divisor > 0 && meta.Get("quota_display_type").String() == "USD" {
		d.Unit = "USD"
		if remaining != nil {
			*remaining /= *divisor
		}
		if used != nil {
			*used /= *divisor
		}
		if remaining != nil && math.IsInf(*remaining, 0) || used != nil && math.IsInf(*used, 0) {
			return nil, errors.New("上游额度换算参数无效")
		}
	}
	d.Remaining, d.Used = remaining, used
	if d.Unlimited {
		d.Remaining = nil
	}
	return d, nil
}

func (s *UpstreamBalanceService) fetchBalanceJSON(ctx context.Context, a *Account, root, path, token, userID string) ([]byte, error) {
	target, err := cnValidateProbeURL(s.cfg, root+path)
	if err != nil {
		return nil, errors.New("上游地址被出站安全策略拒绝")
	}
	ctx = WithHTTPUpstreamRedirectsDisabled(ctx)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, errors.New("无法创建查询请求")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "BirdSub2Api-Balance/1.0")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if userID != "" {
		req.Header.Set("New-Api-User", userID)
	}
	proxyURL := ""
	if a.ProxyID != nil {
		proxy, err := s.proxies.GetByID(ctx, *a.ProxyID)
		if err != nil || proxy == nil {
			return nil, errors.New("无法读取账号代理")
		}
		proxyURL = proxy.URL()
	}
	resp, err := s.http.Do(req, proxyURL, a.ID, 1)
	if err != nil {
		return nil, errors.New("余额查询连接失败或超时")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("余额接口返回 HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 128*1024+1))
	if err != nil || len(body) > 128*1024 {
		return nil, errors.New("余额响应过大或读取失败")
	}
	if !json.Valid(body) {
		return nil, errors.New("该地址未返回余额 JSON（可能不支持此接口）")
	}
	return body, nil
}

func (s *UpstreamBalanceService) query(ctx context.Context, a *Account, cfg balanceStoredConfig) (*UpstreamBalanceData, error) {
	root, err := upstreamBalanceRoot(a.GetCredential("base_url"))
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	key := a.GetCredential("api_key")
	if cfg.Mode == "new_api" {
		if cfg.Token == "" {
			return nil, errors.New("请配置 New API 访问令牌")
		}
		token, err := s.encryptor.Decrypt(cfg.Token)
		if err != nil {
			return nil, errors.New("无法解密访问令牌，请重新配置")
		}
		body, err := s.fetchBalanceJSON(ctx, a, root, "/api/user/self", token, cfg.UserID)
		if err != nil {
			return nil, err
		}
		status, _ := s.fetchBalanceJSON(ctx, a, root, "/api/status", "", "")
		return parseNewAPIBalance(body, status, true)
	}
	if cfg.Mode == "auto" || cfg.Mode == "sub2api" {
		body, err := s.fetchBalanceJSON(ctx, a, root, "/v1/usage", key, "")
		if err == nil {
			if data, parseErr := parseSub2APIBalance(body); parseErr == nil {
				return data, nil
			} else {
				err = parseErr
			}
		}
		if cfg.Mode == "sub2api" {
			return nil, err
		}
	}
	body, err := s.fetchBalanceJSON(ctx, a, root, "/api/usage/token/", key, "")
	if err != nil {
		return nil, errors.New("未能自动查询；New API 可配置访问令牌后重试")
	}
	status, _ := s.fetchBalanceJSON(ctx, a, root, "/api/status", "", "")
	return parseNewAPIBalance(body, status, false)
}
