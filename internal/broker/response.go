package broker

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strconv"
)

var ErrInvalidProviderReply = errors.New("provider returned an incomplete or mismatched decision")

type Answer struct {
	Type          string                     `json:"type"`
	Noul          *float64                   `json:"noul,omitempty"`
	Choice        *string                    `json:"choice,omitempty"`
	Score         *float64                   `json:"score,omitempty"`
	Confidence    *float64                   `json:"confidence,omitempty"`
	Probabilities map[string]float64         `json:"probabilities,omitempty"`
	Legend        map[string]json.RawMessage `json:"legend,omitempty"`
	Uncertain     bool                       `json:"uncertain,omitempty"`
}

type Usage struct {
	InputTokens  int64    `json:"input_tokens,omitempty"`
	OutputTokens int64    `json:"output_tokens,omitempty"`
	Cost         *float64 `json:"cost_usd,omitempty"`
}

type Evaluation struct {
	Answers map[string]Answer `json:"answers"`
	Model   string            `json:"model"`
	Usage   Usage             `json:"usage"`
}

func ValidateReply(raw []byte, questions map[string]Question, model string) (Evaluation, error) {
	if len(raw) == 0 || len(raw) > 16<<20 || rejectDuplicateKeys(raw) != nil {
		return Evaluation{}, ErrInvalidProviderReply
	}
	var wire struct {
		Answers map[string]json.RawMessage `json:"answers"`
		Usage   struct {
			InputTokens  *int64   `json:"input_tokens"`
			OutputTokens *int64   `json:"output_tokens"`
			Cost         *float64 `json:"cost"`
		} `json:"usage"`
	}
	if json.Unmarshal(raw, &wire) != nil || len(wire.Answers) != len(questions) {
		return Evaluation{}, ErrInvalidProviderReply
	}
	result := Evaluation{Answers: map[string]Answer{}, Model: model}
	if wire.Usage.InputTokens != nil && *wire.Usage.InputTokens >= 0 {
		result.Usage.InputTokens = *wire.Usage.InputTokens
	}
	if wire.Usage.OutputTokens != nil && *wire.Usage.OutputTokens >= 0 {
		result.Usage.OutputTokens = *wire.Usage.OutputTokens
	}
	if wire.Usage.Cost != nil && *wire.Usage.Cost >= 0 && !math.IsInf(*wire.Usage.Cost, 0) && !math.IsNaN(*wire.Usage.Cost) {
		result.Usage.Cost = wire.Usage.Cost
	}
	for id, q := range questions {
		body, ok := wire.Answers[id]
		if !ok {
			return Evaluation{}, ErrInvalidProviderReply
		}
		var a Answer
		if json.Unmarshal(body, &a) != nil || a.Type != q.Mode || a.Confidence != nil && !finite01(*a.Confidence) {
			return Evaluation{}, ErrInvalidProviderReply
		}
		// Uncertainty is a local policy output, never a provider-supplied claim.
		a.Uncertain = false
		switch q.Mode {
		case "noul":
			if a.Noul == nil || !finite01(*a.Noul) || a.Choice != nil || a.Score != nil {
				return Evaluation{}, ErrInvalidProviderReply
			}
			if q.MinConfidence != nil && math.Abs(2*(*a.Noul)-1) < *q.MinConfidence {
				a.Uncertain = true
			}
			a.Probabilities = nil
			a.Legend = nil
		case "choice":
			var options map[string]json.RawMessage
			if json.Unmarshal(q.Options, &options) != nil || a.Choice == nil || a.Noul != nil || a.Score != nil ||
				!distribution(a.Probabilities, len(options), func(k string) bool { _, ok := options[k]; return ok }) ||
				(q.MinConfidence != nil && a.Confidence == nil) {
				return Evaluation{}, ErrInvalidProviderReply
			}
			if _, ok := options[*a.Choice]; !ok {
				return Evaluation{}, ErrInvalidProviderReply
			}
			for _, p := range a.Probabilities {
				if p > a.Probabilities[*a.Choice]+1e-6 {
					return Evaluation{}, ErrInvalidProviderReply
				}
			}
			if q.MinConfidence != nil && *a.Confidence < *q.MinConfidence {
				a.Uncertain = true
				abstain := "__uncertain__"
				a.Choice = &abstain
			}
			a.Legend = nil
		case "score":
			var levels []json.RawMessage
			if json.Unmarshal(q.Rubric, &levels) != nil || a.Score == nil || math.IsNaN(*a.Score) || math.IsInf(*a.Score, 0) ||
				*a.Score < 0 || *a.Score > float64(len(levels)-1) || a.Noul != nil || a.Choice != nil ||
				len(a.Legend) != len(levels) || !distribution(a.Probabilities, len(levels), func(k string) bool {
				i, err := strconv.Atoi(k)
				return err == nil && i >= 0 && i < len(levels) && strconv.Itoa(i) == k
			}) {
				return Evaluation{}, ErrInvalidProviderReply
			}
			weighted := 0.0
			for i, level := range levels {
				key := strconv.Itoa(i)
				if !sameDescription(a.Legend[key], level) {
					return Evaluation{}, ErrInvalidProviderReply
				}
				weighted += float64(i) * a.Probabilities[key]
			}
			if math.Abs(*a.Score-weighted) > 0.05 {
				return Evaluation{}, ErrInvalidProviderReply
			}
		default:
			return Evaluation{}, ErrInvalidProviderReply
		}
		// Only these validated fields survive. Never forward provider echo, error
		// text, arbitrary fields or the submitted state to an MCP caller.
		result.Answers[id] = a
	}
	return result, nil
}

func distribution(values map[string]float64, want int, validKey func(string) bool) bool {
	if len(values) != want {
		return false
	}
	sum := 0.0
	for key, probability := range values {
		if !validKey(key) || !finite01(probability) {
			return false
		}
		sum += probability
	}
	return math.Abs(sum-1) <= 0.01
}

func sameDescription(got, want json.RawMessage) bool {
	if len(got) == 0 || len(want) == 0 {
		return false
	}
	var actual, expected any
	if json.Unmarshal(got, &actual) != nil || json.Unmarshal(want, &expected) != nil {
		return false
	}
	if text, ok := actual.(string); ok {
		if _, wantString := expected.(string); !wantString && json.Valid([]byte(text)) {
			var decoded any
			if json.Unmarshal([]byte(text), &decoded) == nil {
				actual = decoded
			}
		}
	}
	return reflect.DeepEqual(actual, expected)
}

func replyJSON(v any) []byte {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if encoder.Encode(v) != nil {
		return []byte(`{"error":"response encoding failed"}`)
	}
	return bytes.TrimSpace(buffer.Bytes())
}
