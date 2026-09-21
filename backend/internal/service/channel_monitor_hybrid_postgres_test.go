package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func TestHybridPostgresPersistence(t *testing.T) {
	dsn := os.Getenv("HYBRID_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set HYBRID_TEST_DATABASE_URL to an isolated local test database")
	}
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	defer db.Close()
	db.SetMaxOpenConns(1)
	schema := "hybrid_test_" + uuid.NewString()[:8]
	_, err = db.Exec("CREATE SCHEMA " + schema)
	require.NoError(t, err)
	defer db.Exec("DROP SCHEMA " + schema + " CASCADE")
	_, err = db.Exec("SET search_path TO " + schema)
	require.NoError(t, err)
	_, err = db.Exec(`CREATE TABLE usage_logs(id bigserial,request_id text,first_token_ms bigint,group_id bigint,requested_model text,model text,api_key_id bigint,created_at timestamptz,actual_cost numeric,input_tokens bigint,output_tokens bigint,cache_read_tokens bigint,cache_creation_tokens bigint,request_type integer);
 CREATE TABLE ops_error_logs(id bigserial,request_id text,client_request_id text,error_owner text,status_code integer,upstream_status_code integer,error_type text,error_message text,group_id bigint,requested_model text,model text,api_key_id bigint,created_at timestamptz,is_count_tokens boolean,request_type integer);`)
	require.NoError(t, err)
	migration, err := os.ReadFile(filepath.Join("..", "..", "migrations", "239_channel_monitor_hybrid.sql"))
	require.NoError(t, err)
	_, err = db.Exec(string(migration))
	require.NoError(t, err)
	_, err = db.Exec(string(migration))
	require.NoError(t, err)
	ctx := context.Background()
	svc := &HybridMonitorService{db: db}
	rule := hybridTestRule()
	cfg := HybridMonitorConfig{Version: 1, Enabled: true, NotificationsEnabled: true, Rules: []HybridMonitorRule{rule}}
	raw, _ := json.Marshal(cfg)
	_, err = db.Exec("UPDATE channel_monitor_hybrid_config SET config=$1", string(raw))
	require.NoError(t, err)
	minute := time.Now().UTC().Truncate(time.Minute).Add(-10 * time.Minute)
	_, err = db.Exec(`INSERT INTO usage_logs(request_id,first_token_ms,group_id,model,api_key_id,created_at,actual_cost,input_tokens,output_tokens,cache_read_tokens,cache_creation_tokens,request_type) VALUES('success',1200,1,'model',8,$1,0,4,2,0,0,0),('probe',1200,1,'model',9,$1,1,4,2,0,0,0)`, minute)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO ops_error_logs(request_id,client_request_id,error_owner,status_code,upstream_status_code,error_type,error_message,group_id,model,api_key_id,created_at,is_count_tokens,request_type) VALUES('failure','','provider',503,503,'upstream','unavailable',1,'model',8,$1,false,2),('client:success','','provider',503,503,'upstream','retry',1,'model',8,$1,false,2)`, minute)
	require.NoError(t, err)
	passive, err := svc.passive(ctx, rule, 9, minute)
	require.NoError(t, err)
	require.EqualValues(t, 2, passive.Requests)
	require.EqualValues(t, 0, passive.Successes)
	require.EqualValues(t, 2, passive.Failures)
	for index := 0; index < 3; index++ {
		point := HybridMonitorMinute{Minute: minute.Add(time.Duration(index) * time.Minute), ObservedAt: time.Now(), Source: "passive", Status: "failed", Reasons: []string{"success_rate"}}
		require.NoError(t, svc.persist(ctx, cfg, rule, point))
		require.NoError(t, svc.persist(ctx, cfg, rule, point))
	}
	restarted := &HybridMonitorService{db: db}
	_, items, err := restarted.Snapshot(ctx, true, nil)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.True(t, items[0].State.Incident)
	require.Len(t, items[0].Minutes, 3)
	notifications, err := restarted.Notifications(ctx)
	require.NoError(t, err)
	require.Len(t, notifications, 1)
	_, err = db.Exec("UPDATE channel_monitor_hybrid_notifications SET sent_at=NOW() WHERE rule_id=$1 AND kind='incident'", rule.ID)
	require.NoError(t, err)
	_, items, err = restarted.Snapshot(ctx, false, []int64{2})
	require.NoError(t, err)
	require.Empty(t, items)
	_, items, err = restarted.Snapshot(ctx, false, []int64{1})
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Nil(t, items[0].State)
	require.Zero(t, items[0].Rule.MonitorID)
	for index := 3; index < 5; index++ {
		require.NoError(t, restarted.persist(ctx, cfg, rule, HybridMonitorMinute{Minute: minute.Add(time.Duration(index) * time.Minute), ObservedAt: time.Now(), Source: "active", Status: "operational"}))
	}
	notifications, err = restarted.Notifications(ctx)
	require.NoError(t, err)
	require.Len(t, notifications, 2)
	require.Equal(t, "recovery", notifications[0]["kind"])
	cfg.Version = 2
	require.ErrorIs(t, restarted.persist(ctx, cfg, rule, HybridMonitorMinute{Minute: minute.Add(6 * time.Minute)}), ErrHybridConflict)
	cfg.Version = 1
	cfg.Enabled = false
	saved, err := restarted.Save(ctx, cfg)
	require.NoError(t, err)
	require.Equal(t, 2, saved.Version)
	require.False(t, saved.Enabled)
	_, err = restarted.Save(ctx, cfg)
	require.ErrorIs(t, err, ErrHybridConflict)
	var remaining int
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM channel_monitor_hybrid_states").Scan(&remaining))
	require.Zero(t, remaining)
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM channel_monitor_hybrid_notifications WHERE sent_at IS NULL").Scan(&remaining))
	require.Zero(t, remaining)
	publicConfig, publicItems, err := restarted.Snapshot(ctx, false, []int64{1})
	require.NoError(t, err)
	require.Empty(t, publicItems)
	require.Empty(t, publicConfig.Rules)
}
