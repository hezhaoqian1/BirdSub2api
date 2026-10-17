package repository

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestUpstreamBalanceHTTPDoesNotCompeteWithActiveInferenceConnection(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/stream" {
			_, _ = io.WriteString(w, "data: first\n\n")
			flusher, ok := w.(http.Flusher)
			require.True(t, ok)
			flusher.Flush()
			select {
			case <-release:
			case <-r.Context().Done():
			}
			return
		}
		_, _ = io.WriteString(w, `{"balance":10}`)
	}))
	t.Cleanup(server.Close)
	t.Cleanup(func() { close(release) })
	cfg := &config.Config{}
	cfg.Gateway.ConnectionPoolIsolation = config.ConnectionPoolIsolationAccount
	cfg.Gateway.MaxUpstreamClients = 1
	gateway, ok := NewHTTPUpstream(cfg).(*httpUpstreamService)
	require.True(t, ok)
	balance, ok := NewUpstreamBalanceHTTP(cfg).HTTPUpstream.(*httpUpstreamService)
	require.True(t, ok)
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL+"/stream", nil)
	require.NoError(t, err)
	stream, err := gateway.Do(request, "", 7, 1)
	require.NoError(t, err)
	t.Cleanup(func() { _ = stream.Body.Close() })
	first := make([]byte, len("data: first\n\n"))
	_, err = io.ReadFull(stream.Body, first)
	require.NoError(t, err)
	var gatewayEntry *upstreamClientEntry
	for _, entry := range gateway.clients {
		gatewayEntry = entry
	}
	require.NotNil(t, gatewayEntry)
	require.Equal(t, int64(1), atomic.LoadInt64(&gatewayEntry.inFlight))

	// With a shared one-connection pool, this request cannot finish until the
	// stream closes. The isolated probe must finish while the stream stays open.
	probeCtx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	request, err = http.NewRequestWithContext(probeCtx, http.MethodGet, server.URL+"/balance", nil)
	require.NoError(t, err)
	response, err := balance.Do(request, "", 7, 1)
	require.NoError(t, err)
	_, err = io.ReadAll(response.Body)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	require.Equal(t, int64(1), atomic.LoadInt64(&gatewayEntry.inFlight))

	// Probe cache eviction must neither change gateway settings nor evict its
	// active transport, even when more balance accounts than the cache limit run.
	for id := int64(20); id < 40; id++ {
		_, err = balance.getOrCreateClient("", id, 1)
		require.NoError(t, err)
	}
	require.Len(t, balance.clients, 16)
	require.Len(t, gateway.clients, 1)
	for _, entry := range gateway.clients {
		require.Same(t, gatewayEntry, entry)
	}
	require.Equal(t, 1, cfg.Gateway.MaxUpstreamClients)
}
