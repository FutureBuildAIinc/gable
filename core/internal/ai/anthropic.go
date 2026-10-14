// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package ai

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

// The provider names an admin picks between in Tech Admin's AI settings.
const (
	ProviderOpenRouter = "openrouter"
	ProviderAnthropic  = "anthropic"
)

// The system_settings rows the Anthropic provider reads. The key row holds a
// sealed value (see Sealer); there is no environment fallback for it.
const (
	SettingProvider       = "ai.provider"
	SettingAnthropicKey   = "anthropic_api_key"
	SettingAnthropicModel = "ai.anthropic.model"
)

const (
	defaultAnthropicBaseURL = "https://api.anthropic.com"
	anthropicVersion        = "2023-06-01"

	// DefaultAnthropicModel is the model used when the admin has chosen none.
	DefaultAnthropicModel = "claude-sonnet-5-5"
)

// anthropicModelRE reads the family and the major and minor version of a
// Claude model id such as claude-sonnet-5-5 or claude-opus-5-5-20260101. The
// minor is one or two digits and the only suffix is an eight digit date, so
// a dated alias (claude-sonnet-5-20260101) reads as major 5, no minor.
var anthropicModelRE = regexp.MustCompile(`^claude-(sonnet|opus|fable)-(\d+)(?:-(\d{1,2}))?(?:-\d{8})?$`)

// ValidateAnthropicModel refuses any model below Sonnet 5.5: Sonnet 5.5 or
// later, Opus 5.5 or later, and the Fable family (above Opus) are allowed. An
// empty id is valid (it selects DefaultAnthropicModel). The refusal names the
// model.
func ValidateAnthropicModel(model string) error {
	model = strings.TrimSpace(model)
	if model == "" {
		return nil
	}
	refuse := fmt.Errorf("model %q is not allowed: use Sonnet 5.5 or better (Opus 5.5 and later)", model)
	m := anthropicModelRE.FindStringSubmatch(model)
	if m == nil {
		return refuse
	}
	if m[1] == "fable" {
		return nil
	}
	major, _ := strconv.Atoi(m[2])
	minor := 0
	if m[3] != "" {
		minor, _ = strconv.Atoi(m[3])
	}
	if major > 5 || (major == 5 && minor >= 5) {
		return nil
	}
	return refuse
}

// --- Messages API types ---

type anthropicRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	System    string             `json:"system,omitempty"`
	Messages  []anthropicMessage `json:"messages"`
}

type anthropicMessage struct {
	Role    string           `json:"role"`
	Content []anthropicBlock `json:"content"`
}

type anthropicBlock struct {
	Type   string           `json:"type"` // "text" | "image" | "document"
	Text   string           `json:"text,omitempty"`
	Source *anthropicSource `json:"source,omitempty"`
}

type anthropicSource struct {
	Type      string `json:"type"` // "base64"
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

type anthropicResponse struct {
	Model   string `json:"model"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func (r *anthropicResponse) text() string {
	var sb strings.Builder
	for _, b := range r.Content {
		if b.Type == "text" {
			sb.WriteString(b.Text)
		}
	}
	return sb.String()
}

// anthropicModelID resolves the model: the admin's choice, else the default.
func (c *Client) anthropicModelID(ctx context.Context) string {
	if c.anthropicModelStore != nil {
		if m := strings.TrimSpace(c.anthropicModelStore.Get(ctx)); m != "" {
			return m
		}
	}
	if m := strings.TrimSpace(c.anthropicModel); m != "" {
		return m
	}
	return DefaultAnthropicModel
}

func (c *Client) anthropicBase() string {
	if c.anthropicBaseURL != "" && ValidateBaseURL(c.anthropicBaseURL) == nil {
		return strings.TrimRight(c.anthropicBaseURL, "/")
	}
	return defaultAnthropicBaseURL
}

// anthropicMessages performs one POST {base}/v1/messages. Errors carry the
// provider's own words and never the key (a provider that echoes it back is
// scrubbed).
func (c *Client) anthropicMessages(ctx context.Context, req anthropicRequest) (*anthropicResponse, error) {
	key := c.anthropicKeyValue(ctx)
	if key == "" {
		return nil, fmt.Errorf("AI is off: no Anthropic key is set (an admin enters it in Tech Admin, AI settings)")
	}
	if err := ValidateAnthropicModel(req.Model); err != nil {
		return nil, err
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.anthropicBase()+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", key)
	httpReq.Header.Set("anthropic-version", anthropicVersion)

	scrub := func(s string) string { return strings.ReplaceAll(s, key, "[key]") }

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("Anthropic request failed: %s", scrub(err.Error()))
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}
	var ar anthropicResponse
	parsed := json.Unmarshal(respBody, &ar) == nil
	if resp.StatusCode != http.StatusOK {
		msg := string(respBody)
		if parsed && ar.Error != nil && ar.Error.Message != "" {
			msg = ar.Error.Message
		}
		return nil, fmt.Errorf("Anthropic API error (%d): %s", resp.StatusCode, scrub(msg))
	}
	if !parsed {
		return nil, fmt.Errorf("failed to parse Anthropic response")
	}
	if ar.Error != nil {
		return nil, fmt.Errorf("Anthropic error: %s", scrub(ar.Error.Message))
	}
	return &ar, nil
}

func (c *Client) anthropicGenerate(ctx context.Context, system, user string, maxTokens int) (string, string, error) {
	want := c.anthropicModelID(ctx)
	resp, err := c.anthropicMessages(ctx, anthropicRequest{
		Model:     want,
		MaxTokens: maxTokens,
		System:    system,
		Messages:  []anthropicMessage{{Role: "user", Content: []anthropicBlock{{Type: "text", Text: user}}}},
	})
	if err != nil {
		return "", "", err
	}
	model := resp.Model
	if model == "" {
		model = want
	}
	return stripJSONFence(resp.text()), model, nil
}

// anthropicFileBlocks turns an upload into Messages API blocks: image/* is an
// image block, a PDF a document block, text is inlined.
func anthropicFileBlocks(fileBytes []byte, contentType, instruction string) ([]anthropicBlock, error) {
	enc := base64.StdEncoding.EncodeToString(fileBytes)
	switch {
	case strings.HasPrefix(contentType, "image/"):
		return []anthropicBlock{
			{Type: "image", Source: &anthropicSource{Type: "base64", MediaType: contentType, Data: enc}},
			{Type: "text", Text: instruction},
		}, nil
	case contentType == "application/pdf":
		return []anthropicBlock{
			{Type: "document", Source: &anthropicSource{Type: "base64", MediaType: contentType, Data: enc}},
			{Type: "text", Text: instruction},
		}, nil
	case contentType == "text/plain" || contentType == "text/csv":
		return []anthropicBlock{{Type: "text", Text: instruction + "\n\n" + string(fileBytes)}}, nil
	default:
		return nil, fmt.Errorf("unsupported content type: %s", contentType)
	}
}

func (c *Client) anthropicFileCall(ctx context.Context, system, instruction string, fileBytes []byte, contentType string, maxTokens int) (string, error) {
	blocks, err := anthropicFileBlocks(fileBytes, contentType, instruction)
	if err != nil {
		return "", err
	}
	resp, err := c.anthropicMessages(ctx, anthropicRequest{
		Model:     c.anthropicModelID(ctx),
		MaxTokens: maxTokens,
		System:    system,
		Messages:  []anthropicMessage{{Role: "user", Content: blocks}},
	})
	if err != nil {
		return "", err
	}
	return resp.text(), nil
}

// TestConnection makes the cheapest call the active provider allows (one
// output token) and answers nil or the provider's error in words. The error
// never carries a key.
func (c *Client) TestConnection(ctx context.Context) error {
	if c.activeProvider(ctx) == ProviderAnthropic {
		_, err := c.anthropicMessages(ctx, anthropicRequest{
			Model:     c.anthropicModelID(ctx),
			MaxTokens: 1,
			Messages:  []anthropicMessage{{Role: "user", Content: []anthropicBlock{{Type: "text", Text: "ping"}}}},
		})
		return err
	}
	if c.getKey(ctx) == "" {
		return fmt.Errorf("AI is off: no OpenRouter key is set (an admin enters it in Tech Admin, AI settings)")
	}
	_, err := c.chatCompletion(ctx, chatRequest{
		Model:     c.model(ctx, taskCheap),
		MaxTokens: 1,
		Messages:  []chatMessage{{Role: "user", Content: "ping"}},
	})
	return err
}
