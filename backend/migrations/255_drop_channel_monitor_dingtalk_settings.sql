-- Channel-monitor failure alerts now go to the security-alert WeCom robot
-- (security_alert_config.channel_monitor_notify). Drop the unused DingTalk
-- robot settings, including the stored webhook token and signing secret.
DELETE FROM settings
WHERE key IN (
    'channel_monitor_dingtalk_enabled',
    'channel_monitor_dingtalk_webhook',
    'channel_monitor_dingtalk_secret'
);
