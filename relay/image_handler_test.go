package relay

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/setting/model_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type capturedImageHandlerRequest struct {
	body        []byte
	contentType string
	path        string
	err         error
}

func newImageHandlerTestUpstream(t *testing.T) (*httptest.Server, <-chan capturedImageHandlerRequest) {
	t.Helper()
	captured := make(chan capturedImageHandlerRequest, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		captured <- capturedImageHandlerRequest{
			body:        body,
			contentType: r.Header.Get("Content-Type"),
			path:        r.URL.Path,
			err:         err,
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"stop after capture","type":"invalid_request_error"}}`))
	}))
	t.Cleanup(server.Close)
	return server, captured
}

func setImageHandlerTestChannel(c *gin.Context, channelType int, baseURL string, channelSettings dto.ChannelSettings) {
	common.SetContextKey(c, constant.ContextKeyOriginalModel, "gpt-image-2")
	common.SetContextKey(c, constant.ContextKeyChannelType, channelType)
	common.SetContextKey(c, constant.ContextKeyChannelBaseUrl, baseURL)
	common.SetContextKey(c, constant.ContextKeyChannelSetting, channelSettings)
	if channelType == constant.ChannelTypeCodex {
		common.SetContextKey(c, constant.ContextKeyChannelKey, `{"access_token":"test-token","account_id":"test-account"}`)
	} else {
		common.SetContextKey(c, constant.ContextKeyChannelKey, "test-key")
	}
}

func TestImageHelperForcesCodexMultipartEditConversionAndRedactsDebugLog(t *testing.T) {
	gin.SetMode(gin.TestMode)
	originalGlobalPassThrough := model_setting.GetGlobalSettings().PassThroughRequestEnabled
	originalDebugEnabled := common.DebugEnabled
	t.Cleanup(func() {
		model_setting.GetGlobalSettings().PassThroughRequestEnabled = originalGlobalPassThrough
		common.DebugEnabled = originalDebugEnabled
	})

	var logOutput bytes.Buffer
	common.LogWriterMu.Lock()
	originalErrorWriter := gin.DefaultErrorWriter
	gin.DefaultErrorWriter = &logOutput
	common.LogWriterMu.Unlock()
	t.Cleanup(func() {
		common.LogWriterMu.Lock()
		gin.DefaultErrorWriter = originalErrorWriter
		common.LogWriterMu.Unlock()
	})
	common.DebugEnabled = true

	tests := []struct {
		name               string
		globalPassThrough  bool
		channelPassThrough bool
	}{
		{name: "global pass-through", globalPassThrough: true},
		{name: "channel pass-through", channelPassThrough: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logOutput.Reset()
			model_setting.GetGlobalSettings().PassThroughRequestEnabled = tt.globalPassThrough
			server, captured := newImageHandlerTestUpstream(t)

			imageBytes := []byte("sensitive image input")
			var multipartBody bytes.Buffer
			writer := multipart.NewWriter(&multipartBody)
			require.NoError(t, writer.WriteField("model", "gpt-image-2"))
			require.NoError(t, writer.WriteField("prompt", "sensitive edit prompt"))
			part, err := writer.CreateFormFile("image", "input.png")
			require.NoError(t, err)
			_, err = part.Write(imageBytes)
			require.NoError(t, err)
			require.NoError(t, writer.Close())

			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/edits", bytes.NewReader(multipartBody.Bytes()))
			c.Request.Header.Set("Content-Type", writer.FormDataContentType())
			setImageHandlerTestChannel(c, constant.ChannelTypeCodex, server.URL, dto.ChannelSettings{
				PassThroughBodyEnabled: tt.channelPassThrough,
				Proxy:                  server.URL,
			})

			request, err := helper.GetAndValidOpenAIImageRequest(c, relayconstant.RelayModeImagesEdits)
			require.NoError(t, err)
			info := relaycommon.GenRelayInfoImage(c, request)
			handlerErr := ImageHelper(c, info)
			require.Error(t, handlerErr)

			var upstreamRequest capturedImageHandlerRequest
			select {
			case upstreamRequest = <-captured:
			default:
				require.FailNow(t, "Codex edit did not reach the upstream test server")
			}
			require.NoError(t, upstreamRequest.err)
			assert.Equal(t, "/backend-api/codex/images/edits", upstreamRequest.path)
			assert.Equal(t, "application/json", upstreamRequest.contentType)

			var converted map[string]json.RawMessage
			require.NoError(t, common.Unmarshal(upstreamRequest.body, &converted))
			var images []struct {
				ImageURL string `json:"image_url"`
			}
			require.NoError(t, common.Unmarshal(converted["images"], &images))
			require.Len(t, images, 1)
			encodedImage := base64.StdEncoding.EncodeToString(imageBytes)
			assert.Contains(t, images[0].ImageURL, encodedImage)

			logs := logOutput.String()
			assert.Contains(t, logs, "image request body: size=")
			assert.Contains(t, logs, `model="gpt-image-2"`)
			assert.NotContains(t, logs, encodedImage)
			assert.NotContains(t, logs, "sensitive edit prompt")
		})
	}
}

func TestImageHelperConvertsCodexGenerationWithPassThrough(t *testing.T) {
	originalGlobalPassThrough := model_setting.GetGlobalSettings().PassThroughRequestEnabled
	t.Cleanup(func() {
		model_setting.GetGlobalSettings().PassThroughRequestEnabled = originalGlobalPassThrough
	})

	tests := []struct {
		name               string
		globalPassThrough  bool
		channelPassThrough bool
	}{
		{name: "global pass-through", globalPassThrough: true},
		{name: "channel pass-through", channelPassThrough: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			model_setting.GetGlobalSettings().PassThroughRequestEnabled = tt.globalPassThrough
			server, captured := newImageHandlerTestUpstream(t)
			originalBody := []byte("{\n  \"model\": \"gpt-image-2\",\n  \"prompt\": \"a cat\",\n  \"response_format\": \"b64_json\"\n}\n")
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(originalBody))
			c.Request.Header.Set("Content-Type", "application/json")
			setImageHandlerTestChannel(c, constant.ChannelTypeCodex, server.URL, dto.ChannelSettings{
				PassThroughBodyEnabled: tt.channelPassThrough,
				Proxy:                  server.URL,
			})

			request, err := helper.GetAndValidOpenAIImageRequest(c, relayconstant.RelayModeImagesGenerations)
			require.NoError(t, err)
			info := relaycommon.GenRelayInfoImage(c, request)
			handlerErr := ImageHelper(c, info)
			require.Error(t, handlerErr)

			var upstreamRequest capturedImageHandlerRequest
			select {
			case upstreamRequest = <-captured:
			default:
				require.FailNow(t, "Codex generation did not reach the upstream test server")
			}
			require.NoError(t, upstreamRequest.err)
			assert.Equal(t, "/backend-api/codex/images/generations", upstreamRequest.path)
			assert.Equal(t, "application/json", upstreamRequest.contentType)

			var converted map[string]json.RawMessage
			require.NoError(t, common.Unmarshal(upstreamRequest.body, &converted))
			assert.JSONEq(t, `"gpt-image-2"`, string(converted["model"]))
			assert.JSONEq(t, `"a cat"`, string(converted["prompt"]))
			assert.Equal(t, "1", string(converted["n"]))
			assert.NotContains(t, converted, "response_format")
		})
	}
}

func TestImageHelperPreservesOtherChannelEditPassThrough(t *testing.T) {
	originalGlobalPassThrough := model_setting.GetGlobalSettings().PassThroughRequestEnabled
	model_setting.GetGlobalSettings().PassThroughRequestEnabled = false
	t.Cleanup(func() {
		model_setting.GetGlobalSettings().PassThroughRequestEnabled = originalGlobalPassThrough
	})

	server, captured := newImageHandlerTestUpstream(t)
	var multipartBody bytes.Buffer
	writer := multipart.NewWriter(&multipartBody)
	require.NoError(t, writer.WriteField("model", "gpt-image-2"))
	require.NoError(t, writer.WriteField("prompt", "edit this image"))
	part, err := writer.CreateFormFile("image", "input.png")
	require.NoError(t, err)
	_, err = part.Write([]byte("image input"))
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	originalBody := append([]byte(nil), multipartBody.Bytes()...)

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/edits", bytes.NewReader(originalBody))
	c.Request.Header.Set("Content-Type", writer.FormDataContentType())
	setImageHandlerTestChannel(c, constant.ChannelTypeOpenAI, server.URL, dto.ChannelSettings{
		PassThroughBodyEnabled: true,
		Proxy:                  server.URL,
	})

	request, err := helper.GetAndValidOpenAIImageRequest(c, relayconstant.RelayModeImagesEdits)
	require.NoError(t, err)
	info := relaycommon.GenRelayInfoImage(c, request)
	handlerErr := ImageHelper(c, info)
	require.Error(t, handlerErr)

	var upstreamRequest capturedImageHandlerRequest
	select {
	case upstreamRequest = <-captured:
	default:
		require.FailNow(t, "OpenAI edit did not reach the upstream test server")
	}
	require.NoError(t, upstreamRequest.err)
	assert.Equal(t, "/v1/images/edits", upstreamRequest.path)
	assert.Equal(t, originalBody, upstreamRequest.body)
}
