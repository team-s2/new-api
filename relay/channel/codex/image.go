package codex

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
)

const maxCodexImageEditInputs = 16

func (a *Adaptor) ConvertImageRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.ImageRequest) (any, error) {
	if info == nil || (info.RelayMode != relayconstant.RelayModeImagesGenerations && info.RelayMode != relayconstant.RelayModeImagesEdits) {
		return nil, newCodexImageRequestError("codex channel: endpoint not supported")
	}
	if strings.TrimSpace(request.Model) != codexImageModel {
		return nil, newCodexImageRequestError("codex channel: image endpoints only support %s, got %q", codexImageModel, request.Model)
	}

	if info.RelayMode == relayconstant.RelayModeImagesEdits && isCodexMultipartImageEdit(c) {
		converted, err := convertCodexMultipartImageEdit(c, request)
		if err != nil {
			return nil, newCodexImageRequestError("codex channel: invalid multipart image edit request: %v", err)
		}
		return converted, nil
	}

	if err := validateCodexImageResponseFormat(request.ResponseFormat); err != nil {
		return nil, err
	}
	if err := validateCodexOutputCompression(request.OutputCompression); err != nil {
		return nil, err
	}
	if err := validateCodexPartialImages(request.PartialImages); err != nil {
		return nil, err
	}
	if info.RelayMode == relayconstant.RelayModeImagesEdits {
		if err := validateCodexJSONImageEditCount(request.Images); err != nil {
			return nil, err
		}
	}

	converted, err := convertCodexJSONImageRequest(request)
	if err != nil {
		return nil, newCodexImageRequestError("codex channel: failed to convert image request: %v", err)
	}
	return converted, nil
}

func convertCodexJSONImageRequest(request dto.ImageRequest) (map[string]json.RawMessage, error) {
	data, err := common.Marshal(request)
	if err != nil {
		return nil, err
	}

	var converted map[string]json.RawMessage
	if err := common.Unmarshal(data, &converted); err != nil {
		return nil, err
	}
	for name, value := range request.Extra {
		if _, exists := converted[name]; exists {
			continue
		}
		converted[name] = value
	}
	delete(converted, "response_format")
	return converted, nil
}

func isCodexMultipartImageEdit(c *gin.Context) bool {
	if c == nil || c.Request == nil {
		return false
	}
	return c.Request.MultipartForm != nil || strings.Contains(strings.ToLower(c.Request.Header.Get("Content-Type")), "multipart/form-data")
}

func convertCodexMultipartImageEdit(c *gin.Context, request dto.ImageRequest) (relaycommon.EncodedJSONRequest, error) {
	if c == nil || c.Request == nil || c.Request.MultipartForm == nil {
		return nil, fmt.Errorf("parsed multipart form is required")
	}
	form := c.Request.MultipartForm

	responseFormat := ""
	if values := form.Value["response_format"]; len(values) > 0 {
		responseFormat = strings.TrimSpace(values[0])
	}
	if err := validateCodexImageResponseFormat(responseFormat); err != nil {
		return nil, err
	}

	converted := make(map[string]json.RawMessage, len(form.Value)+3)
	for name, values := range form.Value {
		if name == "model" || name == "response_format" || name == "n" || name == "partial_images" || name == "stream" || len(values) == 0 {
			continue
		}
		value, err := marshalCodexMultipartImageField(name, values[0])
		if err != nil {
			return nil, err
		}
		converted[name] = value
	}

	modelJSON, err := common.Marshal(request.Model)
	if err != nil {
		return nil, err
	}
	converted["model"] = modelJSON
	if request.N != nil {
		nJSON, err := common.Marshal(*request.N)
		if err != nil {
			return nil, err
		}
		converted["n"] = nJSON
	}
	if len(request.PartialImages) > 0 {
		if err := validateCodexPartialImages(request.PartialImages); err != nil {
			return nil, err
		}
		converted["partial_images"] = request.PartialImages
	}
	if request.Stream != nil {
		streamJSON, err := common.Marshal(*request.Stream)
		if err != nil {
			return nil, err
		}
		converted["stream"] = streamJSON
	}

	imageFiles := codexMultipartImageFiles(form)
	if len(imageFiles) == 0 {
		return nil, fmt.Errorf("image is required")
	}
	if len(imageFiles) > maxCodexImageEditInputs {
		return nil, fmt.Errorf("image edit accepts at most %d input images", maxCodexImageEditInputs)
	}
	delete(converted, "images")
	maskFiles := form.File["mask"]
	if len(maskFiles) > 0 {
		delete(converted, "mask")
	}

	imageBytes := int64(0)
	for _, fileHeader := range imageFiles {
		if fileHeader != nil && fileHeader.Size > 0 {
			imageBytes += fileHeader.Size
		}
	}
	estimatedSize := base64.StdEncoding.EncodedLen(int(imageBytes)) + len(imageFiles)*32 + 32
	for name, value := range converted {
		estimatedSize += len(name) + len(value) + 6
	}
	if len(maskFiles) > 0 && maskFiles[0] != nil && maskFiles[0].Size > 0 {
		estimatedSize += base64.StdEncoding.EncodedLen(int(maskFiles[0].Size)) + 32
	}

	var payload bytes.Buffer
	payload.Grow(estimatedSize)
	payload.WriteByte('{')
	firstField := true
	for name, value := range converted {
		if !firstField {
			payload.WriteByte(',')
		}
		nameJSON, err := common.Marshal(name)
		if err != nil {
			return nil, err
		}
		payload.Write(nameJSON)
		payload.WriteByte(':')
		payload.Write(value)
		firstField = false
	}
	if !firstField {
		payload.WriteByte(',')
	}
	payload.WriteString(`"images":[`)
	for i, fileHeader := range imageFiles {
		if i > 0 {
			payload.WriteByte(',')
		}
		payload.WriteString(`{"image_url":`)
		if err := writeCodexMultipartFileDataURL(&payload, fileHeader); err != nil {
			return nil, err
		}
		payload.WriteByte('}')
	}
	payload.WriteByte(']')

	if len(maskFiles) > 0 {
		payload.WriteString(`,"mask":{"image_url":`)
		if err := writeCodexMultipartFileDataURL(&payload, maskFiles[0]); err != nil {
			return nil, err
		}
		payload.WriteByte('}')
	}
	payload.WriteByte('}')

	return relaycommon.EncodedJSONRequest(payload.Bytes()), nil
}

func marshalCodexMultipartImageField(name string, value string) (json.RawMessage, error) {
	trimmed := strings.TrimSpace(value)
	switch name {
	case "output_compression":
		parsed, err := strconv.ParseInt(trimmed, 10, 64)
		if err != nil || parsed < 0 || parsed > 100 {
			return nil, fmt.Errorf("output_compression must be an integer between 0 and 100")
		}
		return common.Marshal(parsed)
	case "stream", "watermark":
		parsed, err := strconv.ParseBool(trimmed)
		if err != nil {
			return nil, fmt.Errorf("%s must be a boolean", name)
		}
		return common.Marshal(parsed)
	default:
		return common.Marshal(value)
	}
}

func codexMultipartImageFiles(form *multipart.Form) []*multipart.FileHeader {
	if form == nil {
		return nil
	}

	files := make([]*multipart.FileHeader, 0)
	files = append(files, form.File["image"]...)
	files = append(files, form.File["image[]"]...)

	indexedFields := make([]string, 0)
	for name := range form.File {
		if codexMultipartImageIndex(name) >= 0 {
			indexedFields = append(indexedFields, name)
		}
	}
	sort.Slice(indexedFields, func(i, j int) bool {
		left := codexMultipartImageIndex(indexedFields[i])
		right := codexMultipartImageIndex(indexedFields[j])
		if left == right {
			return indexedFields[i] < indexedFields[j]
		}
		return left < right
	})
	for _, name := range indexedFields {
		files = append(files, form.File[name]...)
	}
	return files
}

func codexMultipartImageIndex(name string) int {
	if !strings.HasPrefix(name, "image[") || !strings.HasSuffix(name, "]") || name == "image[]" {
		return -1
	}
	index, err := strconv.Atoi(name[len("image[") : len(name)-1])
	if err != nil || index < 0 {
		return -1
	}
	return index
}

func writeCodexMultipartFileDataURL(writer io.Writer, fileHeader *multipart.FileHeader) error {
	if fileHeader == nil {
		return fmt.Errorf("image file is missing")
	}
	file, err := fileHeader.Open()
	if err != nil {
		return fmt.Errorf("failed to open image file %q: %w", fileHeader.Filename, err)
	}
	defer file.Close()

	mediaType := strings.TrimSpace(fileHeader.Header.Get("Content-Type"))
	if parsed, _, parseErr := mime.ParseMediaType(mediaType); parseErr == nil && parsed != "" {
		mediaType = parsed
	} else {
		mediaType = ""
	}
	if mediaType == "" || mediaType == "application/octet-stream" {
		var sample [512]byte
		n, readErr := file.Read(sample[:])
		if readErr != nil && readErr != io.EOF {
			return fmt.Errorf("failed to inspect image file %q: %w", fileHeader.Filename, readErr)
		}
		mediaType = http.DetectContentType(sample[:n])
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			return fmt.Errorf("failed to rewind image file %q: %w", fileHeader.Filename, err)
		}
	}
	if _, err := io.WriteString(writer, `"data:`+mediaType+`;base64,`); err != nil {
		return fmt.Errorf("failed to encode image file %q: %w", fileHeader.Filename, err)
	}
	encoder := base64.NewEncoder(base64.StdEncoding, writer)
	if _, err := io.Copy(encoder, file); err != nil {
		_ = encoder.Close()
		return fmt.Errorf("failed to read image file %q: %w", fileHeader.Filename, err)
	}
	if err := encoder.Close(); err != nil {
		return fmt.Errorf("failed to encode image file %q: %w", fileHeader.Filename, err)
	}
	if _, err := io.WriteString(writer, `"`); err != nil {
		return fmt.Errorf("failed to encode image file %q: %w", fileHeader.Filename, err)
	}
	return nil
}

func validateCodexImageResponseFormat(responseFormat string) error {
	responseFormat = strings.TrimSpace(responseFormat)
	if responseFormat == "" || strings.EqualFold(responseFormat, "b64_json") {
		return nil
	}
	return newCodexImageRequestError("codex channel: response_format must be b64_json when provided")
}

func validateCodexOutputCompression(raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	var value int
	if common.GetJsonType(raw) != "number" || common.Unmarshal(raw, &value) != nil || value < 0 || value > 100 {
		return newCodexImageRequestError("codex channel: output_compression must be an integer between 0 and 100")
	}
	return nil
}

func validateCodexPartialImages(raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	var value int
	if common.GetJsonType(raw) != "number" || common.Unmarshal(raw, &value) != nil || value < 0 || value > dto.MaxPartialImages {
		return newCodexImageRequestError("codex channel: partial_images must be an integer between 0 and %d", dto.MaxPartialImages)
	}
	return nil
}

func validateCodexJSONImageEditCount(raw json.RawMessage) error {
	if len(raw) == 0 || common.GetJsonType(raw) != "array" {
		return nil
	}
	var images []json.RawMessage
	if err := common.Unmarshal(raw, &images); err != nil {
		return newCodexImageRequestError("codex channel: images must be a valid array")
	}
	if len(images) > maxCodexImageEditInputs {
		return newCodexImageRequestError("codex channel: image edit accepts at most %d input images", maxCodexImageEditInputs)
	}
	return nil
}

func newCodexImageRequestError(format string, args ...any) *types.NewAPIError {
	return types.NewErrorWithStatusCode(
		fmt.Errorf(format, args...),
		types.ErrorCodeInvalidRequest,
		http.StatusBadRequest,
		types.ErrOptionWithSkipRetry(),
	)
}
