// Package imagegen talks to an external image-generation backend.
//
// The backend is NOT built in. Image generation means a diffusion model, its
// weights, and its hardware; embedding any of that would tie the harness to one
// runtime. This package therefore speaks a small HTTP contract that several
// servers already implement (the AUTOMATIC1111 and Forge `/sdapi/v1/txt2img`
// shape), so the user points it at whatever they run.
//
// The returned images are written into a gallery; nothing here holds model
// state.
package imagegen

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// ErrNoBackend reports a call made without a configured endpoint.
var ErrNoBackend = errors.New("imagegen: no backend endpoint configured")

// Config describes one backend.
type Config struct {
	// Endpoint is the server's base URL, e.g. http://127.0.0.1:7860.
	Endpoint string
	// Path overrides the generation route. Empty uses the txt2img default.
	Path string
	// APIKey, when set, is sent as a Bearer token.
	APIKey string
	// Timeout bounds one generation. Diffusion is slow, so the default is
	// generous rather than tight.
	Timeout time.Duration
	// HTTPClient overrides the client. Nil builds one from Timeout.
	HTTPClient *http.Client
}

// Request is one generation request.
type Request struct {
	Prompt   string
	Negative string
	Width    int
	Height   int
	Steps    int
	CFGScale float64
	Seed     int64
	// Sampler is the backend's sampler name. Empty omits the field, letting the
	// backend apply its own default rather than guessing one.
	Sampler string
	// BatchSize is how many images to produce.
	BatchSize int
}

// Result is one generated image.
type Result struct {
	// Data is the decoded image bytes.
	Data []byte
	// Format is a file extension including the dot, e.g. ".png".
	Format string
	// Info is the backend's raw metadata for the image, when it reports any.
	Info map[string]any
}

// Client is an image-generation backend client.
type Client struct {
	cfg Config
}

// New builds a client. An empty endpoint is allowed so the caller can construct
// a client before the user configures one; calls then fail with ErrNoBackend.
func New(cfg Config) *Client {
	if cfg.Path == "" {
		cfg.Path = "/sdapi/v1/txt2img"
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 5 * time.Minute
	}
	return &Client{cfg: cfg}
}

// wireRequest is the txt2img payload. Field names are the wire vocabulary and
// must not change.
type wireRequest struct {
	Prompt         string  `json:"prompt"`
	NegativePrompt string  `json:"negative_prompt,omitempty"`
	Width          int     `json:"width,omitempty"`
	Height         int     `json:"height,omitempty"`
	Steps          int     `json:"steps,omitempty"`
	CFGScale       float64 `json:"cfg_scale,omitempty"`
	Seed           int64   `json:"seed,omitempty"`
	SamplerName    string  `json:"sampler_name,omitempty"`
	BatchSize      int     `json:"batch_size,omitempty"`
}

type wireResponse struct {
	Images []string `json:"images"`
	Info   string   `json:"info"`
	Detail any      `json:"detail"`
	raw    map[string]any
}

// Generate runs one request and returns the decoded images.
//
// A backend error body is surfaced rather than reduced to a status code: a
// diffusion server reports why it refused (a missing model, an out-of-memory
// condition) in that body, and losing it makes the failure unactionable.
func (c *Client) Generate(ctx context.Context, req Request) ([]Result, error) {
	if strings.TrimSpace(c.cfg.Endpoint) == "" {
		return nil, ErrNoBackend
	}
	if strings.TrimSpace(req.Prompt) == "" {
		return nil, errors.New("imagegen: prompt is empty")
	}

	body, err := json.Marshal(wireRequest{
		Prompt:         req.Prompt,
		NegativePrompt: req.Negative,
		Width:          req.Width,
		Height:         req.Height,
		Steps:          req.Steps,
		CFGScale:       req.CFGScale,
		Seed:           req.Seed,
		SamplerName:    req.Sampler,
		BatchSize:      req.BatchSize,
	})
	if err != nil {
		return nil, err
	}

	url := strings.TrimRight(c.cfg.Endpoint, "/") + c.cfg.Path
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.cfg.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	}

	client := c.cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: c.cfg.Timeout}
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("imagegen: request to %s failed: %w", url, err)
	}
	defer resp.Body.Close()

	var buf bytes.Buffer
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		return nil, fmt.Errorf("imagegen: reading response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		detail := strings.TrimSpace(buf.String())
		if len(detail) > 512 {
			detail = detail[:512] + "..."
		}
		return nil, fmt.Errorf("imagegen: backend returned %d: %s", resp.StatusCode, detail)
	}

	var parsed wireResponse
	if err := json.Unmarshal(buf.Bytes(), &parsed); err != nil {
		return nil, fmt.Errorf("imagegen: response is not valid JSON: %w", err)
	}
	if len(parsed.Images) == 0 {
		return nil, errors.New("imagegen: backend returned no images")
	}

	info := map[string]any{}
	if parsed.Info != "" {
		_ = json.Unmarshal([]byte(parsed.Info), &info)
	}

	out := make([]Result, 0, len(parsed.Images))
	for i, encoded := range parsed.Images {
		data, format, err := decodeImage(encoded)
		if err != nil {
			return nil, fmt.Errorf("imagegen: image %d: %w", i, err)
		}
		out = append(out, Result{Data: data, Format: format, Info: info})
	}
	return out, nil
}

// decodeImage accepts either a bare base64 payload or a data URL, and sniffs
// the format from the leading bytes rather than trusting a declared type.
func decodeImage(encoded string) ([]byte, string, error) {
	payload := strings.TrimSpace(encoded)
	if strings.HasPrefix(payload, "data:") {
		if idx := strings.Index(payload, ","); idx >= 0 {
			payload = payload[idx+1:]
		}
	}
	// The backend may emit standard base64 or its URL-safe variant; try both
	// rather than failing on a padding difference.
	data, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		data, err = base64.RawStdEncoding.DecodeString(strings.TrimRight(payload, "="))
		if err != nil {
			return nil, "", fmt.Errorf("payload is not valid base64: %w", err)
		}
	}
	return data, sniffFormat(data), nil
}

// sniffFormat identifies the image type from its magic bytes.
func sniffFormat(data []byte) string {
	switch {
	case len(data) >= 8 && bytes.Equal(data[:8], []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}):
		return ".png"
	case len(data) >= 3 && data[0] == 0xff && data[1] == 0xd8 && data[2] == 0xff:
		return ".jpg"
	case len(data) >= 12 && bytes.Equal(data[:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")):
		return ".webp"
	case len(data) >= 6 && bytes.Equal(data[:6], []byte("GIF89a")):
		return ".gif"
	case len(data) >= 6 && bytes.Equal(data[:6], []byte("GIF87a")):
		return ".gif"
	default:
		return ".bin"
	}
}
