package service

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

func signedHybridWebhook(raw, secret string, now time.Time) (string, error) {
	target, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || target.Scheme != "https" || !strings.EqualFold(target.Host, "oapi.dingtalk.com") || target.Path != "/robot/send" || target.User != nil || target.Query().Get("access_token") == "" {
		return "", errors.New("请填写有效的钉钉 HTTPS 机器人 Webhook")
	}
	if secret != "" {
		timestamp := strconv.FormatInt(now.UnixMilli(), 10)
		mac := hmac.New(sha256.New, []byte(secret))
		_, _ = mac.Write([]byte(timestamp + "\n" + secret))
		query := target.Query()
		query.Set("timestamp", timestamp)
		query.Set("sign", base64.StdEncoding.EncodeToString(mac.Sum(nil)))
		target.RawQuery = query.Encode()
	}
	return target.String(), nil
}

func (s *HybridMonitorService) send(ctx context.Context, message string) error {
	var webhook, secret string
	if err := s.db.QueryRowContext(ctx, "SELECT webhook_encrypted,secret_encrypted FROM channel_monitor_hybrid_config WHERE id=1").Scan(&webhook, &secret); err != nil {
		return err
	}
	if webhook == "" {
		return errors.New("未配置钉钉 Webhook")
	}
	plain, err := s.encryptor.Decrypt(webhook)
	if err != nil {
		return errors.New("Webhook 解密失败")
	}
	if secret != "" {
		secret, err = s.encryptor.Decrypt(secret)
		if err != nil {
			return errors.New("签名密钥解密失败")
		}
	}
	target, err := signedHybridWebhook(plain, secret, time.Now())
	if err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]any{"msgtype": "text", "text": map[string]string{"content": message}})
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return errors.New("无法构造钉钉请求")
	}
	request.Header.Set("Content-Type", "application/json")
	client := newSSRFSafeHTTPClient(8 * time.Second)
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(request)
	if err != nil {
		return errors.New("钉钉请求失败或超时")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return fmt.Errorf("钉钉 HTTP %d", response.StatusCode)
	}
	var result struct {
		Code *int `json:"errcode"`
	}
	if json.NewDecoder(io.LimitReader(response.Body, 65536)).Decode(&result) != nil || result.Code == nil {
		return errors.New("钉钉响应格式无效")
	}
	if *result.Code != 0 {
		return fmt.Errorf("钉钉拒绝消息，错误码 %d", *result.Code)
	}
	return nil
}

func (s *HybridMonitorService) TestNotification(ctx context.Context) error {
	return s.send(ctx, "渠道监控 · 测试通知\n机器人配置正常。这条消息不改变监控状态。")
}

func (s *HybridMonitorService) enqueueNotification(tx *sql.Tx, rule HybridMonitorRule, point HybridMonitorMinute, state HybridMonitorState, kind string) error {
	opposite := ""
	switch kind {
	case "recovery":
		opposite = "incident"
	case "blind_recovery":
		opposite = "blind_spot"
	}
	if opposite != "" {
		result, err := tx.Exec(`UPDATE channel_monitor_hybrid_notifications SET cancelled_at=NOW(),locked_at=NULL,locked_by=''
 WHERE rule_id=$1 AND generation=$2 AND kind=$3 AND sent_at IS NULL AND cancelled_at IS NULL AND locked_at IS NULL`, rule.ID, state.Generation, opposite)
		if err != nil {
			return err
		}
		cancelled, _ := result.RowsAffected()
		if cancelled > 0 {
			return nil
		}
	}
	_, err := tx.Exec(`INSERT INTO channel_monitor_hybrid_notifications(rule_id,minute,kind,message,generation)
 VALUES($1,$2,$3,'',$4) ON CONFLICT(rule_id,minute,kind) DO NOTHING`, rule.ID, point.Minute, kind, state.Generation)
	return err
}

func hybridNotificationMessage(rule HybridMonitorRule, point HybridMonitorMinute, kind string) string {
	label := map[string]string{
		"incident":       "连续三个分钟异常",
		"recovery":       "连续两个分钟恢复正常",
		"blind_spot":     "连续三个分钟监控不可用",
		"blind_recovery": "监控数据恢复",
	}[kind]
	if label == "" {
		label = kind
	}
	message := fmt.Sprintf("渠道监控 · %s\n%s / %s\n分组：%d\n来源：%s\n观察时间：%s\n原因：%s", label, rule.Name, rule.Model, rule.GroupID, point.Source, point.ObservedAt.Format(time.RFC3339), strings.Join(point.Reasons, ", "))
	if point.SuccessRate != nil {
		message += fmt.Sprintf("\n实际成功率：%.1f%%", *point.SuccessRate*100)
	}
	if point.TTFTMs != nil {
		message += fmt.Sprintf("\n首 Token：%.1f 秒", float64(*point.TTFTMs)/1000)
	}
	return message
}

func (s *HybridMonitorService) deliveryTick(parent context.Context) {
	ctx, cancel := context.WithTimeout(parent, 45*time.Second)
	defer cancel()
	cfg, err := s.Config(ctx)
	if err != nil || !cfg.Enabled || !cfg.NotificationsEnabled {
		return
	}
	s.syncRuntimeAlert(ctx)
	s.deliver(ctx, cfg)
}

func (s *HybridMonitorService) syncRuntimeAlert(ctx context.Context) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return
	}
	defer tx.Rollback()
	var heartbeat time.Time
	var alerted bool
	if err = tx.QueryRowContext(ctx, "SELECT heartbeat_at,blind_alerted FROM channel_monitor_hybrid_runtime WHERE id=1 FOR UPDATE").Scan(&heartbeat, &alerted); err != nil {
		return
	}
	stale := time.Since(heartbeat) >= 2*time.Minute
	if stale == alerted {
		return
	}
	kind := "monitor_recovery"
	message := "渠道监控 · 评估任务恢复\n分钟统计与主动探测已恢复运行。"
	if stale {
		kind = "monitor_blind_spot"
		message = "渠道监控 · 评估任务不可用\n已连续两分钟没有新的监控心跳，请检查应用实例和数据库。"
	}
	minute := time.Now().UTC().Truncate(time.Minute)
	if _, err = tx.ExecContext(ctx, `INSERT INTO channel_monitor_hybrid_notifications(rule_id,minute,kind,message,generation)
 VALUES('__monitor__',$1,$2,$3,0) ON CONFLICT(rule_id,minute,kind) DO NOTHING`, minute, kind, message); err != nil {
		return
	}
	if _, err = tx.ExecContext(ctx, "UPDATE channel_monitor_hybrid_runtime SET blind_alerted=$1 WHERE id=1", stale); err != nil {
		return
	}
	_ = tx.Commit()
}

func (s *HybridMonitorService) deliver(ctx context.Context, cfg HybridMonitorConfig) {
	if !cfg.NotificationsEnabled {
		return
	}
	workerID := uuid.NewString()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT n.id,n.rule_id,n.minute,n.kind,n.generation,n.message
 FROM channel_monitor_hybrid_notifications n
 WHERE n.sent_at IS NULL AND n.cancelled_at IS NULL AND n.attempts<8 AND n.next_attempt_at<=NOW()
 AND (n.locked_at IS NULL OR n.locked_at<NOW()-INTERVAL '1 minute')
 AND NOT EXISTS(SELECT 1 FROM channel_monitor_hybrid_notifications older
   WHERE older.rule_id=n.rule_id AND older.id<n.id AND older.sent_at IS NULL AND older.cancelled_at IS NULL)
 ORDER BY n.id LIMIT 5 FOR UPDATE SKIP LOCKED`)
	if err != nil {
		return
	}
	type pending struct {
		id         int64
		ruleID     string
		minute     time.Time
		kind       string
		generation int
		message    string
	}
	list := []pending{}
	for rows.Next() {
		var entry pending
		if rows.Scan(&entry.id, &entry.ruleID, &entry.minute, &entry.kind, &entry.generation, &entry.message) == nil {
			list = append(list, entry)
		}
	}
	rows.Close()
	for _, entry := range list {
		_, _ = tx.ExecContext(ctx, "UPDATE channel_monitor_hybrid_notifications SET locked_at=NOW(),locked_by=$2 WHERE id=$1", entry.id, workerID)
	}
	if tx.Commit() != nil {
		return
	}
	for _, entry := range list {
		latest, configErr := s.Config(ctx)
		if configErr != nil || !latest.Enabled || !latest.NotificationsEnabled {
			_, _ = s.db.ExecContext(ctx, "UPDATE channel_monitor_hybrid_notifications SET locked_at=NULL,locked_by='' WHERE id=$1 AND locked_by=$2", entry.id, workerID)
			continue
		}
		allowed := entry.ruleID == "__monitor__"
		var currentRule HybridMonitorRule
		for _, rule := range latest.Rules {
			if rule.ID == entry.ruleID && rule.Enabled && !rule.Muted {
				allowed = true
				currentRule = rule
			}
		}
		if !allowed {
			_, _ = s.db.ExecContext(ctx, "UPDATE channel_monitor_hybrid_notifications SET locked_at=NULL,locked_by='' WHERE id=$1 AND locked_by=$2", entry.id, workerID)
			continue
		}
		if entry.ruleID == "__monitor__" {
			sendErr := s.send(ctx, entry.message)
			if sendErr == nil {
				_, _ = s.db.ExecContext(ctx, "UPDATE channel_monitor_hybrid_notifications SET sent_at=NOW(),attempts=attempts+1,last_error='',locked_at=NULL,locked_by='' WHERE id=$1 AND locked_by=$2", entry.id, workerID)
			} else {
				_, _ = s.db.ExecContext(ctx, "UPDATE channel_monitor_hybrid_notifications SET attempts=attempts+1,last_error=$3,next_attempt_at=NOW()+INTERVAL '1 minute'*LEAST(30,POWER(2,attempts)),locked_at=NULL,locked_by='' WHERE id=$1 AND locked_by=$2", entry.id, workerID, sendErr.Error())
			}
			continue
		}
		var raw []byte
		var point HybridMonitorMinute
		if err = s.db.QueryRowContext(ctx, "SELECT result FROM channel_monitor_hybrid_minutes WHERE rule_id=$1 AND minute=$2", entry.ruleID, entry.minute).Scan(&raw); err != nil || json.Unmarshal(raw, &point) != nil {
			_, _ = s.db.ExecContext(ctx, "UPDATE channel_monitor_hybrid_notifications SET locked_at=NULL,locked_by='',attempts=attempts+1,last_error='通知证据不存在' WHERE id=$1 AND locked_by=$2", entry.id, workerID)
			continue
		}
		sendErr := s.send(ctx, hybridNotificationMessage(currentRule, point, entry.kind))
		if sendErr == nil {
			_, _ = s.db.ExecContext(ctx, "UPDATE channel_monitor_hybrid_notifications SET sent_at=NOW(),attempts=attempts+1,last_error='',locked_at=NULL,locked_by='' WHERE id=$1 AND locked_by=$2", entry.id, workerID)
		} else {
			_, _ = s.db.ExecContext(ctx, "UPDATE channel_monitor_hybrid_notifications SET attempts=attempts+1,last_error=$3,next_attempt_at=NOW()+INTERVAL '1 minute'*LEAST(30,POWER(2,attempts)),locked_at=NULL,locked_by='' WHERE id=$1 AND locked_by=$2", entry.id, workerID, sendErr.Error())
		}
	}
}

func (s *HybridMonitorService) Notifications(ctx context.Context) ([]map[string]any, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id,rule_id,minute,kind,attempts,sent_at,last_error FROM channel_monitor_hybrid_notifications WHERE cancelled_at IS NULL ORDER BY id DESC LIMIT 50")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	entries := []map[string]any{}
	for rows.Next() {
		var id int64
		var rule, kind, lastError string
		var minute time.Time
		var attempts int
		var sent *time.Time
		if err = rows.Scan(&id, &rule, &minute, &kind, &attempts, &sent, &lastError); err != nil {
			return nil, err
		}
		entries = append(entries, map[string]any{"id": id, "rule_id": rule, "minute": minute, "kind": kind, "attempts": attempts, "sent_at": sent, "last_error": lastError})
	}
	return entries, rows.Err()
}
