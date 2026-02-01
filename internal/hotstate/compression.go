package hotstate

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"

	"github.com/howk/howk/internal/domain"
)

// compressWebhook compresses a webhook payload using gzip
func compressWebhook(webhook *domain.Webhook) ([]byte, error) {
	data, err := json.Marshal(webhook)
	if err != nil {
		return nil, fmt.Errorf("marshal webhook: %w", err)
	}

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write(data); err != nil {
		gz.Close()
		return nil, fmt.Errorf("compress webhook: %w", err)
	}
	if err := gz.Close(); err != nil {
		return nil, fmt.Errorf("close gzip writer: %w", err)
	}

	return buf.Bytes(), nil
}

// decompressWebhook decompresses a webhook payload
func decompressWebhook(data []byte) (*domain.Webhook, error) {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("create gzip reader: %w", err)
	}
	defer gz.Close()

	jsonData, err := io.ReadAll(gz)
	if err != nil {
		return nil, fmt.Errorf("read gzip data: %w", err)
	}

	var webhook domain.Webhook
	if err := json.Unmarshal(jsonData, &webhook); err != nil {
		return nil, fmt.Errorf("unmarshal webhook: %w", err)
	}

	return &webhook, nil
}
