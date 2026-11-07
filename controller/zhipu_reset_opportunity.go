package controller

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/channel/zhipu_4v"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/service"
	"github.com/bytedance/gopkg/util/gopool"
)

var zhipuResetOpportunityOnce sync.Once

// StartZhipuResetOpportunityTask claims cards even while channels are disabled
// for peak hours. It never consumes cards or changes channel health.
func StartZhipuResetOpportunityTask(notify func([]*model.Channel)) {
	zhipuResetOpportunityOnce.Do(func() {
		if !common.IsMasterNode {
			return
		}
		gopool.Go(func() {
			states := make(map[[32]byte]*service.ZhipuResetOpportunity)
			ticker := time.NewTicker(5 * time.Minute)
			defer ticker.Stop()
			for {
				// Reuse the existing channel reader, including credentials, without
				// introducing database writes or a new persisted configuration.
				channels, err := model.GetAllChannels(0, 0, true, true)
				if err != nil {
					common.SysError("Zhipu reset opportunity: failed to load channels")
				} else {
					seen := make(map[[32]byte]bool)
					var grantedChannels []*model.Channel
					for _, channel := range channels {
						if channel.Type != constant.ChannelTypeBigModelSub || channel.ChannelInfo.IsMultiKey {
							continue
						}
						if channel.Status != common.ChannelStatusEnabled && channel.StatusReason() != model.ChannelStatusReasonZhipuPeak {
							continue
						}
						credential, err := zhipu_4v.ParseOAuthCredential(channel.Key)
						if err != nil || credential.ZcodeJWT == "" {
							continue
						}
						// Duplicate channels sharing credentials share the cooldown. Do not
						// retain raw tokens in the scheduler map or print them in errors.
						key := sha256.Sum256([]byte(credential.ZcodeJWT))
						seen[key] = true
						state := states[key]
						if state == nil {
							state = &service.ZhipuResetOpportunity{}
							states[key] = state
						}
						if time.Now().Before(state.NextAt) {
							continue
						}
						var setting dto.ChannelSettings
						if channel.Setting != nil && common.UnmarshalJsonStr(*channel.Setting, &setting) != nil {
							state.NextAt = time.Now().Add(5 * time.Minute)
							continue
						}
						client, err := service.NewProxyHttpClient(setting.Proxy)
						if err != nil {
							state.NextAt = time.Now().Add(5 * time.Minute)
							continue
						}
						ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
						granted, err := state.Claim(ctx, client, credential.ZcodeJWT, credential.AccessToken)
						if err != nil {
							common.SysError(fmt.Sprintf("Zhipu reset opportunity failed for channel %d", channel.Id))
						} else if granted {
							// No local card cache: refresh immediately, and subsequent UI/QQ
							// queries read the inventory from upstream as before.
							_, err = service.FetchZhipuCodingPlanResetStatus(ctx, client, credential.ZcodeJWT, credential.AccessToken)
							if err != nil {
								common.SysError(fmt.Sprintf("Zhipu reset inventory refresh failed for channel %d", channel.Id))
							}
							common.SysLog(fmt.Sprintf("Zhipu reset card granted for channel %d", channel.Id))
							grantedChannels = append(grantedChannels, channel)
						}
						cancel()
					}
					if len(grantedChannels) > 0 && notify != nil {
						notify(grantedChannels)
					}
					for key := range states {
						if !seen[key] {
							delete(states, key)
						}
					}
				}
				<-ticker.C
			}
		})
	})
}
