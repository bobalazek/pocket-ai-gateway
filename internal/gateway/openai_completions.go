package gateway

import (
	"errors"
	"math"

	"github.com/bobalazek/pocket-ai-gateway/internal/protocol"
)

func completionOutputReservation(maxTokens int64, request protocol.OpenAICompletionRequest) (int64, bool, error) {
	bounded := maxTokens > 0
	if !bounded {
		maxTokens = 4096
	}
	multiplier := request.PromptCount * request.Candidates
	if multiplier <= 0 || maxTokens > math.MaxInt64/multiplier {
		return 0, false, errors.New("completion output reservation exceeds the supported range")
	}
	return maxTokens * multiplier, bounded, nil
}
