package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestShouldRecordRequestBodySnapshot(t *testing.T) {
	u4, u5 := int64(4), int64(5)
	cfg := OpsAdvancedSettings{RecordRequestBodyOnError: true, RequestBodySnapshotExcludeUserIDs: []int64{4, 9}}

	if cfg.ShouldRecordRequestBodySnapshot(&u4) {
		t.Fatal("excluded user 4 must not be recorded")
	}
	if !cfg.ShouldRecordRequestBodySnapshot(&u5) {
		t.Fatal("user 5 is not excluded and must be recorded")
	}
	if !cfg.ShouldRecordRequestBodySnapshot(nil) {
		t.Fatal("unknown user follows the global switch")
	}

	cfg.RecordRequestBodyOnError = false
	if cfg.ShouldRecordRequestBodySnapshot(&u5) || cfg.ShouldRecordRequestBodySnapshot(nil) {
		t.Fatal("global switch off disables every snapshot")
	}
}

func TestNormalizeOpsAdvancedSettings_SnapshotExcludeUserIDs(t *testing.T) {
	cfg := defaultOpsAdvancedSettings()
	cfg.RequestBodySnapshotExcludeUserIDs = []int64{57, 4, 0, -3, 4, 57}
	normalizeOpsAdvancedSettings(cfg)
	got := cfg.RequestBodySnapshotExcludeUserIDs
	if len(got) != 2 || got[0] != 4 || got[1] != 57 {
		t.Fatalf("normalized ids = %v, want [4 57]", got)
	}

	// 旧数据没有该字段：归一化后输出 []，而不是 null。
	legacy := defaultOpsAdvancedSettings()
	if err := json.Unmarshal([]byte(`{"record_request_body_on_error":true}`), legacy); err != nil {
		t.Fatal(err)
	}
	normalizeOpsAdvancedSettings(legacy)
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"request_body_snapshot_exclude_user_ids":[]`) {
		t.Fatalf("expected empty list in JSON, got %s", raw)
	}
}

func TestUpdateOpsAdvancedSettings_RejectsHugeSnapshotExcludeList(t *testing.T) {
	svc := &OpsService{settingRepo: newRuntimeSettingRepoStub()}
	cfg := defaultOpsAdvancedSettings()
	for i := int64(1); i <= opsRequestBodySnapshotExcludeUserIDsMax+1; i++ {
		cfg.RequestBodySnapshotExcludeUserIDs = append(cfg.RequestBodySnapshotExcludeUserIDs, i)
	}
	if _, err := svc.UpdateOpsAdvancedSettings(context.Background(), cfg); err == nil {
		t.Fatal("expected an error for an oversized exclude list")
	}
}
