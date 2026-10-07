package handler

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// burst503Upstream mimics a relay that rejects every request with an empty-body
// 503 for a while (its load balancer has no healthy backend) and then recovers.
type burst503Upstream struct {
	service.HTTPUpstream
	mu        sync.Mutex
	failFirst int
	calls     []int64
	at        []time.Time
}

func (u *burst503Upstream) Do(_ *http.Request, _ string, accountID int64, _ int) (*http.Response, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.calls = append(u.calls, accountID)
	u.at = append(u.at, time.Now())
	if len(u.calls) <= u.failFirst {
		return &http.Response{
			StatusCode: http.StatusServiceUnavailable,
			Header:     http.Header{"Content-Length": []string{"0"}},
			Body:       io.NopCloser(strings.NewReader("")),
		}, nil
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(
			`{"id":"resp_ok","object":"response","model":"gpt-5.2","status":"completed","usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`)),
	}, nil
}

func TestPoolMode503BurstRecoversOnSameAccountWithBackoff(t *testing.T) {
	gin.SetMode(gin.TestMode)
	groupID := int64(4303)
	accounts := []service.Account{
		{
			ID: 9920, Name: "relay-pool", Platform: service.PlatformOpenAI,
			Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Priority: 1,
			Credentials: map[string]any{
				"api_key":                      "sk-relay",
				"base_url":                     "https://relay.example.test",
				"pool_mode":                    true,
				"pool_mode_retry_count":        float64(3),
				"pool_mode_retry_status_codes": []any{float64(http.StatusBadGateway), float64(http.StatusServiceUnavailable)},
			},
			Extra: map[string]any{"openai_passthrough": true},
		},
		{
			ID: 9921, Name: "same-relay-second-key", Platform: service.PlatformOpenAI,
			Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Priority: 100,
			Credentials: map[string]any{"api_key": "sk-relay-2", "base_url": "https://relay.example.test"},
			Extra:       map[string]any{"openai_passthrough": true},
		},
	}
	cfg := &config.Config{RunMode: config.RunModeSimple}
	cfg.Default.RateMultiplier = 1
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Gateway.MaxAccountSwitches = 2

	accountRepo := &openAIWSFailoverHandlerAccountRepoStub{accounts: accounts}
	upstream := &burst503Upstream{failFirst: 2}
	billingCacheSvc := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billingCacheSvc.Stop)
	gatewaySvc := service.NewOpenAIGatewayService(
		accountRepo, nil, nil, nil, nil, nil, nil, cfg, nil, nil,
		service.NewBillingService(cfg, nil), nil, billingCacheSvc, upstream,
		&service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil,
	)
	h := NewOpenAIGatewayHandler(
		gatewaySvc, service.NewConcurrencyService(nil), billingCacheSvc,
		service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg), nil, nil, nil, nil, cfg,
	)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses", strings.NewReader(`{"model":"gpt-5.2","input":"hello","stream":false}`))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{
		ID: 1804, GroupID: &groupID,
		User:  &service.User{ID: 1704, Status: service.StatusActive},
		Group: &service.Group{ID: groupID, Platform: service.PlatformOpenAI, Status: service.StatusActive},
	})
	c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 1704, Concurrency: 0})

	h.Responses(c)

	// Two empty 503s, then success — all on the pool account, no failover to the second key.
	require.Equal(t, []int64{9920, 9920, 9920}, upstream.calls)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "resp_ok", gjson.GetBytes(rec.Body.Bytes(), "id").String())

	// Retry gaps grow: ~0.5s before the 1st retry, ~1s before the 2nd.
	gap1 := upstream.at[1].Sub(upstream.at[0])
	gap2 := upstream.at[2].Sub(upstream.at[1])
	require.GreaterOrEqual(t, gap1, 450*time.Millisecond, "gap1=%s", gap1)
	require.GreaterOrEqual(t, gap2, 950*time.Millisecond, "gap2=%s", gap2)
	require.Greater(t, gap2, gap1)
}
