package usage

import (
	"math"
	"testing"
)

func TestMoneyAndTokenCostStayExact(t *testing.T) {
	for value, want := range map[string]int64{"0": 0, "1": 1_000_000_000, "12.345678901": 12_345_678_901} {
		got, err := ParseUSD(value)
		if err != nil || got != want || FormatUSD(got) != value {
			t.Fatalf("money %q = %d (%s), %v", value, got, FormatUSD(got), err)
		}
	}
	if _, err := ParseUSD("0.0000000001"); err == nil {
		t.Fatal("accepted sub-nano amount")
	}
	cost, err := CalculateCost(1, 1, 1, 1)
	if err != nil || cost != 2 {
		t.Fatalf("rounded cost = %d, %v", cost, err)
	}
}

func TestFormatUSDMinInt64(t *testing.T) {
	if got := FormatUSD(math.MinInt64); got != "-9223372036.854775808" {
		t.Fatalf("FormatUSD(math.MinInt64) = %q", got)
	}
}
