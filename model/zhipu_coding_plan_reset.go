package model

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
)

// GetZhipuResetOpportunitySnapshots returns the enabled and auto-disabled
// BigModel Coding Plan channels with the fields the reset-card claim task
// needs, credentials included. The result must not be saved back.
func GetZhipuResetOpportunitySnapshots() ([]*Channel, error) {
	var channels []*Channel
	err := DB.Select("id", "name", "type", "status", "group", "key", "setting", "other_info", "channel_info").
		Where("type = ? AND status IN ?", constant.ChannelTypeBigModelSub, []int{common.ChannelStatusEnabled, common.ChannelStatusAutoDisabled}).
		Order("id asc").
		Find(&channels).Error
	return channels, err
}
