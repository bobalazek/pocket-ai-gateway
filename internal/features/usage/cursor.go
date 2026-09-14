package usage

import (
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
)

func encodeCursor(at int64, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(at, 10) + ":" + id))
}

func decodeCursor(value string) (int64, string, error) {
	if value == "" {
		return 0, "", nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return 0, "", errors.New("invalid cursor")
	}
	timePart, id, ok := strings.Cut(string(raw), ":")
	at, err := strconv.ParseInt(timePart, 10, 64)
	if err != nil || !ok || id == "" {
		return 0, "", errors.New("invalid cursor")
	}
	return at, id, nil
}
