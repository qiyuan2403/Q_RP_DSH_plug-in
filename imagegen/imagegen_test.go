package imagegen

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// pngBytes is a minimal PNG payload used to prove format sniffing.
func pngBytes() []byte {
	return []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0x00, 0x01}
}

// TestGeneratePostsAndDecodes pins the happy path: the request carries the
// prompt, and a base64 response decodes to image bytes.
func TestGeneratePostsAndDecodes(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sdapi/v1/txt2img" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		resp := map[string]any{
			"images": []string{base64.StdEncoding.EncodeToString(pngBytes())},
			"info":   `{"seed":42}`,
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := New(Config{Endpoint: srv.URL})
	results, err := c.Generate(context.Background(), Request{Prompt: "a cat", Steps: 20, Width: 512})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if gotBody["prompt"] != "a cat" {
		t.Fatalf("prompt on the wire = %v", gotBody["prompt"])
	}
	if gotBody["steps"] != float64(20) {
		t.Fatalf("steps on the wire = %v", gotBody["steps"])
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if results[0].Format != ".png" {
		t.Fatalf("Format = %q, want .png", results[0].Format)
	}
	if string(results[0].Data) != string(pngBytes()) {
		t.Fatal("decoded bytes do not match")
	}
	if results[0].Info["seed"] != float64(42) {
		t.Fatalf("info = %v", results[0].Info)
	}
}

// TestGenerateAcceptsDataURL pins that a data-URL payload is unwrapped, since
// backends differ on which form they emit.
func TestGenerateAcceptsDataURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"images": []string{"data:image/png;base64," + base64.StdEncoding.EncodeToString(pngBytes())},
		})
	}))
	defer srv.Close()

	c := New(Config{Endpoint: srv.URL})
	results, err := c.Generate(context.Background(), Request{Prompt: "x"})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if results[0].Format != ".png" {
		t.Fatalf("Format = %q", results[0].Format)
	}
}

// TestGenerateSniffsFormatFromMagicBytes pins that the format comes from the
// content, not from a declared type that may be wrong.
func TestGenerateSniffsFormatFromMagicBytes(t *testing.T) {
	jpeg := []byte{0xff, 0xd8, 0xff, 0xe0, 0x00}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			// A declared PNG data URL carrying JPEG bytes.
			"images": []string{"data:image/png;base64," + base64.StdEncoding.EncodeToString(jpeg)},
		})
	}))
	defer srv.Close()

	c := New(Config{Endpoint: srv.URL})
	results, err := c.Generate(context.Background(), Request{Prompt: "x"})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if results[0].Format != ".jpg" {
		t.Fatalf("Format = %q, want .jpg from the magic bytes", results[0].Format)
	}
}

// TestGenerateSurfacesBackendDetail pins that a non-200 body reaches the
// caller: a diffusion server explains the refusal there, and dropping it makes
// the failure unactionable.
func TestGenerateSurfacesBackendDetail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"detail":"model not found: anime-v3"}`))
	}))
	defer srv.Close()

	c := New(Config{Endpoint: srv.URL})
	_, err := c.Generate(context.Background(), Request{Prompt: "x"})
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "anime-v3") {
		t.Fatalf("error dropped the backend detail: %v", err)
	}
}

// TestGenerateRequiresEndpointAndPrompt pins fail-loud on both preconditions.
func TestGenerateRequiresEndpointAndPrompt(t *testing.T) {
	c := New(Config{})
	if _, err := c.Generate(context.Background(), Request{Prompt: "x"}); !errors.Is(err, ErrNoBackend) {
		t.Fatalf("error = %v, want ErrNoBackend", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	c2 := New(Config{Endpoint: srv.URL})
	if _, err := c2.Generate(context.Background(), Request{Prompt: "   "}); err == nil {
		t.Fatal("an empty prompt was accepted")
	}
}

// TestGenerateRejectsEmptyImageList pins that a 200 with no images is an error
// rather than a silent empty result.
func TestGenerateRejectsEmptyImageList(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"images": []string{}})
	}))
	defer srv.Close()
	c := New(Config{Endpoint: srv.URL})
	if _, err := c.Generate(context.Background(), Request{Prompt: "x"}); err == nil {
		t.Fatal("an empty image list was accepted")
	}
}

// TestGenerateSendsBearerWhenConfigured pins the auth header.
func TestGenerateSendsBearerWhenConfigured(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"images": []string{base64.StdEncoding.EncodeToString(pngBytes())},
		})
	}))
	defer srv.Close()
	c := New(Config{Endpoint: srv.URL, APIKey: "secret"})
	if _, err := c.Generate(context.Background(), Request{Prompt: "x"}); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if gotAuth != "Bearer secret" {
		t.Fatalf("Authorization = %q", gotAuth)
	}
}

// TestGenerateRespectsContextCancellation pins that a cancelled context aborts
// the request rather than waiting out the timeout.
func TestGenerateRespectsContextCancellation(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	defer func() { close(release); srv.Close() }()

	c := New(Config{Endpoint: srv.URL, Timeout: 30 * time.Second})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Generate(ctx, Request{Prompt: "x"}); err == nil {
		t.Fatal("a cancelled context did not abort the request")
	}
}

// TestSniffFormatCoversTheKnownTypes pins the format table directly.
func TestSniffFormatCoversTheKnownTypes(t *testing.T) {
	cases := map[string][]byte{
		".png":  {0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'},
		".jpg":  {0xff, 0xd8, 0xff, 0xe0},
		".webp": append([]byte("RIFF\x00\x00\x00\x00"), []byte("WEBP")...),
		".gif":  []byte("GIF89a"),
		".bin":  {0x00, 0x01, 0x02},
	}
	for want, data := range cases {
		if got := sniffFormat(data); got != want {
			t.Errorf("sniffFormat(%v) = %q, want %q", data[:4], got, want)
		}
	}
}
