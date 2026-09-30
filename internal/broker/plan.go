package broker

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"regexp"
	"sort"
	"strings"
)

const (
	MaxInputBytes    = 1 << 20
	MaxItems         = MaxPaidRequestsPerCall
	MaxQuestions     = 64
	MaxOutboundBytes = 4 << 20
	maxChoices       = 255
	maxScoreLevels   = 10
)

var (
	questionIDPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`)
	itemIDPattern     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)
	credentialPattern = regexp.MustCompile(`(?i)(-----BEGIN [A-Z ]*PRIVATE KEY-----|\bBearer[ 	]+[A-Za-z0-9._~+/-]{12,}|\b(?:sk-(?:proj-|or-v1-)?|ghp_|gho_|github_pat_|xox[baprs]-|AIza|AKIA|ASIA|hf_|gta_|glpat-)[A-Za-z0-9_-]{12,}|\beyJ[A-Za-z0-9_-]{12,}\.[A-Za-z0-9_-]{12,}\.[A-Za-z0-9_-]{12,}\b|\b[0-9]{8,10}:[A-Za-z0-9_-]{30,}\b|\b(?:postgres(?:ql)?|mysql|mongodb(?:\+srv)?|redis)://[^\s/@:]+:[^\s/@]+@|(?:api[_ -]?key|access[_ -]?token|refresh[_ -]?token|password|passwd|client[_ -]?secret|private[_ -]?key|session[_ -]?(?:id|cookie|token)?|пароль)["']?\s*[:=]\s*["']?[^\s"'&,;]{4,})`)
)

type Input struct {
	State     json.RawMessage            `json:"state,omitempty"`
	Items     map[string]json.RawMessage `json:"items,omitempty"`
	Questions map[string]Question        `json:"questions"`
}

type Question struct {
	Mode          string          `json:"mode"`
	Task          json.RawMessage `json:"task"`
	Criteria      json.RawMessage `json:"criteria,omitempty"`
	Options       json.RawMessage `json:"options,omitempty"`
	Rubric        json.RawMessage `json:"rubric,omitempty"`
	MinConfidence *float64        `json:"min_confidence,omitempty"`
}

type providerQuestion struct {
	Type         string          `json:"type"`
	Instructions json.RawMessage `json:"instructions"`
	Criteria     json.RawMessage `json:"criteria,omitempty"`
}

type ProviderRequest struct {
	State     json.RawMessage             `json:"state"`
	Questions map[string]providerQuestion `json:"questions"`
	Model     string                      `json:"model"`
}

type Plan struct {
	Single            *ProviderRequest
	Items             map[string]ProviderRequest
	Questions         map[string]Question
	ProviderQuestions map[string]providerQuestion
	ItemIDs           []string
	Modes             map[string]int
}

var ErrInvalidInput = errors.New("invalid evaluate input")
var ErrCredential = errors.New("evaluate input may contain a credential; remove it before calling")

// PlanInput permits agent-defined evidence and questions while fixing the
// provider, endpoint and model at the server. The scanner is best-effort only.
func PlanInput(raw []byte, model string) (Plan, error) {
	if len(raw) == 0 || len(raw) > MaxInputBytes || model == "" {
		return Plan{}, ErrInvalidInput
	}
	if credentialPattern.Match(raw) {
		return Plan{}, ErrCredential
	}
	var in Input
	if err := decodeStrict(raw, &in); err != nil || len(in.Questions) == 0 || len(in.Questions) > MaxQuestions {
		return Plan{}, ErrInvalidInput
	}
	var decoded any
	if json.Unmarshal(raw, &decoded) != nil || hasCredential(decoded) {
		return Plan{}, ErrCredential
	}
	if in.Items == nil {
		if !validContent(in.State) {
			return Plan{}, ErrInvalidInput
		}
	} else if len(in.Items) == 0 || len(in.State) > 0 && !validContent(in.State) {
		return Plan{}, ErrInvalidInput
	}
	if len(in.Items) > MaxItems {
		return Plan{}, ErrCostLimit
	}
	plan := Plan{Questions: in.Questions, ProviderQuestions: map[string]providerQuestion{}, Modes: map[string]int{"noul": 0, "choice": 0, "score": 0}}
	for id, q := range in.Questions {
		if !questionIDPattern.MatchString(id) || !validContent(q.Task) || len(q.Task) > 8<<10 || q.MinConfidence != nil && (!finite01(*q.MinConfidence)) {
			return Plan{}, ErrInvalidInput
		}
		pq := providerQuestion{Type: q.Mode, Instructions: q.Task}
		switch q.Mode {
		case "noul":
			if len(q.Options) > 0 || len(q.Rubric) > 0 || !validNoulCriteria(q.Criteria) {
				return Plan{}, ErrInvalidInput
			}
			if len(q.Criteria) > 0 {
				pq.Criteria = q.Criteria
			}
		case "choice":
			if len(q.Criteria) > 0 || len(q.Rubric) > 0 || !validOptions(q.Options) {
				return Plan{}, ErrInvalidInput
			}
			var options map[string]json.RawMessage
			_ = json.Unmarshal(q.Options, &options)
			if _, reserved := options["__uncertain__"]; reserved {
				return Plan{}, ErrInvalidInput
			}
			pq.Criteria = q.Options
		case "score":
			if len(q.Criteria) > 0 || len(q.Options) > 0 || q.MinConfidence != nil || !validRubric(q.Rubric) {
				return Plan{}, ErrInvalidInput
			}
			pq.Criteria = q.Rubric
		default:
			return Plan{}, ErrInvalidInput
		}
		plan.Modes[q.Mode]++
		plan.ProviderQuestions[id] = pq
	}
	if in.Items == nil {
		one := ProviderRequest{State: in.State, Questions: plan.ProviderQuestions, Model: model}
		plan.Single = &one
		return plan, nil
	}
	plan.Items = map[string]ProviderRequest{}
	outboundBytes := 0
	for id, item := range in.Items {
		if !itemIDPattern.MatchString(id) || !validContent(item) {
			return Plan{}, ErrInvalidInput
		}
		state := map[string]json.RawMessage{"item": item}
		if len(in.State) > 0 {
			state["context"] = in.State
		}
		stateJSON, _ := json.Marshal(state)
		request := ProviderRequest{State: stateJSON, Questions: plan.ProviderQuestions, Model: model}
		encoded, err := json.Marshal(request)
		if err != nil || len(encoded) > MaxOutboundBytes-outboundBytes {
			return Plan{}, ErrInvalidInput
		}
		outboundBytes += len(encoded)
		plan.Items[id] = request
		plan.ItemIDs = append(plan.ItemIDs, id)
	}
	sort.Strings(plan.ItemIDs)
	return plan, nil
}

func validContent(raw json.RawMessage) bool {
	var v any
	if len(raw) == 0 || json.Unmarshal(raw, &v) != nil {
		return false
	}
	switch value := v.(type) {
	case string:
		return strings.TrimSpace(value) != ""
	case map[string]any:
		return len(value) > 0
	case []any:
		return len(value) > 0
	default:
		return false
	}
}

func validNoulCriteria(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return true
	}
	var criteria map[string]json.RawMessage
	if json.Unmarshal(raw, &criteria) != nil || len(criteria) == 0 || len(criteria) > 2 {
		return false
	}
	for label, text := range criteria {
		if (label != "true" && label != "false") || !validDescription(text) {
			return false
		}
	}
	return true
}

func validDescription(raw json.RawMessage) bool {
	return len(raw) <= 1024 && validContent(raw)
}

func validOptions(raw json.RawMessage) bool {
	var options map[string]json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &options) != nil || len(options) < 2 || len(options) > maxChoices {
		return false
	}
	for id, text := range options {
		if !questionIDPattern.MatchString(id) || (string(bytes.TrimSpace(text)) != "null" && !validDescription(text)) {
			return false
		}
	}
	return true
}

func validRubric(raw json.RawMessage) bool {
	var levels []json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &levels) != nil || len(levels) < 2 || len(levels) > maxScoreLevels {
		return false
	}
	for _, level := range levels {
		if !validDescription(level) {
			return false
		}
	}
	return true
}

func finite01(f float64) bool {
	return !math.IsNaN(f) && !math.IsInf(f, 0) && f >= 0 && f <= 1
}

func hasCredential(value any) bool {
	switch v := value.(type) {
	case map[string]any:
		for key, item := range v {
			name := strings.ToLower(key)
			name = strings.NewReplacer("_", "", "-", "", " ", "").Replace(name)
			switch {
			case strings.Contains(name, "token"), strings.Contains(name, "secret"),
				strings.Contains(name, "password"), strings.Contains(name, "passwd"),
				strings.Contains(name, "credential"), strings.Contains(name, "apikey"),
				strings.Contains(name, "privatekey"), strings.Contains(name, "accesskey"),
				strings.Contains(name, "sessionid"), strings.Contains(name, "sessioncookie"),
				name == "bearer", strings.Contains(name, "пароль"):
				return true
			}
			if hasCredential(item) {
				return true
			}
		}
	case []any:
		for _, item := range v {
			if hasCredential(item) {
				return true
			}
		}
	case string:
		return credentialPattern.MatchString(v)
	}
	return false
}

func ToolSchema() map[string]any {
	question := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"mode":           map[string]any{"type": "string", "enum": []string{"noul", "choice", "score"}},
			"task":           map[string]any{"description": "Your precise judgment instruction as nonempty text, object, or array"},
			"criteria":       map[string]any{"type": "object", "description": "noul only: optional true/false descriptions"},
			"options":        map[string]any{"type": "object", "description": "choice only: named options; add none_unknown when no option may fit"},
			"rubric":         map[string]any{"type": "array", "minItems": 2, "maxItems": maxScoreLevels, "description": "score only: ordered level descriptions, low to high (2–10 levels)"},
			"min_confidence": map[string]any{"type": "number", "minimum": 0, "maximum": 1, "description": "optional noul/choice abstention threshold"},
		},
		"required":             []string{"mode", "task"},
		"additionalProperties": false,
	}
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"state":     map[string]any{"description": "Evidence/context as nonempty text, object or array; optional with items"},
			"items":     map[string]any{"type": "object", "minProperties": 1, "maxProperties": MaxPaidRequestsPerCall, "description": "Optional named records; each is a separate paid request"},
			"questions": map[string]any{"type": "object", "minProperties": 1, "maxProperties": MaxQuestions, "additionalProperties": question},
		},
		"required":             []string{"questions"},
		"additionalProperties": false,
	}
}
