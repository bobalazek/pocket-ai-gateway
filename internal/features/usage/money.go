package usage

import (
	"errors"
	"math"
	"math/bits"
	"strconv"
	"strings"
)

const nanosPerUSD int64 = 1_000_000_000

func ParseUSD(value string) (int64, error) {
	value = strings.TrimSpace(value)
	if value == "" || strings.HasPrefix(value, "-") || strings.HasPrefix(value, "+") {
		return 0, errors.New("amount must be a non-negative USD decimal")
	}
	parts := strings.Split(value, ".")
	if len(parts) > 2 || parts[0] == "" || (len(parts) == 2 && parts[1] == "") {
		return 0, errors.New("amount must be a non-negative USD decimal")
	}
	whole, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || whole > math.MaxInt64/nanosPerUSD {
		return 0, errors.New("amount is too large")
	}
	fraction := ""
	if len(parts) == 2 {
		fraction = parts[1]
	}
	if len(fraction) > 9 {
		return 0, errors.New("amount supports at most 9 decimal places")
	}
	for _, digit := range fraction {
		if digit < '0' || digit > '9' {
			return 0, errors.New("amount must be a non-negative USD decimal")
		}
	}
	fraction += strings.Repeat("0", 9-len(fraction))
	fractionNanos := int64(0)
	if fraction != "" {
		fractionNanos, _ = strconv.ParseInt(fraction, 10, 64)
	}
	if whole > (math.MaxInt64-fractionNanos)/nanosPerUSD {
		return 0, errors.New("amount is too large")
	}
	return whole*nanosPerUSD + fractionNanos, nil
}

func parseSignedUSD(value string) (int64, error) {
	value = strings.TrimSpace(value)
	negative := strings.HasPrefix(value, "-")
	if negative {
		value = strings.TrimPrefix(value, "-")
	}
	amount, err := ParseUSD(value)
	if err != nil {
		return 0, err
	}
	if negative {
		return -amount, nil
	}
	return amount, nil
}

func FormatUSD(nanos int64) string {
	if nanos < 0 {
		magnitude := uint64(-(nanos + 1)) + 1
		return "-" + formatUSDUnsigned(magnitude)
	}
	return formatUSDUnsigned(uint64(nanos))
}

func formatUSDUnsigned(nanos uint64) string {
	whole, fraction := nanos/uint64(nanosPerUSD), nanos%uint64(nanosPerUSD)
	if fraction == 0 {
		return strconv.FormatUint(whole, 10)
	}
	return strconv.FormatUint(whole, 10) + "." + strings.TrimRight(strconv.FormatUint(fraction+uint64(nanosPerUSD), 10)[1:], "0")
}

func tokenCost(tokens, rateNanosPerMillion int64) (int64, error) {
	if tokens < 0 || rateNanosPerMillion < 0 {
		return 0, errors.New("token counts and prices must be non-negative")
	}
	hi, lo := bits.Mul64(uint64(tokens), uint64(rateNanosPerMillion))
	if hi >= 1_000_000 {
		return 0, errors.New("calculated cost is too large")
	}
	quotient, remainder := bits.Div64(hi, lo, 1_000_000)
	if remainder != 0 {
		quotient++
	}
	if quotient > math.MaxInt64 {
		return 0, errors.New("calculated cost is too large")
	}
	return int64(quotient), nil
}

func CalculateCost(inputTokens, outputTokens, inputRate, outputRate int64) (int64, error) {
	inputCost, err := tokenCost(inputTokens, inputRate)
	if err != nil {
		return 0, err
	}
	outputCost, err := tokenCost(outputTokens, outputRate)
	if err != nil || inputCost > math.MaxInt64-outputCost {
		return 0, errors.New("calculated cost is too large")
	}
	return inputCost + outputCost, nil
}
