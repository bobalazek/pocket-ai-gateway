package usage

import "testing"

func TestCursorRoundTripAndRejectsMalformed(t *testing.T) {
	wantAt, wantID := int64(123), "item:with:colons"
	gotAt, gotID, err := decodeCursor(encodeCursor(wantAt, wantID))
	if err != nil || gotAt != wantAt || gotID != wantID {
		t.Fatalf("cursor round trip = (%d, %q, %v)", gotAt, gotID, err)
	}
	for _, value := range []string{"not-base64!", "bm90LWFuLWludDppZA", "MTIzOg"} {
		if _, _, err := decodeCursor(value); err == nil {
			t.Fatalf("decodeCursor(%q) accepted malformed cursor", value)
		}
	}
}
