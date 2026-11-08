package service

import (
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/constant"

	"github.com/google/uuid"
)

// setZcodeSourceHeaders adds the headers the official client attaches to every
// request for the zcode.z.ai app API (its NodeApiClient), reported as that
// client's Linux server build. Model relay requests are not app API requests
// and do not carry them.
//
// The official client sends one persistent random device ID per install, and
// billing/balance rejects a request without it. new-api keeps no such state, so
// the ID is derived from the ZCode JWT: stable across nodes and restarts, and
// shared by channels on one account. An empty JWT, as in the OAuth exchange
// before one exists, omits the header.
func setZcodeSourceHeaders(header http.Header, zcodeJWT string) {
	header.Set("User-Agent", constant.ZcodeTrialPlanUserAgent)
	header.Set("HTTP-Referer", "https://zcode.z.ai")
	header.Set("X-ZCode-App-Version", constant.ZcodeClientVersion)
	header.Set("X-Title", "Z Code@electron")
	header.Set("X-Platform", "linux-x64")
	header.Set("X-Release-Channel", "production")
	header.Set("X-Client-Language", "zh-CN")
	header.Set("X-Client-Timezone", "Asia/Shanghai")
	header.Set("X-Os-Category", "linux")
	if token := strings.TrimSpace(zcodeJWT); token != "" {
		header.Set("X-Device-Mid", uuid.NewSHA1(uuid.NameSpaceOID, []byte(token)).String())
	}
}
