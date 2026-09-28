package broker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"
)

var ErrProvider = errors.New("provider request failed or returned an invalid answer; do not retry blindly")

type Provider interface {
	Evaluate(context.Context, ProviderRequest) ([]byte, error)
}

type OpenRouterProvider struct {
	key      string
	endpoint string
	client   *http.Client
}

func NewOpenRouterProvider(key string) (*OpenRouterProvider, error) {
	if key == "" {
		return nil, errors.New("OpenRouter API key is required")
	}
	return &OpenRouterProvider{
		key:      key,
		endpoint: providerURL,
		client:   &http.Client{Timeout: 60 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	}, nil
}

func (p *OpenRouterProvider) Evaluate(ctx context.Context, request ProviderRequest) ([]byte, error) {
	if p == nil || p.key == "" || p.endpoint == "" || p.client == nil {
		return nil, ErrProvider
	}
	body, err := json.Marshal(request)
	if err != nil {
		return nil, ErrProvider
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, ErrProvider
	}
	r.Header.Set("Authorization", "Bearer "+p.key)
	r.Header.Set("Content-Type", "application/json")
	response, err := p.client.Do(r) // exactly one call; no automatic 429/5xx retry
	if err != nil {
		return nil, ErrProvider
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		// Never read or forward a provider error body: it may echo the request.
		return nil, ErrProvider
	}
	reply, err := io.ReadAll(io.LimitReader(response.Body, (16<<20)+1))
	if err != nil || len(reply) == 0 || len(reply) > 16<<20 || !json.Valid(reply) {
		return nil, ErrProvider
	}
	return reply, nil
}
