package providers

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const maxProviderCredentialBytes = 16_384

func resolveExternalCredential(reference string) (string, bool, error) {
	source, value, bearer, ok := parseExternalCredentialRef(reference)
	if !ok {
		return "", false, errors.New("external provider credential reference is invalid")
	}
	var credential string
	if source == "env" {
		credential = os.Getenv(value)
	} else {
		info, err := os.Stat(value)
		if err != nil || !info.Mode().IsRegular() || info.Size() > maxProviderCredentialBytes {
			return "", false, errors.New("external provider credential file is invalid")
		}
		file, err := os.Open(value)
		if err != nil {
			return "", false, errors.New("external provider credential is unavailable")
		}
		defer file.Close()
		info, err = file.Stat()
		if err != nil || !info.Mode().IsRegular() || info.Size() > maxProviderCredentialBytes {
			return "", false, errors.New("external provider credential file is invalid")
		}
		raw, err := io.ReadAll(io.LimitReader(file, maxProviderCredentialBytes+1))
		if err != nil || len(raw) > maxProviderCredentialBytes {
			return "", false, errors.New("external provider credential file is invalid")
		}
		credential = string(raw)
	}
	credential = strings.TrimSpace(credential)
	if credential == "" || len(credential) > maxProviderCredentialBytes || strings.ContainsAny(credential, "\r\n") {
		return "", false, errors.New("external provider credential is unavailable")
	}
	return credential, bearer, nil
}

func validExternalRef(reference string) bool {
	_, _, _, ok := parseExternalCredentialRef(reference)
	return ok
}

func parseExternalCredentialRef(reference string) (source, value string, bearer, ok bool) {
	if len(reference) > 1_024 {
		return "", "", false, false
	}
	switch {
	case strings.HasPrefix(reference, "bearer-env:"):
		source, value, bearer = "env", strings.TrimPrefix(reference, "bearer-env:"), true
	case strings.HasPrefix(reference, "bearer-file:"):
		source, value, bearer = "file", strings.TrimPrefix(reference, "bearer-file:"), true
	case strings.HasPrefix(reference, "env:"):
		source, value = "env", strings.TrimPrefix(reference, "env:")
	case strings.HasPrefix(reference, "file:"):
		source, value = "file", strings.TrimPrefix(reference, "file:")
	default:
		return "", "", false, false
	}
	if source == "file" {
		return source, value, bearer, filepath.IsAbs(value) && filepath.Clean(value) == value
	}
	if value == "" || len(value) > 200 {
		return "", "", false, false
	}
	for index, character := range value {
		if character >= 'A' && character <= 'Z' || character >= 'a' && character <= 'z' || character == '_' || index > 0 && character >= '0' && character <= '9' {
			continue
		}
		return "", "", false, false
	}
	return source, value, bearer, true
}
