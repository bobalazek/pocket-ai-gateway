package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"

	_ "golang.org/x/image/webp"
)

const (
	maxImageMultipartBody = 64 << 20
	maxImageEditFile      = 50_000_000
	maxImageVariationFile = 4_000_000
	maxImageMask          = 4_000_000
)

func (handler *Handler) imageEdit(response http.ResponseWriter, request *http.Request) {
	handler.imageMultipart(response, request, "images:edit", "images/edits", false)
}

func (handler *Handler) imageVariation(response http.ResponseWriter, request *http.Request) {
	handler.imageMultipart(response, request, "images:variation", "images/variations", true)
}

func (handler *Handler) imageMultipart(response http.ResponseWriter, request *http.Request, scope, upstreamPath string, variation bool) {
	principal, ok := handler.authenticate(response, request, "openai")
	if !ok {
		return
	}
	if !principal.AllowsScope(scope) {
		handler.writeError(response, "openai", http.StatusNotFound, "model_not_found", "Model is unavailable")
		return
	}
	if !handler.acquireMultipart(response, "openai") {
		return
	}
	defer handler.releaseMultipart()
	body, err := io.ReadAll(http.MaxBytesReader(response, request.Body, maxImageMultipartBody))
	if err != nil {
		handler.writeError(response, "openai", http.StatusRequestEntityTooLarge, "request_too_large", "Multipart request exceeds 64 MiB")
		return
	}
	input, err := validateImageMultipart(body, request.Header.Get("Content-Type"), variation)
	if err != nil {
		handler.writeError(response, "openai", http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	envelope, _ := json.Marshal(map[string]any{"model": input.model, "n": input.n, "stream": input.stream, "partial_images": input.partialImages})
	request = request.WithContext(context.WithValue(request.Context(), nativeMultipartRequestKey{}, nativeMultipartRequest{body: body, contentType: request.Header.Get("Content-Type")}))
	handler.forwardAuthorized(response, request, "openai", scope, upstreamPath, input.model, nil, principal, envelope)
}

type imageMultipartInput struct {
	model         string
	n             int64
	stream        bool
	partialImages int64
}

func validateImageMultipart(body []byte, contentType string, variation bool) (imageMultipartInput, error) {
	mediaType, parameters, err := mime.ParseMediaType(contentType)
	if err != nil || mediaType != "multipart/form-data" || parameters["boundary"] == "" {
		return imageMultipartInput{}, errors.New("content type must be multipart/form-data with a boundary")
	}
	reader := multipart.NewReader(bytes.NewReader(body), parameters["boundary"])
	model, prompt := "", ""
	n := int64(1)
	stream := false
	partialImages := int64(0)
	partialImagesSet := false
	images, masks, parts := 0, 0, 0
	var firstImage, mask image.Config
	fields := map[string]int{}
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return imageMultipartInput{}, errors.New("multipart request is invalid")
		}
		parts++
		if parts > 1000 {
			return imageMultipartInput{}, errors.New("multipart request has too many fields")
		}
		name := part.FormName()
		if name == "image[]" {
			name = "image"
		} else if strings.HasSuffix(name, "[]") {
			return imageMultipartInput{}, errors.New("only image may use array field syntax")
		}
		if part.FileName() != "" {
			switch name {
			case "image":
				images++
				if variation && images > 1 || !variation && images > 16 {
					return imageMultipartInput{}, errors.New("too many image uploads")
				}
				limit := int64(maxImageEditFile)
				if variation {
					limit = maxImageVariationFile
				}
				configuration, format, readErr := readImageConfig(part, limit)
				if readErr != nil || variation && format != "png" {
					return imageMultipartInput{}, errors.New("image must be a non-empty supported upload")
				}
				if images == 1 {
					firstImage = configuration
				}
				if variation {
					if configuration.Width != configuration.Height {
						return imageMultipartInput{}, errors.New("image variation requires a square PNG")
					}
				}
			case "mask":
				masks++
				if variation || masks > 1 {
					return imageMultipartInput{}, errors.New("mask is supported only once for image edits")
				}
				configuration, format, readErr := readImageConfig(part, maxImageMask)
				if readErr != nil || format != "png" {
					return imageMultipartInput{}, errors.New("mask must be a non-empty PNG under 4 MB")
				}
				mask = configuration
			default:
				return imageMultipartInput{}, errors.New("only image and mask fields may contain uploads")
			}
			continue
		}
		value, readErr := io.ReadAll(io.LimitReader(part, (1<<20)+1))
		if readErr != nil || len(value) > 1<<20 {
			return imageMultipartInput{}, errors.New("multipart field exceeds 1 MiB")
		}
		text := strings.TrimSpace(string(value))
		switch name {
		case "model", "prompt", "stream", "n", "output_compression", "partial_images":
			fields[name]++
			if fields[name] > 1 {
				return imageMultipartInput{}, fmt.Errorf("%s must be provided at most once as text", name)
			}
		}
		switch name {
		case "model":
			model = text
		case "prompt":
			prompt = text
		case "stream":
			if text != "" && text != "null" {
				parsed, parseErr := strconv.ParseBool(text)
				if parseErr != nil {
					return imageMultipartInput{}, errors.New("stream must be a boolean")
				}
				if variation && parsed {
					return imageMultipartInput{}, errors.New("streaming image variations are not supported")
				}
				stream = parsed
			}
		case "n", "output_compression", "partial_images":
			if text == "" || text == "null" {
				continue
			}
			value, parseErr := strconv.ParseInt(text, 10, 64)
			if parseErr != nil {
				return imageMultipartInput{}, fmt.Errorf("%s must be an integer", name)
			}
			switch name {
			case "n":
				if value < 1 || value > 10 {
					return imageMultipartInput{}, errors.New("n must be an integer between 1 and 10")
				}
				n = value
			case "output_compression":
				if value < 0 || value > 100 {
					return imageMultipartInput{}, errors.New("output_compression must be an integer between 0 and 100")
				}
			case "partial_images":
				if value < 0 || value > 3 {
					return imageMultipartInput{}, errors.New("partial_images must be an integer between 0 and 3")
				}
				partialImages = value
				partialImagesSet = true
			}
		}
	}
	if fields["model"] != 1 || model == "" {
		return imageMultipartInput{}, errors.New("model must be provided once as text")
	}
	if variation {
		if images != 1 {
			return imageMultipartInput{}, errors.New("exactly one image is required for a variation")
		}
		if partialImagesSet {
			return imageMultipartInput{}, errors.New("partial_images is not supported for image variations")
		}
		return imageMultipartInput{model: model, n: n}, nil
	}
	if images < 1 || images > 16 {
		return imageMultipartInput{}, errors.New("image edits require 1-16 images")
	}
	if fields["prompt"] != 1 || prompt == "" || len([]rune(prompt)) > 32_000 {
		return imageMultipartInput{}, errors.New("prompt must contain 1-32000 characters")
	}
	if masks == 1 && (mask.Width != firstImage.Width || mask.Height != firstImage.Height) {
		return imageMultipartInput{}, errors.New("mask dimensions must match the first image")
	}
	if partialImagesSet && !stream {
		return imageMultipartInput{}, errors.New("partial_images requires streaming image edits")
	}
	if stream && n != 1 {
		return imageMultipartInput{}, errors.New("streaming image edits require n=1")
	}
	return imageMultipartInput{model: model, n: n, stream: stream, partialImages: partialImages}, nil
}

func readImageConfig(reader io.Reader, limit int64) (image.Config, string, error) {
	limited := &io.LimitedReader{R: reader, N: limit}
	configuration, format, err := image.DecodeConfig(limited)
	if err == nil {
		_, err = io.Copy(io.Discard, limited)
	}
	if err != nil || limited.N == 0 || configuration.Width < 1 || configuration.Height < 1 || format != "png" && format != "jpeg" && format != "webp" {
		return image.Config{}, "", errors.New("image is invalid or exceeds its size limit")
	}
	return configuration, format, nil
}
