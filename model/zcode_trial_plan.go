package model

import "github.com/QuantumNous/new-api/constant"

// IsZcodeTrialPlan reports whether the channel relays through the ZCode trial
// plan endpoint (start plan).
func (channel *Channel) IsZcodeTrialPlan() bool {
	if channel.Type != constant.ChannelTypeZhipu_v4 && channel.Type != constant.ChannelTypeBigModelSub {
		return false
	}
	return constant.IsZcodeTrialPlanBase(channel.GetBaseURL())
}

// GetZcodeTrialPlanChannels returns every trial plan channel with the fields plan
// detection needs. The result must not be saved back.
func GetZcodeTrialPlanChannels() ([]*Channel, error) {
	var channels []*Channel
	err := DB.Select("id", "name", "type", "status", "base_url", "key", "group", "model").
		Where("type IN ?", []int{constant.ChannelTypeZhipu_v4, constant.ChannelTypeBigModelSub}).
		Find(&channels).Error
	return channels, err
}
