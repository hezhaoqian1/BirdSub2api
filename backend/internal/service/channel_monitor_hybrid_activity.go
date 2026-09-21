package service

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
)

const HybridMonitorProbeHeader = "X-Sub2API-Monitor-Probe"

type hybridGroupActivity struct {
	inFlight     atomic.Int64
	lastActivity atomic.Int64
}

var hybridBusinessActivity sync.Map
var hybridProbeKeysMu sync.RWMutex
var hybridProbeKeys = map[int64]int64{}

func hybridActivityFor(groupID int64) *hybridGroupActivity {
	value, _ := hybridBusinessActivity.LoadOrStore(groupID, &hybridGroupActivity{})
	return value.(*hybridGroupActivity)
}

func BeginHybridBusinessRequest(groupID int64) func() {
	activity := hybridActivityFor(groupID)
	activity.lastActivity.Store(time.Now().UTC().UnixNano())
	activity.inFlight.Add(1)
	return func() {
		activity.lastActivity.Store(time.Now().UTC().UnixNano())
		activity.inFlight.Add(-1)
	}
}

func HybridBusinessActivity(groupID int64) (int64, time.Time) {
	activity := hybridActivityFor(groupID)
	nanos := activity.lastActivity.Load()
	if nanos == 0 {
		return activity.inFlight.Load(), time.Time{}
	}
	return activity.inFlight.Load(), time.Unix(0, nanos).UTC()
}

func HybridBusinessActivitySnapshot(visit func(groupID int64, inFlight int64, lastActivity time.Time)) {
	hybridBusinessActivity.Range(func(key, value any) bool {
		groupID := key.(int64)
		activity := value.(*hybridGroupActivity)
		_, lastActivity := HybridBusinessActivity(groupID)
		visit(groupID, activity.inFlight.Load(), lastActivity)
		return true
	})
}

func RegisterHybridMonitorProbeKey(keyID, groupID int64) {
	if keyID > 0 && groupID > 0 {
		hybridProbeKeysMu.Lock()
		hybridProbeKeys[keyID] = groupID
		hybridProbeKeysMu.Unlock()
	}
}

func IsRegisteredHybridMonitorProbeKey(keyID, groupID int64) bool {
	hybridProbeKeysMu.RLock()
	registered, ok := hybridProbeKeys[keyID]
	hybridProbeKeysMu.RUnlock()
	return ok && registered == groupID
}

func ReplaceHybridMonitorProbeKeys(keys map[int64]int64) {
	next := make(map[int64]int64, len(keys))
	for keyID, groupID := range keys {
		next[keyID] = groupID
	}
	hybridProbeKeysMu.Lock()
	hybridProbeKeys = next
	hybridProbeKeysMu.Unlock()
}

func IsHybridMonitorProbe(request *http.Request) bool {
	return request != nil && request.Header.Get(HybridMonitorProbeHeader) == "1"
}

func WithHybridMonitorProbe(ctx context.Context) context.Context {
	return context.WithValue(ctx, ctxkey.HybridMonitorProbe, true)
}

func IsHybridMonitorProbeContext(ctx context.Context) bool {
	probe, _ := ctx.Value(ctxkey.HybridMonitorProbe).(bool)
	return probe
}
