package service

import (
	"errors"
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/types"
	"github.com/stretchr/testify/require"
)

func TestShouldDisableChannelSkipsFirstResponseTimeout(t *testing.T) {
	previous := common.AutomaticDisableChannelEnabled
	common.AutomaticDisableChannelEnabled = true
	defer func() {
		common.AutomaticDisableChannelEnabled = previous
	}()

	timeoutErr := types.NewErrorWithStatusCode(
		errors.New("upstream first response timed out"),
		types.ErrorCodeUpstreamFirstResponseTimeout,
		http.StatusGatewayTimeout,
	)
	require.False(t, ShouldDisableChannel(timeoutErr))
}

func TestShouldDisableChannelSkipsServerOverload(t *testing.T) {
	previousEnabled := common.AutomaticDisableChannelEnabled
	previousRanges := operation_setting.AutomaticDisableStatusCodeRanges
	common.AutomaticDisableChannelEnabled = true
	operation_setting.AutomaticDisableStatusCodeRanges = []operation_setting.StatusCodeRange{
		{Start: http.StatusServiceUnavailable, End: http.StatusServiceUnavailable},
	}
	t.Cleanup(func() {
		common.AutomaticDisableChannelEnabled = previousEnabled
		operation_setting.AutomaticDisableStatusCodeRanges = previousRanges
	})

	overloadedErr := types.WithOpenAIError(types.OpenAIError{
		Message: "server is overloaded",
		Type:    "server_error",
		Code:    "server_is_overloaded",
	}, http.StatusServiceUnavailable)
	require.False(t, ShouldDisableChannel(overloadedErr))
}
