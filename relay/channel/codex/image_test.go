package codex

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCodexModelListIncludesImageModelWithoutCompactVariant(t *testing.T) {
	assert.Contains(t, ModelList, codexImageModel)
	assert.NotContains(t, ModelList, codexImageModel+"-openai-compact")
}

func TestConvertCodexJSONImageRequestPreservesFieldsAndExtra(t *testing.T) {
	zero := uint(0)
	stream := false
	request := dto.ImageRequest{
		Model:          codexImageModel,
		Prompt:         "draw an otter",
		N:              &zero,
		ResponseFormat: "b64_json",
		PartialImages:  json.RawMessage("0"),
		Stream:         &stream,
		Extra: map[string]json.RawMessage{
			"custom_zero":  json.RawMessage("0"),
			"custom_false": json.RawMessage("false"),
			"prompt":       json.RawMessage(`"must not replace the known prompt"`),
		},
	}

	convertedAny, err := (&Adaptor{}).ConvertImageRequest(nil, &relaycommon.RelayInfo{
		RelayMode: relayconstant.RelayModeImagesGenerations,
	}, request)
	require.NoError(t, err)
	converted, ok := convertedAny.(map[string]json.RawMessage)
	require.True(t, ok)

	assert.JSONEq(t, `"draw an otter"`, string(converted["prompt"]))
	assert.Equal(t, "0", string(converted["n"]))
	assert.Equal(t, "0", string(converted["partial_images"]))
	assert.Equal(t, "false", string(converted["stream"]))
	assert.Equal(t, "0", string(converted["custom_zero"]))
	assert.Equal(t, "false", string(converted["custom_false"]))
	assert.NotContains(t, converted, "response_format")
}

func TestConvertCodexImageRequestRejectsInvalidClientInputWithoutRetry(t *testing.T) {
	tests := []struct {
		name    string
		request dto.ImageRequest
	}{
		{
			name: "mapped model",
			request: dto.ImageRequest{
				Model:  "gpt-image-1.5",
				Prompt: "draw",
			},
		},
		{
			name: "response format",
			request: dto.ImageRequest{
				Model:          codexImageModel,
				Prompt:         "draw",
				ResponseFormat: "url",
			},
		},
		{
			name: "partial image bound",
			request: dto.ImageRequest{
				Model:         codexImageModel,
				Prompt:        "draw",
				PartialImages: json.RawMessage(fmt.Sprintf("%d", dto.MaxPartialImages+1)),
			},
		},
		{
			name: "output compression bound",
			request: dto.ImageRequest{
				Model:             codexImageModel,
				Prompt:            "draw",
				OutputCompression: json.RawMessage("101"),
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := (&Adaptor{}).ConvertImageRequest(nil, &relaycommon.RelayInfo{
				RelayMode: relayconstant.RelayModeImagesGenerations,
			}, test.request)
			requireCodexBadRequest(t, err)
		})
	}
}

func TestConvertCodexMultipartImageEditToJSON(t *testing.T) {
	c, request := newCodexMultipartImageEditContext(t, map[string]string{
		"model":              codexImageModel,
		"prompt":             "replace the background",
		"n":                  "0",
		"partial_images":     "0",
		"stream":             "false",
		"output_compression": "0",
		"response_format":    "b64_json",
		"custom_field":       "preserve me",
		"images":             "must be replaced by uploaded images",
		"mask":               "must be replaced by the uploaded mask",
	}, []codexMultipartTestFile{
		{field: "image", filename: "main.png", mediaType: "image/png", data: "main-image"},
		{field: "image[]", filename: "array.jpg", mediaType: "image/jpeg", data: "array-image"},
		{field: "image[2]", filename: "two.webp", mediaType: "image/webp", data: "indexed-two"},
		{field: "image[1]", filename: "one.png", mediaType: "image/png", data: "indexed-one"},
		{field: "mask", filename: "mask.png", mediaType: "image/png", data: "mask-image"},
	})

	// The common validator normalizes n=0 to the API default while preserving
	// explicit zero/false for partial_images and stream.
	require.NotNil(t, request.N)
	assert.Equal(t, uint(1), *request.N)

	convertedAny, err := (&Adaptor{}).ConvertImageRequest(c, &relaycommon.RelayInfo{
		RelayMode: relayconstant.RelayModeImagesEdits,
	}, *request)
	require.NoError(t, err)
	converted, ok := convertedAny.(relaycommon.EncodedJSONRequest)
	require.True(t, ok)
	require.True(t, json.Valid(converted))
	assert.Equal(t, 1, strings.Count(string(converted), `"images":`))
	assert.Equal(t, 1, strings.Count(string(converted), `"mask":`))

	var payload map[string]any
	require.NoError(t, common.Unmarshal(converted, &payload))

	assert.Equal(t, codexImageModel, payload["model"])
	assert.Equal(t, "replace the background", payload["prompt"])
	assert.Equal(t, float64(1), payload["n"])
	assert.Equal(t, float64(0), payload["partial_images"])
	assert.Equal(t, false, payload["stream"])
	assert.Equal(t, float64(0), payload["output_compression"])
	assert.Equal(t, "preserve me", payload["custom_field"])
	assert.NotContains(t, payload, "response_format")

	images, ok := payload["images"].([]any)
	require.True(t, ok)
	require.Len(t, images, 4)
	expected := []struct {
		mediaType string
		data      string
	}{
		{mediaType: "image/png", data: "main-image"},
		{mediaType: "image/jpeg", data: "array-image"},
		{mediaType: "image/png", data: "indexed-one"},
		{mediaType: "image/webp", data: "indexed-two"},
	}
	for i, expectedImage := range expected {
		image, ok := images[i].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, imageDataURL(expectedImage.mediaType, expectedImage.data), image["image_url"])
	}
	mask, ok := payload["mask"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, imageDataURL("image/png", "mask-image"), mask["image_url"])
}

func TestConvertCodexMultipartImageEditEnforcesLimits(t *testing.T) {
	t.Run("image count", func(t *testing.T) {
		files := make([]codexMultipartTestFile, maxCodexImageEditInputs+1)
		for i := range files {
			files[i] = codexMultipartTestFile{
				field:     "image[]",
				filename:  fmt.Sprintf("%d.png", i),
				mediaType: "image/png",
				data:      "image",
			}
		}
		c, request := newCodexMultipartImageEditContext(t, map[string]string{
			"model":  codexImageModel,
			"prompt": "edit",
		}, files)

		_, err := (&Adaptor{}).ConvertImageRequest(c, &relaycommon.RelayInfo{
			RelayMode: relayconstant.RelayModeImagesEdits,
		}, *request)
		requireCodexBadRequest(t, err)
	})

	t.Run("output compression", func(t *testing.T) {
		c, request := newCodexMultipartImageEditContext(t, map[string]string{
			"model":              codexImageModel,
			"prompt":             "edit",
			"output_compression": "101",
		}, []codexMultipartTestFile{
			{field: "image", filename: "input.png", mediaType: "image/png", data: "image"},
		})

		_, err := (&Adaptor{}).ConvertImageRequest(c, &relaycommon.RelayInfo{
			RelayMode: relayconstant.RelayModeImagesEdits,
		}, *request)
		requireCodexBadRequest(t, err)
	})
}

func TestCodexImageRequestURLs(t *testing.T) {
	tests := []struct {
		mode int
		path string
	}{
		{mode: relayconstant.RelayModeImagesGenerations, path: "/backend-api/codex/images/generations"},
		{mode: relayconstant.RelayModeImagesEdits, path: "/backend-api/codex/images/edits"},
	}
	for _, test := range tests {
		url, err := (&Adaptor{}).GetRequestURL(&relaycommon.RelayInfo{
			RelayMode: test.mode,
			ChannelMeta: &relaycommon.ChannelMeta{
				ChannelBaseUrl:    "https://chatgpt.com",
				UpstreamModelName: codexImageModel,
			},
		})
		require.NoError(t, err)
		assert.Equal(t, "https://chatgpt.com"+test.path, url)
	}

	_, err := (&Adaptor{}).GetRequestURL(&relaycommon.RelayInfo{
		RelayMode: relayconstant.RelayModeImagesGenerations,
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelBaseUrl:    "https://chatgpt.com",
			UpstreamModelName: "gpt-image-1.5",
		},
	})
	requireCodexBadRequest(t, err)
}

func TestCodexImageRequestHeaders(t *testing.T) {
	tests := []struct {
		name           string
		stream         bool
		originator     string
		wantOriginator string
		wantAccept     string
	}{
		{name: "json defaults", wantOriginator: codexImageOriginator, wantAccept: "application/json"},
		{name: "stream preserves originator", stream: true, originator: "custom-originator", wantOriginator: "custom-originator", wantAccept: "text/event-stream"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader("{}"))
			c.Request.Header.Set("Content-Type", "application/json; charset=utf-8")
			header := http.Header{
				"OpenAI-Beta": []string{"responses=experimental"},
			}
			if test.originator != "" {
				header.Set("Originator", test.originator)
			}
			info := &relaycommon.RelayInfo{
				RelayMode: relayconstant.RelayModeImagesGenerations,
				IsStream:  test.stream,
				ChannelMeta: &relaycommon.ChannelMeta{
					ApiKey: `{"access_token":"oauth-token","account_id":"account-123"}`,
				},
			}

			err := (&Adaptor{}).SetupRequestHeader(c, &header, info)
			require.NoError(t, err)
			assert.Equal(t, "Bearer oauth-token", header.Get("Authorization"))
			assert.Equal(t, "account-123", header.Get("Chatgpt-Account-Id"))
			assert.Equal(t, test.wantOriginator, header.Get("Originator"))
			assert.NotEmpty(t, header.Get("Session_id"))
			assert.Equal(t, codexUserAgent, header.Get("User-Agent"))
			assert.Equal(t, "application/json", header.Get("Content-Type"))
			assert.Equal(t, test.wantAccept, header.Get("Accept"))
			assert.Empty(t, header.Get("OpenAI-Beta"))
		})
	}
}

func TestCodexImageHeaderOverrideWinsOverDefaultUserAgent(t *testing.T) {
	service.InitHttpClient()
	var upstreamUserAgent string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamUserAgent = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer server.Close()

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader("{}"))
	c.Request.Header.Set("Content-Type", "application/json")
	info := &relaycommon.RelayInfo{
		RelayMode: relayconstant.RelayModeImagesGenerations,
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType:       constant.ChannelTypeCodex,
			ChannelBaseUrl:    server.URL,
			ApiKey:            `{"access_token":"oauth-token","account_id":"account-123"}`,
			UpstreamModelName: codexImageModel,
			HeadersOverride: map[string]interface{}{
				"User-Agent": "custom-codex-client/1.0",
			},
		},
	}

	respAny, err := (&Adaptor{}).DoRequest(c, info, strings.NewReader("{}"))
	require.NoError(t, err)
	resp, ok := respAny.(*http.Response)
	require.True(t, ok)
	defer resp.Body.Close()
	_, err = io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, "custom-codex-client/1.0", upstreamUserAgent)
}

func TestCodexDoResponseUsesOpenAIImageHandler(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
	upstreamBody := `{"created":1713833628,"data":[{"b64_json":"AA=="}],"usage":{"input_tokens":4,"output_tokens":6,"total_tokens":10}}`
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(upstreamBody)),
	}

	usageAny, apiErr := (&Adaptor{}).DoResponse(c, resp, &relaycommon.RelayInfo{
		RelayMode: relayconstant.RelayModeImagesGenerations,
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType: constant.ChannelTypeCodex,
		},
	})
	require.Nil(t, apiErr)
	usage, ok := usageAny.(*dto.Usage)
	require.True(t, ok)
	assert.Equal(t, 4, usage.PromptTokens)
	assert.Equal(t, 6, usage.CompletionTokens)
	assert.JSONEq(t, upstreamBody, recorder.Body.String())
}

type codexMultipartTestFile struct {
	field     string
	filename  string
	mediaType string
	data      string
}

func newCodexMultipartImageEditContext(t *testing.T, fields map[string]string, files []codexMultipartTestFile) (*gin.Context, *dto.ImageRequest) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for name, value := range fields {
		require.NoError(t, writer.WriteField(name, value))
	}
	for _, file := range files {
		header := make(textproto.MIMEHeader)
		header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"; filename="%s"`, file.field, file.filename))
		header.Set("Content-Type", file.mediaType)
		part, err := writer.CreatePart(header)
		require.NoError(t, err)
		_, err = part.Write([]byte(file.data))
		require.NoError(t, err)
	}
	require.NoError(t, writer.Close())

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/edits", bytes.NewReader(body.Bytes()))
	c.Request.Header.Set("Content-Type", writer.FormDataContentType())
	request, err := helper.GetAndValidOpenAIImageRequest(c, relayconstant.RelayModeImagesEdits)
	require.NoError(t, err)
	return c, request
}

func imageDataURL(mediaType string, data string) string {
	return "data:" + mediaType + ";base64," + base64.StdEncoding.EncodeToString([]byte(data))
}

func requireCodexBadRequest(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err)
	var apiErr *types.NewAPIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusBadRequest, apiErr.StatusCode)
	assert.True(t, types.IsSkipRetryError(apiErr))
}
