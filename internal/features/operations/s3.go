package operations

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

var s3Client = &http.Client{Timeout: 30 * time.Minute}

func uploadS3(ctx context.Context, filename, archiveName string, settings Settings, getenv func(string) string) error {
	endpoint, err := url.Parse(settings.S3Endpoint)
	if err != nil || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || (endpoint.Scheme != "https" && !loopbackHTTP(endpoint)) {
		return errors.New("S3 endpoint must use HTTPS, except for a loopback test endpoint")
	}
	accessKey, secretKey := strings.TrimSpace(getenv(settings.S3AccessKeyEnv)), strings.TrimSpace(getenv(settings.S3SecretKeyEnv))
	if accessKey == "" || secretKey == "" {
		return errors.New("S3 credential environment variables are not configured")
	}
	payloadHash, payloadSize, err := hashArchive(ctx, filename)
	if err != nil {
		return err
	}
	// S3 keys are not filesystem paths: repeated slashes and dot segments matter.
	objectPath := strings.TrimRight(endpoint.Path, "/") + "/" + settings.S3Bucket + "/"
	if settings.S3Prefix != "" {
		objectPath += settings.S3Prefix + "/"
	}
	endpoint.Path = objectPath + archiveName
	// SigV4 permits only unreserved bytes and slashes, unlike URL.EscapedPath.
	endpoint.RawPath = strings.ReplaceAll(strings.ReplaceAll(url.QueryEscape(endpoint.Path), "+", "%20"), "%2F", "/")
	rawHash, _ := hex.DecodeString(payloadHash)
	checksum := base64.StdEncoding.EncodeToString(rawHash)
	signedHeaders := "host;x-amz-checksum-sha256;x-amz-content-sha256;x-amz-date"

	for attempt := 0; attempt < 3; attempt++ {
		now := time.Now().UTC()
		date, timestamp := now.Format("20060102"), now.Format("20060102T150405Z")
		canonicalHeaders := "host:" + endpoint.Host + "\n" + "x-amz-checksum-sha256:" + checksum + "\n" + "x-amz-content-sha256:" + payloadHash + "\n" + "x-amz-date:" + timestamp + "\n"
		canonicalRequest := "PUT\n" + endpoint.EscapedPath() + "\n\n" + canonicalHeaders + "\n" + signedHeaders + "\n" + payloadHash
		scope := date + "/" + settings.S3Region + "/s3/aws4_request"
		requestHash := sha256.Sum256([]byte(canonicalRequest))
		stringToSign := "AWS4-HMAC-SHA256\n" + timestamp + "\n" + scope + "\n" + hex.EncodeToString(requestHash[:])
		signingKey := hmacSum(hmacSum(hmacSum(hmacSum([]byte("AWS4"+secretKey), date), settings.S3Region), "s3"), "aws4_request")
		signature := hex.EncodeToString(hmacSum(signingKey, stringToSign))
		file, err := os.Open(filename)
		if err != nil {
			return err
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint.String(), file)
		if err != nil {
			file.Close()
			return err
		}
		request.Header.Set("x-amz-checksum-sha256", checksum)
		request.Header.Set("x-amz-content-sha256", payloadHash)
		request.Header.Set("x-amz-date", timestamp)
		request.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+accessKey+"/"+scope+", SignedHeaders="+signedHeaders+", Signature="+signature)
		request.ContentLength = payloadSize
		response, err := s3Client.Do(request)
		file.Close()
		if err == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
			response.Body.Close()
			if response.StatusCode >= 200 && response.StatusCode < 300 {
				return nil
			}
			if response.StatusCode < 500 && response.StatusCode != http.StatusTooManyRequests {
				return fmt.Errorf("S3 upload returned HTTP %d", response.StatusCode)
			}
		}
		if attempt < 2 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt+1) * 250 * time.Millisecond):
			}
		}
	}
	return errors.New("S3 upload failed after three attempts")
}

func loopbackHTTP(value *url.URL) bool {
	if value.Scheme != "http" {
		return false
	}
	host := value.Hostname()
	return host == "localhost" || net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback()
}

func hashArchive(ctx context.Context, filename string) (string, int64, error) {
	file, err := os.Open(filename)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	hash := sha256.New()
	size, err := io.Copy(hash, &contextReader{ctx: ctx, reader: file})
	return hex.EncodeToString(hash.Sum(nil)), size, err
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (reader *contextReader) Read(buffer []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	return reader.reader.Read(buffer)
}

func hmacSum(key []byte, value string) []byte {
	hash := hmac.New(sha256.New, key)
	_, _ = hash.Write([]byte(value))
	return hash.Sum(nil)
}
