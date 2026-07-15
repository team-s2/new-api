package service

import (
	"errors"
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/common"
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
