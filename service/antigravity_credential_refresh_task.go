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
	antigravityCredentialRefreshTickInterval = 10 * time.Minute
	antigravityCredentialRefreshThreshold    = 30 * time.Minute
	antigravityCredentialRefreshBatchSize    = 200
)

var (
	antigravityCredentialRefreshOnce    sync.Once
	antigravityCredentialRefreshRunning atomic.Bool
)

func StartAntigravityCredentialAutoRefreshTask() {
	antigravityCredentialRefreshOnce.Do(func() {
		if !common.IsMasterNode {
			return
		}
		gopool.Go(func() {
			logger.LogInfo(context.Background(), fmt.Sprintf("antigravity credential auto-refresh task started: tick=%s threshold=%s", antigravityCredentialRefreshTickInterval, antigravityCredentialRefreshThreshold))

			ticker := time.NewTicker(antigravityCredentialRefreshTickInterval)
			defer ticker.Stop()

			runAntigravityCredentialAutoRefreshOnce()
			for range ticker.C {
				runAntigravityCredentialAutoRefreshOnce()
			}
		})
	})
}

func runAntigravityCredentialAutoRefreshOnce() {
	if !antigravityCredentialRefreshRunning.CompareAndSwap(false, true) {
		return
	}
	defer antigravityCredentialRefreshRunning.Store(false)

	ctx := context.Background()
	now := time.Now()

	offset := 0
	for {
		var channels []*model.Channel
		err := model.DB.
			Select("id", "name", "key", "status", "channel_info").
			Where("type = ? AND (status = ? OR status = ?)",
				constant.ChannelTypeAntigravitySub,
				common.ChannelStatusEnabled,
				common.ChannelStatusAutoDisabled,
			).
			Order("id asc").
			Limit(antigravityCredentialRefreshBatchSize).
			Offset(offset).
			Find(&channels).Error
		if err != nil {
			logger.LogError(ctx, fmt.Sprintf("antigravity credential auto-refresh: query channels failed: %v", err))
			return
		}
		if len(channels) == 0 {
			break
		}
		offset += antigravityCredentialRefreshBatchSize

		for _, ch := range channels {
			if ch == nil || ch.ChannelInfo.IsMultiKey {
				continue
			}
			rawKey := strings.TrimSpace(ch.Key)
			if rawKey == "" {
				continue
			}
			oauthKey, ok := ParseAntigravityOAuthKey(rawKey)
			if !ok || oauthKey.RefreshToken == "" {
				continue
			}
			expiredAt, parseErr := time.Parse(time.RFC3339, oauthKey.Expired)
			if parseErr == nil && !expiredAt.IsZero() && expiredAt.Sub(now) > antigravityCredentialRefreshThreshold {
				continue
			}

			oauthKey, updated, refreshErr := refreshAntigravityChannelCredential(ctx, ch, true)
			if refreshErr != nil {
				logger.LogError(ctx, fmt.Sprintf("antigravity credential auto-refresh: channel #%d failed: %v", ch.Id, refreshErr))
				continue
			}
			backfillAntigravityKeyMetadata(ctx, updated, oauthKey)
		}
	}
}
