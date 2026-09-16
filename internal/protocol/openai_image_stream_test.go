package protocol

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestCopyOpenAIImageGenerationStream(t *testing.T) {
	source := imageStreamEvent("image_generation.partial_image", `{"type":"image_generation.partial_image","b64_json":"cGFydGlhbA==","background":"auto","created_at":1,"output_format":"png","partial_image_index":0,"quality":"high","size":"1024x1024"}`) +
		imageStreamEvent("image_generation.completed", `{"type":"image_generation.completed","b64_json":"ZmluYWw=","background":"opaque","created_at":2,"output_format":"webp","quality":"medium","size":"1024x1024","usage":{"input_tokens":3,"input_tokens_details":{"image_tokens":1,"text_tokens":2},"output_tokens":4,"total_tokens":7}}`)
	var output bytes.Buffer
	usage, err := CopyOpenAIImageGenerationStream(&output, strings.NewReader(source), 1)
	if err != nil {
		t.Fatal(err)
	}
	if output.String() != source {
		t.Fatalf("forwarded stream=%q", output.String())
	}
	if string(usage) != `{"usage":{"input_tokens":3,"input_tokens_details":{"image_tokens":1,"text_tokens":2},"output_tokens":4,"total_tokens":7}}` {
		t.Fatalf("usage=%s", usage)
	}
	if bytes.Contains(usage, []byte("final")) {
		t.Fatal("terminal image data was retained in usage metadata")
	}
}

func TestCopyOpenAIImageGenerationStreamAcceptsCompletedOnly(t *testing.T) {
	source := imageStreamEvent("image_generation.completed", `{"type":"image_generation.completed","b64_json":"ZmluYWw=","background":"transparent","created_at":2,"output_format":"jpeg","quality":"auto","size":"1536x1024","usage":{"input_tokens":0,"input_tokens_details":{"image_tokens":0,"text_tokens":0},"output_tokens":0,"total_tokens":0}}`)
	if _, err := CopyOpenAIImageGenerationStream(&bytes.Buffer{}, strings.NewReader(source), 0); err != nil {
		t.Fatal(err)
	}
}

func TestCopyOpenAIImageGenerationStreamRejectsInvalidContract(t *testing.T) {
	partial := `{"type":"image_generation.partial_image","b64_json":"cGFydGlhbA==","background":"auto","created_at":1,"output_format":"png","partial_image_index":0,"quality":"high","size":"1024x1024"}`
	completed := `{"type":"image_generation.completed","b64_json":"ZmluYWw=","background":"opaque","created_at":2,"output_format":"webp","quality":"medium","size":"1024x1024","usage":{"input_tokens":3,"input_tokens_details":{"image_tokens":1,"text_tokens":2},"output_tokens":4,"total_tokens":7}}`
	tests := map[string]string{
		"done sentinel":         "event: image_generation.completed\ndata: [DONE]\n\n",
		"unnamed event":         "data: " + completed + "\n\n",
		"mismatched type":       imageStreamEvent("image_generation.partial_image", strings.Replace(partial, "image_generation.partial_image", "image_generation.completed", 1)),
		"partial not requested": imageStreamEvent("image_generation.partial_image", partial) + imageStreamEvent("image_generation.completed", completed),
		"invalid field type":    imageStreamEvent("image_generation.completed", strings.Replace(completed, `"created_at":2`, `"created_at":"2"`, 1)),
		"null required field":   imageStreamEvent("image_generation.completed", strings.Replace(completed, `"b64_json":"ZmluYWw="`, `"b64_json":null`, 1)),
		"missing usage":         imageStreamEvent("image_generation.completed", strings.Replace(completed, `,"usage":{`, `,"missing":{`, 1)),
		"invalid usage field":   imageStreamEvent("image_generation.completed", strings.Replace(completed, `"output_tokens":4`, `"output_tokens":-1`, 1)),
		"inconsistent total":    imageStreamEvent("image_generation.completed", strings.Replace(completed, `"total_tokens":7`, `"total_tokens":8`, 1)),
		"inconsistent details":  imageStreamEvent("image_generation.completed", strings.Replace(completed, `"image_tokens":1`, `"image_tokens":2`, 1)),
		"empty image":           imageStreamEvent("image_generation.completed", strings.Replace(completed, `"b64_json":"ZmluYWw="`, `"b64_json":""`, 1)),
		"invalid base64":        imageStreamEvent("image_generation.completed", strings.Replace(completed, `"b64_json":"ZmluYWw="`, `"b64_json":"not-base64"`, 1)),
		"missing terminal":      imageStreamEvent("image_generation.partial_image", partial),
		"event after terminal":  imageStreamEvent("image_generation.completed", completed) + imageStreamEvent("image_generation.completed", completed),
		"truncated event":       "event: image_generation.completed\ndata: " + completed,
	}
	for name, source := range tests {
		t.Run(name, func(t *testing.T) {
			partialImages := 1
			if name == "partial not requested" {
				partialImages = 0
			}
			if _, err := CopyOpenAIImageGenerationStream(&bytes.Buffer{}, strings.NewReader(source), partialImages); !errors.Is(err, ErrInvalidOpenAIImageStream) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestCopyOpenAIImageGenerationStreamBoundsPartialIndexAndEventSize(t *testing.T) {
	partial := `{"type":"image_generation.partial_image","b64_json":"cGFydGlhbA==","background":"auto","created_at":1,"output_format":"png","partial_image_index":1,"quality":"high","size":"1024x1024"}`
	if _, err := CopyOpenAIImageGenerationStream(&bytes.Buffer{}, strings.NewReader(imageStreamEvent("image_generation.partial_image", partial)), 1); !errors.Is(err, ErrInvalidOpenAIImageStream) {
		t.Fatalf("partial index error=%v", err)
	}
	duplicate := imageStreamEvent("image_generation.partial_image", strings.Replace(partial, `"partial_image_index":1`, `"partial_image_index":0`, 1))
	if _, err := CopyOpenAIImageGenerationStream(&bytes.Buffer{}, strings.NewReader(duplicate+duplicate), 1); !errors.Is(err, ErrInvalidOpenAIImageStream) {
		t.Fatalf("duplicate partial error=%v", err)
	}
	oversized := "event: image_generation.completed\ndata: " + strings.Repeat("x", maxOpenAIImageStreamEventBytes) + "\n\n"
	if _, err := CopyOpenAIImageGenerationStream(&bytes.Buffer{}, strings.NewReader(oversized), 0); !errors.Is(err, ErrInvalidOpenAIImageStream) {
		t.Fatalf("oversized event error=%v", err)
	}
}

func imageStreamEvent(name, data string) string {
	return "event: " + name + "\ndata: " + data + "\n\n"
}
