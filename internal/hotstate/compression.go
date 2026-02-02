package hotstate

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/howk/howk/internal/domain"
	"github.com/klauspost/compress/zstd"
)

// compressWebhook compresses a webhook payload using zstd
func compressWebhook(webhook *domain.Webhook) ([]byte, error) {
	data, err := json.Marshal(webhook)
	if err != nil {
		return nil, fmt.Errorf("marshal webhook: %w", err)
	}

	var buf bytes.Buffer
	enc, err := zstd.NewWriter(&buf)
	if err != nil {
		return nil, fmt.Errorf("create zstd writer: %w", err)
	}
	defer enc.Close()

	if _, err := enc.Write(data); err != nil {
		return nil, fmt.Errorf("compress webhook: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("close zstd writer: %w", err)
	}

	return buf.Bytes(), nil
}

// decompressWebhook decompresses a webhook payload
func decompressWebhook(data []byte) (*domain.Webhook, error) {
	dec, err := zstd.NewReader(nil)
	if err != nil {
		return nil, fmt.Errorf("create zstd reader: %w", err)
	}
	defer dec.Close()

	jsonData, err := dec.DecodeAll(data, nil)
	if err != nil {
		return nil, fmt.Errorf("read zstd data: %w", err)
	}

	var webhook domain.Webhook
	if err := json.Unmarshal(jsonData, &webhook); err != nil {
		return nil, fmt.Errorf("unmarshal webhook: %w", err)
	}

	return &webhook, nil
}
