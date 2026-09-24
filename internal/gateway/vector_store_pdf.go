package gateway

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/giraffesyo/pdf"
)

const maxVectorStorePDFPages = 256

func vectorStorePDFText(ctx context.Context, content []byte) ([]byte, error) {
	if len(content) > maxFileBytes || !vectorStorePDFHeader(content) {
		return nil, errors.New("PDF content is invalid or exceeds 16 MiB")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	var text strings.Builder
	_, err := pdf.ExtractPages(ctx, bytes.NewReader(content), int64(len(content)), pdf.Options{
		Strict:                      true,
		Concurrency:                 1,
		IgnoreAnnotationAppearances: true,
		Limits: pdf.Limits{
			MaxStreamBytes:      maxVectorStoreParsedContentBytes,
			MaxOperatorsPerPage: 500_000,
			MaxGlyphsPerPage:    100_000,
			MaxFormDepth:        8,
			MaxImagesPerPage:    1_000,
		},
	}, func(page pdf.Page) error {
		if page.Number > maxVectorStorePDFPages {
			return errors.New("PDF exceeds 256 pages")
		}
		pageText := page.Text()
		if pageText == "" {
			return nil
		}
		separator := 0
		if text.Len() > 0 {
			separator = 2
		}
		if len(pageText)+separator > maxVectorStoreParsedContentBytes-text.Len() {
			return errors.New("PDF extracted text exceeds 16 MiB")
		}
		if separator > 0 {
			text.WriteString("\n\n")
		}
		text.WriteString(pageText)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("PDF text extraction failed: %w", err)
	}
	if text.Len() == 0 {
		return nil, errors.New("PDF has no selectable text; OCR is not supported")
	}
	return []byte(text.String()), nil
}
