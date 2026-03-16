package nicecode

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
)

var (
	nicecodeClient     *http.Client
	nicecodeClientOnce sync.Once

	// 积分比例缓存
	cacheMutex          sync.RWMutex
	creditToQuotaRatio  int64
	cacheExpireTime     time.Time
	cacheDuration       = 5 * time.Minute // 缓存5分钟
)

// initNicecodeClient 初始化 nicecode HTTP 客户端（全局单例）
func initNicecodeClient() {
	nicecodeClientOnce.Do(func() {
		nicecodeClient = &http.Client{
			Timeout: 10 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:        100,
				MaxIdleConnsPerHost: 10,
				IdleConnTimeout:     90 * time.Second,
			},
		}
	})
}

// getNicecodeClient 获取 nicecode HTTP 客户端
func getNicecodeClient() *http.Client {
	initNicecodeClient()
	return nicecodeClient
}

// ConsumeLogRequest 积分消耗记录请求
type ConsumeLogRequest struct {
	ApiUserID        int                    `json:"api_user_id"`
	ApiTokenID       int                    `json:"api_token_id"`
	ModelName        string                 `json:"model_name"`
	Quota            int64                  `json:"quota"`
	PromptTokens     int64                  `json:"prompt_tokens"`
	CompletionTokens int64                  `json:"completion_tokens"`
	CacheReadTokens  int64                  `json:"cache_read_tokens"`
	CacheWriteTokens int64                  `json:"cache_write_tokens"`
	RequestID        string                 `json:"request_id"`
	IP               string                 `json:"ip"`
	Other            map[string]interface{} `json:"other,omitempty"`
}

// sendConsumeLog 发送消耗记录到 nicecode（带重试机制）
func sendConsumeLog(baseURL, apiKey string, req *ConsumeLogRequest) error {
	// 构建请求URL
	url := baseURL + "/api/system/consume-log"

	// 序列化请求体
	bodyBytes, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	// 重试配置
	const (
		maxRetries     = 3                // 最大重试次数
		initialBackoff = 200 * time.Millisecond // 初始退避时间
	)

	var lastErr error
	client := getNicecodeClient()

	// 重试循环
	for attempt := 0; attempt <= maxRetries; attempt++ {
		// 创建HTTP请求
		httpReq, err := http.NewRequest("POST", url, bytes.NewReader(bodyBytes))
		if err != nil {
			return fmt.Errorf("failed to create request: %w", err)
		}

		// 设置请求头
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)

		// 发送请求
		resp, err := client.Do(httpReq)
		if err != nil {
			lastErr = fmt.Errorf("failed to send request: %w", err)
			if attempt < maxRetries {
				// 指数退避：200ms, 400ms, 800ms
				backoff := time.Duration(float64(initialBackoff) * math.Pow(2, float64(attempt)))
				common.SysLog(fmt.Sprintf("Nicecode request failed (attempt %d/%d), retrying after %v: %v",
					attempt+1, maxRetries+1, backoff, err))
				time.Sleep(backoff)
				continue
			}
			return lastErr
		}
		defer resp.Body.Close()

		// 检查响应状态
		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("nicecode API returned status %d", resp.StatusCode)
			// 对于 5xx 错误进行重试，4xx 错误直接返回
			if resp.StatusCode >= 500 && attempt < maxRetries {
				backoff := time.Duration(float64(initialBackoff) * math.Pow(2, float64(attempt)))
				common.SysLog(fmt.Sprintf("Nicecode API returned %d (attempt %d/%d), retrying after %v",
					resp.StatusCode, attempt+1, maxRetries+1, backoff))
				time.Sleep(backoff)
				continue
			}
			return lastErr
		}

		// 解析响应
		var result struct {
			Success bool   `json:"success"`
			Message string `json:"message"`
			Data    struct {
				TransactionID uint  `json:"transaction_id"`
				Balance       int64 `json:"balance"`
			} `json:"data"`
		}

		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			lastErr = fmt.Errorf("failed to decode response: %w", err)
			if attempt < maxRetries {
				backoff := time.Duration(float64(initialBackoff) * math.Pow(2, float64(attempt)))
				common.SysLog(fmt.Sprintf("Failed to decode nicecode response (attempt %d/%d), retrying after %v: %v",
					attempt+1, maxRetries+1, backoff, err))
				time.Sleep(backoff)
				continue
			}
			return lastErr
		}

		if !result.Success {
			return fmt.Errorf("nicecode API error: %s", result.Message)
		}

		common.SysLog(fmt.Sprintf("Recorded consume log to nicecode: api_user_id=%d, transaction_id=%d, balance=%d",
			req.ApiUserID, result.Data.TransactionID, result.Data.Balance))

		return nil
	}

	return lastErr
}

// RecordConsumeLog 记录积分消耗到 nicecode
// 此函数被 model 包调用，避免循环依赖
func RecordConsumeLog(userId, tokenId int, modelName string, quota, promptTokens, completionTokens int, other map[string]interface{}, requestId, clientIP string) {
	if common.NicecodeURL == "" || common.NicecodeAPIKey == "" {
		return
	}

	// 获取 other 中的 cache tokens（如果存在）
	var cacheReadTokens, cacheWriteTokens int64
	if other != nil {
		if val, ok := other["cache_tokens"].(float64); ok {
			cacheReadTokens = int64(val)
		} else if v, ok2 := other["cache_tokens"].(int); ok2 {
			cacheReadTokens = int64(v)
		}
		if val, ok := other["cache_creation_tokens"].(float64); ok {
			cacheWriteTokens = int64(val)
		} else if v, ok2 := other["cache_creation_tokens"].(int); ok2 {
			cacheWriteTokens = int64(v)
		}
	}

	// 构建请求（userId 就是 new-api-code 的 user_id，也就是 api_user_id）
	req := &ConsumeLogRequest{
		ApiUserID:        userId,
		ApiTokenID:       tokenId,
		ModelName:        modelName,
		Quota:            int64(quota),
		PromptTokens:     int64(promptTokens),
		CompletionTokens: int64(completionTokens),
		CacheReadTokens:  cacheReadTokens,
		CacheWriteTokens: cacheWriteTokens,
		RequestID:        requestId,
		IP:               clientIP,
		Other:            other,
	}

	// 发送请求
	if err := sendConsumeLog(common.NicecodeURL, common.NicecodeAPIKey, req); err != nil {
		common.SysLog(fmt.Sprintf("Failed to record consume log to nicecode: %v", err))
	}
}

// StatusResponse nicecode 状态响应
type StatusResponse struct {
	Success bool `json:"success"`
	Data    struct {
		CreditToQuotaRatio int64 `json:"credit_to_quota_ratio"`
		CreditToPriceRatio int64 `json:"credit_to_price_ratio"`
	} `json:"data"`
}

// getStatus 获取 nicecode 状态信息
func getStatus(baseURL string) (*StatusResponse, error) {
	if baseURL == "" {
		return nil, fmt.Errorf("nicecode not configured")
	}

	url := baseURL + "/api/status"
	client := getNicecodeClient()

	httpReq, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("nicecode API returned status %d", resp.StatusCode)
	}

	var result StatusResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	if !result.Success {
		return nil, fmt.Errorf("nicecode API returned success=false")
	}

	return &result, nil
}

// GetCreditToQuotaRatio 获取积分转额度比例（带缓存）
func GetCreditToQuotaRatio() (int64, error) {
	if common.NicecodeURL == "" {
		return 0, fmt.Errorf("nicecode not configured")
	}

	// 先检查缓存
	cacheMutex.RLock()
	if creditToQuotaRatio > 0 && time.Now().Before(cacheExpireTime) {
		ratio := creditToQuotaRatio
		cacheMutex.RUnlock()
		return ratio, nil
	}
	cacheMutex.RUnlock()

	// 缓存过期或未初始化，获取新数据
	cacheMutex.Lock()
	defer cacheMutex.Unlock()

	// 双重检查，防止并发请求
	if creditToQuotaRatio > 0 && time.Now().Before(cacheExpireTime) {
		return creditToQuotaRatio, nil
	}

	// 调用 API 获取状态
	status, err := getStatus(common.NicecodeURL)
	if err != nil {
		return 0, err
	}

	// 更新缓存
	creditToQuotaRatio = status.Data.CreditToQuotaRatio
	cacheExpireTime = time.Now().Add(cacheDuration)

	return creditToQuotaRatio, nil
}
