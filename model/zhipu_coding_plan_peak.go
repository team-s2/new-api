package model

import "github.com/QuantumNous/new-api/constant"

// ChannelStatusReasonZhipuPeak marks a channel the Coding Plan peak task took
// offline. That task owns every status carrying this reason and is the only
// writer that re-enables such a channel automatically.
const ChannelStatusReasonZhipuPeak = "Disabled during Zhipu Coding Plan peak hours"

// StatusReason returns the reason recorded with the current channel status.
func (channel *Channel) StatusReason() string {
	reason, _ := channel.GetOtherInfo()["status_reason"].(string)
	return reason
}

// IsZhipuCodingPlan reports whether the channel runs on a Zhipu Coding Plan
// subscription.
func (channel *Channel) IsZhipuCodingPlan() bool {
	return constant.IsZhipuCodingPlanChannel(channel.Type, channel.GetBaseURL())
}

// GetZhipuPeakSnapshots returns the channel fields the peak task needs.
// Credentials and model lists stay out so the periodic pass reads small columns;
// the result must not be saved back.
func GetZhipuPeakSnapshots() ([]*Channel, error) {
	var channels []*Channel
	err := DB.Select("id", "name", "type", "status", "base_url", "group", "setting", "other_info").Find(&channels).Error
	return channels, err
}
