package common

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestGitHubAccessPolicyValidation(t *testing.T) {
	for _, raw := range []string{
		DefaultGitHubAccessPolicy,
		`{"enabled":true,"organizations":["Team-One","team-two"],"user_ids":["123"],"role":100}`,
		`{"enabled":true,"role":0}`,
	} {
		_, err := ParseGitHubAccessPolicy(raw)
		require.NoError(t, err)
	}
	for _, raw := range []string{
		`null`, ` null `, `invalid`, `{}`, `{"enabeld":true}`, `{"enabled":true,"role":99}`,
		`{"enabled":false,"role":100}`, `{"enabled":true,"organizations":["../users"]}`,
		`{"enabled":true,"user_ids":["alice"]}`, `{"enabled":true,"user_ids":["0"]}`,
		`{"enabled":true,"user_ids":["01"]}`, `{"enabled":true,"user_ids":["9223372036854775808"]}`,
	} {
		_, err := ParseGitHubAccessPolicy(raw)
		require.Error(t, err, raw)
	}
}
