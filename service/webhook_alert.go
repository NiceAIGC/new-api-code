package service

import (
	"bytes"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
)

// feishuAlertEnabled returns true if the webhook URL is configured and alerts are not explicitly disabled.
func feishuAlertEnabled() bool {
	if os.Getenv("FEISHU_ALERT_ENABLED") == "false" {
		return false
	}
	return constant.FeishuAlertWebhook != ""
}

// FeishuRelayErrorAlert sends a Feishu card alert when a relay request fails after all retries.
// Non-blocking: the HTTP call runs in a goroutine.
func FeishuRelayErrorAlert(relayInfo *relaycommon.RelayInfo, apiErr *types.NewAPIError, usedChannels []string) {
	if !feishuAlertEnabled() || relayInfo == nil || apiErr == nil {
		return
	}
	go func() {
		if err := sendFeishuRelayAlert(relayInfo, apiErr, usedChannels); err != nil {
			common.SysError(fmt.Sprintf("feishu alert failed: %s", err.Error()))
		}
	}()
}

func sendFeishuRelayAlert(relayInfo *relaycommon.RelayInfo, apiErr *types.NewAPIError, usedChannels []string) error {
	now := time.Now().Format("2006-01-02 15:04:05")
	retryDetail := buildRetryDetail(usedChannels)

	card := map[string]any{
		"config": map[string]any{"wide_screen_mode": true},
		"header": map[string]any{
			"title":    map[string]any{"tag": "plain_text", "content": "AI 接口调用失败告警"},
			"template": "red",
		},
		"elements": []any{
			map[string]any{
				"tag": "div",
				"fields": []any{
					shortField("用户 ID", fmt.Sprintf("%d", relayInfo.UserId)),
					shortField("Token ID", fmt.Sprintf("%d", relayInfo.TokenId)),
				},
			},
			map[string]any{
				"tag": "div",
				"fields": []any{
					shortField("渠道 ID", fmt.Sprintf("%d", relayInfo.ChannelMeta.ChannelId)),
					shortField("渠道类型", fmt.Sprintf("%d", relayInfo.ChannelMeta.ChannelType)),
				},
			},
			map[string]any{
				"tag": "div",
				"fields": []any{
					shortField("模型", relayInfo.OriginModelName),
					shortField("分组", relayInfo.UsingGroup),
				},
			},
			map[string]any{"tag": "hr"},
			map[string]any{
				"tag": "div",
				"text": map[string]any{
					"tag":     "lark_md",
					"content": fmt.Sprintf("**错误信息**\n%s", apiErr.Error()),
				},
			},
			map[string]any{
				"tag": "div",
				"fields": []any{
					shortField("HTTP 状态码", fmt.Sprintf("%d", apiErr.StatusCode)),
					shortField("重试次数", fmt.Sprintf("%d", relayInfo.RetryIndex)),
				},
			},
			map[string]any{"tag": "hr"},
			map[string]any{
				"tag": "div",
				"text": map[string]any{
					"tag":     "lark_md",
					"content": fmt.Sprintf("**重试渠道链路**\n%s", retryDetail),
				},
			},
			map[string]any{
				"tag": "note",
				"elements": []any{
					map[string]any{"tag": "plain_text", "content": fmt.Sprintf("告警时间: %s", now)},
				},
			},
		},
	}

	body, err := common.Marshal(map[string]any{"msg_type": "interactive", "card": card})
	if err != nil {
		return err
	}

	resp, err := http.Post(constant.FeishuAlertWebhook, "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}

func shortField(label, value string) map[string]any {
	return map[string]any{
		"is_short": true,
		"text": map[string]any{
			"tag":     "lark_md",
			"content": fmt.Sprintf("**%s**\n%s", label, value),
		},
	}
}

func buildRetryDetail(usedChannels []string) string {
	if len(usedChannels) == 0 {
		return "无"
	}
	result := ""
	for i, ch := range usedChannels {
		if i > 0 {
			result += " → "
		}
		result += fmt.Sprintf("渠道 %s", ch)
	}
	return result
}
