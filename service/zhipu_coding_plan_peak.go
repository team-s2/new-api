package service

import (
	"context"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/dto"

	"github.com/bytedance/gopkg/util/gopool"
)

const (
	// zhipuPeakTickInterval bounds how late a channel follows the peak window.
	zhipuPeakTickInterval = 30 * time.Second
	zhipuPeakStartHour    = 14
	zhipuPeakEndHour      = 18
)

// zhipuPeakZone is fixed so the window does not depend on the host's tzdata.
var zhipuPeakZone = time.FixedZone("UTC+8", 8*60*60)

var zhipuPeakTaskOnce sync.Once

// ZhipuPeakNotifier announces the channels one pass disabled and restored.
type ZhipuPeakNotifier func(disabled, restored []*model.Channel)

// IsZhipuPeak reports whether now falls in the Coding Plan peak window,
// 14:00-18:00 UTC+8 on Monday through Friday.
func IsZhipuPeak(now time.Time) bool {
	local := now.In(zhipuPeakZone)
	weekday := local.Weekday()
	if weekday == time.Saturday || weekday == time.Sunday {
		return false
	}
	return local.Hour() >= zhipuPeakStartHour && local.Hour() < zhipuPeakEndHour
}

// StartZhipuPeakTask keeps opted-in Coding Plan channels offline during peak
// hours. Only the master node writes channel status, matching the other
// scheduled channel jobs that must not race across instances.
func StartZhipuPeakTask(notify ZhipuPeakNotifier) {
	zhipuPeakTaskOnce.Do(func() {
		if !common.IsMasterNode {
			return
		}
		gopool.Go(func() {
			logger.LogInfo(context.Background(), common.LogText("Zhipu Coding Plan peak task started: tick=%s", zhipuPeakTickInterval))
			ticker := time.NewTicker(zhipuPeakTickInterval)
			defer ticker.Stop()

			runZhipuPeakOnce(notify)
			for range ticker.C {
				runZhipuPeakOnce(notify)
			}
		})
	})
}

// peakAutoDisableOn reads the option without Channel.GetSetting, which saves
// the record when the setting is unreadable and would wipe the columns a
// snapshot leaves out. An unreadable setting counts as off.
func peakAutoDisableOn(channel *model.Channel) bool {
	if !channel.IsZhipuCodingPlan() || channel.Setting == nil {
		return false
	}
	var setting dto.ChannelSettings
	return common.UnmarshalJsonStr(*channel.Setting, &setting) == nil && setting.PeakAutoDisable
}

// runZhipuPeakOnce disables opted-in channels during peak hours and restores
// the ones this task disabled once they no longer qualify, whether the window
// closed or the option was turned off. Any other disabled channel is left to
// the administrator or the upstream-failure flow.
func runZhipuPeakOnce(notify ZhipuPeakNotifier) {
	channels, err := model.GetZhipuPeakSnapshots()
	if err != nil {
		logger.LogWarn(context.Background(), common.LogText("Zhipu Coding Plan peak task failed to load channels: %v", err))
		return
	}

	peak := IsZhipuPeak(time.Now())
	var disabled, restored []*model.Channel
	for _, channel := range channels {
		shouldDisable := peak && peakAutoDisableOn(channel)
		switch {
		case shouldDisable && channel.Status == common.ChannelStatusEnabled:
			if model.UpdateChannelStatus(channel.Id, "", common.ChannelStatusAutoDisabled, model.ChannelStatusReasonZhipuPeak) {
				disabled = append(disabled, channel)
			}
		case !shouldDisable && channel.Status != common.ChannelStatusEnabled && channel.StatusReason() == model.ChannelStatusReasonZhipuPeak:
			if model.UpdateChannelStatus(channel.Id, "", common.ChannelStatusEnabled, "") {
				restored = append(restored, channel)
			}
		}
	}
	if len(disabled) == 0 && len(restored) == 0 {
		return
	}

	model.InitChannelCache()
	disabledIDs := make([]int, 0, len(disabled))
	for _, channel := range disabled {
		disabledIDs = append(disabledIDs, channel.Id)
	}
	CloseActiveWebSocketsForChannels(disabledIDs, ChannelDisabledCloseReason)
	notify(disabled, restored)
}
