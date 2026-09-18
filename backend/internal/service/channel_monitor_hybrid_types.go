package service

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

type HybridMonitorRule struct {
	ID                 string  `json:"id"`
	Name               string  `json:"name"`
	GroupID            int64   `json:"group_id"`
	MonitorID          int64   `json:"monitor_id"`
	Model              string  `json:"model"`
	Enabled            bool    `json:"enabled"`
	Public             bool    `json:"public"`
	Muted              bool    `json:"muted"`
	RedSuccess         float64 `json:"red_success"`
	GreenSuccess       float64 `json:"green_success"`
	WarningTTFT        int64   `json:"warning_ttft_ms"`
	CriticalTTFT       int64   `json:"critical_ttft_ms"`
	MinimumTTFTSamples int     `json:"minimum_ttft_samples"`
}

type HybridMonitorConfig struct {
	Version              int                 `json:"version"`
	Enabled              bool                `json:"enabled"`
	NotificationsEnabled bool                `json:"notifications_enabled"`
	Rules                []HybridMonitorRule `json:"rules"`
	Webhook              string              `json:"webhook,omitempty"`
	Secret               string              `json:"secret,omitempty"`
	ClearCredentials     bool                `json:"clear_credentials,omitempty"`
	WebhookConfigured    bool                `json:"webhook_configured"`
	SecretConfigured     bool                `json:"secret_configured"`
}

type HybridMonitorMinute struct {
	Minute           time.Time `json:"minute"`
	ObservedAt       time.Time `json:"observed_at"`
	Source           string    `json:"source"`
	Status           string    `json:"status"`
	Reasons          []string  `json:"reasons"`
	SuccessRate      *float64  `json:"success_rate"`
	AlertSuccessRate *float64  `json:"alert_success_rate,omitempty"`
	Requests         int64     `json:"requests,omitempty"`
	Successes        int64     `json:"successes,omitempty"`
	Failures         int64     `json:"failures,omitempty"`
	Excluded         int64     `json:"excluded,omitempty"`
	TTFTMs           *int64    `json:"ttft_ms"`
	TTFTSamples      int       `json:"ttft_samples,omitempty"`
	TTFTSufficient   bool      `json:"ttft_sufficient"`
	Detail           string    `json:"detail,omitempty"`
	Finalized        bool      `json:"finalized"`
}

type HybridMonitorState struct {
	Minute     time.Time `json:"minute"`
	RedCount   int       `json:"red_count"`
	GreenCount int       `json:"green_count"`
	Incident   bool      `json:"incident"`
	Generation int       `json:"generation"`
	BlindCount int       `json:"blind_count"`
	BlindSpot  bool      `json:"blind_spot"`
}

type HybridMonitorItem struct {
	Rule    HybridMonitorRule     `json:"rule"`
	Minutes []HybridMonitorMinute `json:"minutes"`
	State   *HybridMonitorState   `json:"state,omitempty"`
}

func validateHybridConfig(cfg *HybridMonitorConfig) error {
	if len(cfg.Rules) > 100 {
		return errors.New("最多配置 100 条监控")
	}
	ids := map[string]bool{}
	targets := map[string]bool{}
	for index := range cfg.Rules {
		rule := &cfg.Rules[index]
		rule.ID = strings.TrimSpace(rule.ID)
		rule.Name = strings.TrimSpace(rule.Name)
		rule.Model = strings.TrimSpace(rule.Model)
		if rule.ID == "" || len(rule.ID) > 80 || ids[rule.ID] || rule.Name == "" || len([]rune(rule.Name)) > 100 || rule.GroupID <= 0 || rule.MonitorID <= 0 || rule.Model == "" || len(rule.Model) > 200 {
			return errors.New("监控名称、唯一 ID、真实分组、探测配置和模型不能为空")
		}
		ids[rule.ID] = true
		target := fmt.Sprintf("%d:%s", rule.GroupID, rule.Model)
		if targets[target] {
			return errors.New("同一分组与模型只能配置一条监控")
		}
		targets[target] = true
		if math.IsNaN(rule.RedSuccess) || math.IsNaN(rule.GreenSuccess) || rule.RedSuccess <= 0 || rule.RedSuccess >= rule.GreenSuccess || rule.GreenSuccess > 1 || rule.WarningTTFT <= 0 || rule.CriticalTTFT <= rule.WarningTTFT || rule.CriticalTTFT > 40000 || rule.MinimumTTFTSamples < 1 || rule.MinimumTTFTSamples > 1000 {
			return errors.New("阈值无效：成功率应满足 0 < 红线 < 绿线 ≤ 100%，首 Token 黄线 < 红线 ≤ 40 秒")
		}
	}
	return nil
}

func evaluateHybridMinute(rule HybridMonitorRule, point HybridMonitorMinute) HybridMonitorMinute {
	point.Status = "operational"
	point.Reasons = []string{}
	eligible := point.Successes + point.Failures - point.Excluded
	if point.Source == "passive" {
		if eligible <= 0 || point.Requests <= 0 {
			point.Status = "unknown"
			return point
		}
		actual := float64(point.Successes) / float64(point.Requests)
		rate := float64(point.Successes) / float64(eligible)
		point.SuccessRate = &actual
		point.AlertSuccessRate = &rate
		if rate < rule.RedSuccess {
			point.Status = "failed"
			point.Reasons = append(point.Reasons, "success_rate")
		} else if rate < rule.GreenSuccess {
			point.Status = "degraded"
			point.Reasons = append(point.Reasons, "success_rate")
		}
	}
	point.TTFTSufficient = point.TTFTMs != nil && (point.Source == "active" || point.TTFTSamples >= rule.MinimumTTFTSamples)
	if point.TTFTSufficient {
		if *point.TTFTMs >= rule.CriticalTTFT {
			point.Status = "failed"
			point.Reasons = append(point.Reasons, "ttft")
		} else if *point.TTFTMs >= rule.WarningTTFT {
			if point.Status == "operational" {
				point.Status = "degraded"
			}
			point.Reasons = append(point.Reasons, "ttft")
		}
	}
	return point
}

func advanceHybridState(state HybridMonitorState, point HybridMonitorMinute) (HybridMonitorState, string) {
	if !point.Minute.After(state.Minute) {
		return state, ""
	}
	if point.Minute.Sub(state.Minute) != time.Minute {
		state.RedCount = 0
		state.GreenCount = 0
	}
	state.Minute = point.Minute
	kind := ""
	monitorUnavailable := false
	for _, reason := range point.Reasons {
		monitorUnavailable = monitorUnavailable || reason == "monitor_unavailable"
	}
	if monitorUnavailable {
		state.BlindCount++
		if state.BlindCount >= 3 && !state.BlindSpot {
			state.BlindCount = 3
			state.BlindSpot = true
			kind = "blind_spot"
		}
	} else {
		state.BlindCount = 0
		if state.BlindSpot {
			state.BlindSpot = false
			kind = "blind_recovery"
		}
	}
	switch point.Status {
	case "failed", "error":
		state.GreenCount = 0
		state.RedCount++
		if state.RedCount >= 3 && !state.Incident {
			state.Incident = true
			state.Generation++
			return state, "incident"
		}
	case "operational":
		state.RedCount = 0
		state.GreenCount++
		if state.GreenCount >= 2 && state.Incident {
			state.Incident = false
			return state, "recovery"
		}
	default:
		state.RedCount = 0
		state.GreenCount = 0
	}
	if state.RedCount > 3 {
		state.RedCount = 3
	}
	if state.GreenCount > 2 {
		state.GreenCount = 2
	}
	return state, kind
}
