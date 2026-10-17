package repository

import (
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// NewUpstreamBalanceHTTP gives admin probes a separate, small client cache.
// Its connections, eviction and in-flight counts never touch inference clients.
// Reuse the existing transport implementation to preserve proxy and URL policy.
func NewUpstreamBalanceHTTP(cfg *config.Config) *service.UpstreamBalanceHTTP {
	isolated := config.Config{}
	if cfg != nil {
		isolated = *cfg
	}
	isolated.Gateway.MaxUpstreamClients = 16
	isolated.Gateway.MaxIdleConns = 4
	isolated.Gateway.MaxIdleConnsPerHost = 1
	isolated.Gateway.MaxConnsPerHost = 4
	isolated.Gateway.IdleConnTimeoutSeconds = 30
	isolated.Gateway.ClientIdleTTLSeconds = 60
	return &service.UpstreamBalanceHTTP{HTTPUpstream: NewHTTPUpstream(&isolated)}
}
