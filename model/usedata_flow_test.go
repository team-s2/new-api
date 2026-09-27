package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
)

func seedFlowQuotaData(t *testing.T, quotaData QuotaData) {
	t.Helper()
	require.NoError(t, DB.Create(&quotaData).Error)
}

func seedFlowLookupData(t *testing.T) {
	t.Helper()
	require.NoError(t, DB.Create(&Channel{Id: 1, Name: "east"}).Error)
	require.NoError(t, DB.Create(&Channel{Id: 2, Name: "west"}).Error)
	require.NoError(t, DB.Create(&Token{Id: 11, UserId: 1, Key: "sk-primary", Name: "primary"}).Error)
	require.NoError(t, DB.Create(&Token{Id: 22, UserId: 2, Key: "sk-backup", Name: "backup"}).Error)
	require.NoError(t, DB.Delete(&Token{Id: 11}).Error)
}

func TestGetFlowQuotaDataUsesQuotaDataRoleSpecificDimensions(t *testing.T) {
	truncateTables(t)
	seedFlowLookupData(t)
	// This test exercises the quota_data path, which now only serves windows
	// longer than FlowDirectLogsWindowSeconds.
	original := common.FlowDirectLogsWindowSeconds
	common.FlowDirectLogsWindowSeconds = 0
	t.Cleanup(func() { common.FlowDirectLogsWindowSeconds = original })

	seedFlowQuotaData(t, QuotaData{
		UserID:    1,
		Username:  "alice",
		NodeName:  "node-a",
		TokenID:   11,
		UseGroup:  "vip",
		ModelName: "gpt-a",
		ChannelID: 1,
		CreatedAt: 1000,
		Count:     2,
		Quota:     100,
		TokenUsed: 40,
	})
	seedFlowQuotaData(t, QuotaData{
		UserID:    1,
		Username:  "alice",
		NodeName:  "node-a",
		TokenID:   11,
		UseGroup:  "vip",
		ModelName: "gpt-a",
		ChannelID: 1,
		CreatedAt: 1100,
		Count:     1,
		Quota:     50,
		TokenUsed: 20,
	})
	seedFlowQuotaData(t, QuotaData{
		UserID:    1,
		Username:  "alice",
		NodeName:  "node-a",
		TokenID:   11,
		UseGroup:  "vip",
		ModelName: "gpt-a",
		ChannelID: 2,
		CreatedAt: 1200,
		Count:     1,
		Quota:     25,
		TokenUsed: 10,
	})
	seedFlowQuotaData(t, QuotaData{
		UserID:    2,
		Username:  "bob",
		NodeName:  "node-b",
		TokenID:   22,
		UseGroup:  "default",
		ModelName: "gpt-b",
		ChannelID: 1,
		CreatedAt: 1300,
		Count:     3,
		Quota:     70,
		TokenUsed: 30,
	})
	seedFlowQuotaData(t, QuotaData{
		UserID:    1,
		Username:  "alice",
		ModelName: "legacy",
		CreatedAt: 1400,
		Count:     99,
		Quota:     999,
		TokenUsed: 999,
	})

	rootRows, err := GetFlowQuotaData(900, 2000, "", 0, common.RoleRootUser)
	require.NoError(t, err)
	require.Len(t, rootRows, 3)
	// Token 11 was soft-deleted, so its name is intentionally left empty for the
	// frontend to render a localized "deleted (id)" label instead.
	require.Equal(t, FlowQuotaData{
		UserID:      1,
		Username:    "alice",
		NodeName:    "node-a",
		TokenID:     11,
		TokenName:   "",
		UseGroup:    "vip",
		ChannelID:   1,
		ChannelName: "east",
		ModelName:   "gpt-a",
		TokenUsed:   60,
		Count:       3,
		Quota:       150,
	}, *rootRows[0])
	// A token that still exists resolves to its current name.
	require.Equal(t, 22, rootRows[1].TokenID)
	require.Equal(t, "backup", rootRows[1].TokenName)

	adminRows, err := GetFlowQuotaData(900, 2000, "alice", 0, common.RoleAdminUser)
	require.NoError(t, err)
	require.Len(t, adminRows, 2)
	require.Equal(t, 0, adminRows[0].TokenID)
	require.Empty(t, adminRows[0].TokenName)
	require.Empty(t, adminRows[0].NodeName)
	require.Equal(t, "alice", adminRows[0].Username)
	require.Equal(t, "vip", adminRows[0].UseGroup)
	require.Equal(t, "east", adminRows[0].ChannelName)
	require.Equal(t, 150, adminRows[0].Quota)

	selfRows, err := GetFlowQuotaData(900, 2000, "", 1, common.RoleCommonUser)
	require.NoError(t, err)
	require.Len(t, selfRows, 1)
	require.Empty(t, selfRows[0].Username)
	require.Equal(t, 0, selfRows[0].ChannelID)
	require.Empty(t, selfRows[0].ChannelName)
	require.Empty(t, selfRows[0].TokenName)
	require.Equal(t, "vip", selfRows[0].UseGroup)
	require.Equal(t, 175, selfRows[0].Quota)
}

func TestLogQuotaDataSplitsRowsByUseGroupTokenChannelAndNode(t *testing.T) {
	truncateTables(t)
	CacheQuotaDataLock.Lock()
	CacheQuotaData = make(map[string]*QuotaData)
	CacheQuotaDataLock.Unlock()

	LogQuotaData(QuotaDataLogParams{
		UserID:    1,
		Username:  "alice",
		ModelName: "gpt-a",
		CreatedAt: 3661,
		UseGroup:  "vip",
		TokenID:   11,
		ChannelID: 1,
		NodeName:  "node-a",
		Quota:     100,
		TokenUsed: 40,
	})
	LogQuotaData(QuotaDataLogParams{
		UserID:    1,
		Username:  "alice",
		ModelName: "gpt-a",
		CreatedAt: 3700,
		UseGroup:  "vip",
		TokenID:   11,
		ChannelID: 1,
		NodeName:  "node-a",
		Quota:     50,
		TokenUsed: 20,
	})
	LogQuotaData(QuotaDataLogParams{
		UserID:    1,
		Username:  "alice",
		ModelName: "gpt-a",
		CreatedAt: 3700,
		UseGroup:  "default",
		TokenID:   11,
		ChannelID: 1,
		NodeName:  "node-a",
		Quota:     25,
		TokenUsed: 10,
	})

	SaveQuotaDataCache()

	var rows []QuotaData
	require.NoError(t, DB.Order("quota DESC").Find(&rows).Error)
	require.Len(t, rows, 2)
	require.Equal(t, int64(3600), rows[0].CreatedAt)
	require.Equal(t, "vip", rows[0].UseGroup)
	require.Equal(t, 11, rows[0].TokenID)
	require.Equal(t, 1, rows[0].ChannelID)
	require.Equal(t, "node-a", rows[0].NodeName)
	require.Equal(t, 2, rows[0].Count)
	require.Equal(t, 150, rows[0].Quota)
	require.Equal(t, 60, rows[0].TokenUsed)
	require.Equal(t, "default", rows[1].UseGroup)
	require.Equal(t, 25, rows[1].Quota)
}

func seedFlowLog(t *testing.T, log Log) {
	t.Helper()
	require.NoError(t, LOG_DB.Create(&log).Error)
}

func TestGetFlowQuotaDataShortWindowAggregatesLogsDirectly(t *testing.T) {
	truncateTables(t)
	seedFlowLookupData(t)

	// 20-minute window [1000, 2200]. quota_data buckets are hour-truncated, so
	// only the direct logs path can resolve this range.
	const start, end = int64(1000), int64(2200)
	seedFlowLog(t, Log{UserId: 1, Username: "alice", Type: LogTypeConsume, ModelName: "gpt-a", Quota: 60, PromptTokens: 10, CompletionTokens: 2, ChannelId: 1, TokenId: 11, Group: "vip", CreatedAt: 1100})
	seedFlowLog(t, Log{UserId: 1, Username: "alice", Type: LogTypeConsume, ModelName: "gpt-a", Quota: 40, PromptTokens: 5, CompletionTokens: 5, ChannelId: 1, TokenId: 11, Group: "vip", CreatedAt: 2100})
	// Outside the window on both ends: must not be counted.
	seedFlowLog(t, Log{UserId: 1, Username: "alice", Type: LogTypeConsume, ModelName: "gpt-a", Quota: 999, ChannelId: 1, TokenId: 11, Group: "vip", CreatedAt: 900})
	seedFlowLog(t, Log{UserId: 1, Username: "alice", Type: LogTypeConsume, ModelName: "gpt-a", Quota: 999, ChannelId: 1, TokenId: 11, Group: "vip", CreatedAt: 2201})
	// Wrong type and empty group: must not be counted.
	seedFlowLog(t, Log{UserId: 1, Username: "alice", Type: LogTypeTopup, ModelName: "gpt-a", Quota: 999, ChannelId: 1, TokenId: 11, Group: "vip", CreatedAt: 1200})
	seedFlowLog(t, Log{UserId: 1, Username: "alice", Type: LogTypeConsume, ModelName: "gpt-b", Quota: 999, ChannelId: 2, TokenId: 22, Group: "", CreatedAt: 1300})
	// Another user only visible to admin/root views.
	seedFlowLog(t, Log{UserId: 2, Username: "bob", Type: LogTypeConsume, ModelName: "gpt-b", Quota: 25, PromptTokens: 3, CompletionTokens: 3, ChannelId: 2, TokenId: 22, Group: "vip", CreatedAt: 1400})

	rootRows, err := GetFlowQuotaData(start, end, "", 0, common.RoleRootUser)
	require.NoError(t, err)
	require.Len(t, rootRows, 2)
	require.Equal(t, "alice", rootRows[0].Username)
	require.Equal(t, "gpt-a", rootRows[0].ModelName)
	require.Equal(t, "east", rootRows[0].ChannelName)
	// Token 11 is soft-deleted in the fixture; names intentionally unresolved.
	require.Equal(t, "", rootRows[0].TokenName)
	require.Equal(t, "vip", rootRows[0].UseGroup)
	require.Equal(t, 100, rootRows[0].Quota)
	require.Equal(t, 2, rootRows[0].Count)
	require.Equal(t, 22, rootRows[0].TokenUsed)
	// Log rows do not carry node_name.
	require.Empty(t, rootRows[0].NodeName)
	require.Equal(t, 25, rootRows[1].Quota)

	selfRows, err := GetFlowQuotaData(start, end, "", 1, common.RoleCommonUser)
	require.NoError(t, err)
	require.Len(t, selfRows, 1)
	require.Equal(t, 100, selfRows[0].Quota)
	require.Equal(t, 2, selfRows[0].Count)
	require.Empty(t, selfRows[0].Username)

	adminRows, err := GetFlowQuotaData(start, end, "bob", 0, common.RoleAdminUser)
	require.NoError(t, err)
	require.Len(t, adminRows, 1)
	require.Equal(t, 25, adminRows[0].Quota)
	require.Equal(t, "west", adminRows[0].ChannelName)
}

func TestGetFlowQuotaDataLongWindowStillUsesQuotaData(t *testing.T) {
	truncateTables(t)
	seedFlowLookupData(t)

	seedFlowQuotaData(t, QuotaData{
		UserID:    1,
		Username:  "alice",
		TokenID:   11,
		UseGroup:  "vip",
		ModelName: "gpt-a",
		ChannelID: 1,
		CreatedAt: 1000,
		Count:     2,
		Quota:     100,
		TokenUsed: 40,
	})
	// A log row that must stay invisible: the window exceeds the threshold.
	seedFlowLog(t, Log{UserId: 1, Username: "alice", Type: LogTypeConsume, ModelName: "gpt-a", Quota: 999, ChannelId: 1, TokenId: 11, Group: "vip", CreatedAt: 1500})

	rootRows, err := GetFlowQuotaData(500, 500+int64(common.FlowDirectLogsWindowSeconds)+10, "", 0, common.RoleRootUser)
	require.NoError(t, err)
	require.Len(t, rootRows, 1)
	require.Equal(t, 100, rootRows[0].Quota)
	require.Equal(t, 2, rootRows[0].Count)
}
