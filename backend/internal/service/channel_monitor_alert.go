package service

import (
	"context"
	"time"
)

// 渠道监控主模型连续失败达到阈值时推送一次告警，恢复后再推送一条恢复通知。
// 推送走安全告警的企业微信机器人（运营告警），实现见 security_alert_channel_monitor.go。

const (
	channelMonitorAlertThreshold    = 3
	channelMonitorAlertTimeout      = 8 * time.Second
	channelMonitorAlertMessageRunes = 200
)

type channelMonitorFailureAlert struct {
	MonitorID           int64
	MonitorName         string
	Model               string
	Status              string
	Message             string
	CheckedAt           time.Time
	ConsecutiveFailures int
}

type channelMonitorRecoveryAlert struct {
	MonitorID   int64
	MonitorName string
	Model       string
	Status      string
	LatencyMs   *int
	CheckedAt   time.Time
}

// channelMonitorAlertNotifier 返回 handled=true 表示这次状态变化已经落定（已推送，
// 或其他实例已推送过）；false 表示没有处理（如告警关闭），下次检测会再尝试。
type channelMonitorAlertNotifier interface {
	NotifyFailure(ctx context.Context, alert channelMonitorFailureAlert) (bool, error)
	NotifyRecovery(ctx context.Context, alert channelMonitorRecoveryAlert) (bool, error)
	// Forget 监控被停用或删除时清掉它未恢复的告警记录，重新启用后按新故障处理。
	Forget(ctx context.Context, monitorID int64)
}
