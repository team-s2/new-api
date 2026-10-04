package service

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"

	"github.com/bytedance/gopkg/util/gopool"
)

const (
	grokCredentialRefreshTickInterval = 10 * time.Minute
	// xAI access tokens live about an hour, so a Codex-style 24h threshold
	// would rotate every refresh token on every tick.
	grokCredentialRefreshThreshold = 30 * time.Minute
	grokCredentialRefreshBatchSize = 200
	grokCredentialRefreshTimeout   = 15 * time.Second
)

var (
	grokCredentialRefreshOnce    sync.Once
	grokCredentialRefreshRunning atomic.Bool
)

// StartGrokCredentialAutoRefreshTask periodically refreshes Grok Subscription
// channel credentials whose tokens expire within the threshold. Master node
// only, mirroring the Codex credential task.
func StartGrokCredentialAutoRefreshTask() {
	grokCredentialRefreshOnce.Do(func() {
		if !common.IsMasterNode {
			return
		}
		gopool.Go(func() {
			logger.LogInfo(context.Background(), fmt.Sprintf("grok credential auto-refresh task started: tick=%s threshold=%s", grokCredentialRefreshTickInterval, grokCredentialRefreshThreshold))

			ticker := time.NewTicker(grokCredentialRefreshTickInterval)
			defer ticker.Stop()

			runGrokCredentialAutoRefreshOnce()
			for range ticker.C {
				runGrokCredentialAutoRefreshOnce()
			}
		})
	})
}

func runGrokCredentialAutoRefreshOnce() {
	if !grokCredentialRefreshRunning.CompareAndSwap(false, true) {
		return
	}
	defer grokCredentialRefreshRunning.Store(false)

	ctx := context.Background()
	now := time.Now()

	offset := 0
	for {
		var channels []*model.Channel
		err := model.DB.
			Select("id", "name", "key", "status", "channel_info").
			Where("type = ? AND (status = ? OR status = ?)",
				constant.ChannelTypeGrokSub,
				common.ChannelStatusEnabled,
				common.ChannelStatusAutoDisabled,
			).
			Order("id asc").
			Limit(grokCredentialRefreshBatchSize).
			Offset(offset).
			Find(&channels).Error
		if err != nil {
			logger.LogError(ctx, fmt.Sprintf("grok credential auto-refresh: query channels failed: %v", err))
			return
		}
		if len(channels) == 0 {
			break
		}
		offset += grokCredentialRefreshBatchSize

		for _, ch := range channels {
			if ch == nil || ch.ChannelInfo.IsMultiKey {
				continue
			}
			rawKey := strings.TrimSpace(ch.Key)
			if rawKey == "" {
				continue
			}
			oauthKey, err := ParseGrokOAuthKey(rawKey)
			if err != nil {
				continue
			}
			if strings.TrimSpace(oauthKey.RefreshToken) == "" {
				continue
			}
			expiredAt, err := time.Parse(time.RFC3339, strings.TrimSpace(oauthKey.Expired))
			if err == nil && !expiredAt.IsZero() && expiredAt.Sub(now) > grokCredentialRefreshThreshold {
				continue
			}

			refreshCtx, cancel := context.WithTimeout(ctx, grokCredentialRefreshTimeout)
			newKey, _, err := refreshGrokChannelCredential(refreshCtx, ch, true)
			cancel()
			if err != nil {
				logger.LogWarn(ctx, fmt.Sprintf("grok credential auto-refresh: channel_id=%d name=%s refresh failed: %v", ch.Id, ch.Name, err))
				continue
			}
			logger.LogInfo(ctx, fmt.Sprintf("grok credential auto-refresh: channel_id=%d name=%s refreshed, expires_at=%s", ch.Id, ch.Name, newKey.Expired))
		}
	}

}
