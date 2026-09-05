package controller

import (
	"context"
	"net/http"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relay/channel/zhipu_4v"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
)

type zhipuOAuthExchangeRequest struct {
	Input string `json:"input"`
	State string `json:"state"`
}

// StartZhipuOAuthLogin returns the bigmodel.cn authorize URL for the BigModel
// Subscription (Coding Plan) channel OAuth login. The caller opens the URL in
// a browser, logs in, cancels the system "open ZCode?" prompt, and pastes the
// zcode://oauth/callback link back into ExchangeZhipuOAuthToken.
func StartZhipuOAuthLogin(c *gin.Context) {
	state, err := service.GenerateZhipuOAuthState()
	if err != nil {
		common.ApiError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data": gin.H{
			"authorize_url": service.BuildZhipuOAuthAuthorizeURL(state),
			"state":         state,
		},
	})
}

// ExchangeZhipuOAuthToken swaps the pasted OAuth callback (or bare
// authorization code) for a complete BigModel Subscription channel credential,
// including the coding plan API key derived from the OAuth access token.
func ExchangeZhipuOAuthToken(c *gin.Context) {
	var request zhipuOAuthExchangeRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		common.ApiError(c, err)
		return
	}
	if request.State == "" {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "state is required, please restart OAuth login"})
		return
	}
	code, err := service.ParseZhipuOAuthCallback(request.Input, request.State)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}

	client, err := service.NewProxyHttpClient("")
	if err != nil {
		common.ApiError(c, err)
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()

	tokenSet, err := service.ExchangeZhipuOAuthCode(ctx, client, code, request.State)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}

	apiKey, customer, err := service.DeriveZhipuCodingPlanAPIKey(ctx, client, tokenSet.AccessToken)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}

	username := tokenSet.Username
	if username == "" && customer != nil {
		username = customer.CustomerName
	}
	credential := zhipu_4v.OAuthCredential{
		APIKey:       apiKey,
		AccessToken:  tokenSet.AccessToken,
		RefreshToken: tokenSet.RefreshToken,
		OAuthUser:    username,
	}
	credentialJSON, err := common.Marshal(credential)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data": gin.H{
			"credential": string(credentialJSON),
			"username":   username,
		},
	})
}
