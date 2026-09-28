package broker

import (
	"context"
	"errors"
)

type Service struct {
	Provider Provider
	Audit    *Audit
	Model    string
	paidSlot chan struct{}
}

func NewService(provider Provider, audit *Audit, model string) (*Service, error) {
	if provider == nil || audit == nil || model == "" {
		return nil, errors.New("broker service is incomplete")
	}
	return &Service{Provider: provider, Audit: audit, Model: model, paidSlot: make(chan struct{}, 1)}, nil
}

type batchMeta struct {
	Model            string   `json:"model"`
	ItemCount        int      `json:"item_count"`
	Succeeded        int      `json:"succeeded"`
	Failed           int      `json:"failed"`
	NotAttempted     int      `json:"not_attempted"`
	AllFailed        bool     `json:"all_failed"`
	CostUSD          *float64 `json:"cost_usd,omitempty"`
	CostUnknownCount int      `json:"cost_unknown_count"`
}

func (s *Service) Evaluate(ctx context.Context, profileID string, raw []byte) ([]byte, error) {
	if s == nil || !profileIDPattern.MatchString(profileID) {
		return nil, ErrInvalidInput
	}
	plan, err := PlanInput(raw, s.Model)
	if err != nil {
		return nil, err
	}
	select {
	case s.paidSlot <- struct{}{}:
		defer func() { <-s.paidSlot }()
	case <-ctx.Done():
		return nil, errors.New("broker call canceled before provider request")
	}
	if plan.Single != nil {
		result, err := s.attempt(ctx, profileID, *plan.Single, plan)
		if err != nil {
			return nil, err
		}
		return replyJSON(result), nil
	}
	out := struct {
		Results map[string]Evaluation `json:"results"`
		Errors  map[string]string     `json:"errors"`
		Meta    batchMeta             `json:"meta"`
	}{Results: map[string]Evaluation{}, Errors: map[string]string{}, Meta: batchMeta{Model: s.Model, ItemCount: len(plan.ItemIDs)}}
	totalCost := 0.0
	for index, id := range plan.ItemIDs {
		if ctx.Err() != nil {
			for _, remaining := range plan.ItemIDs[index:] {
				out.Errors[remaining] = "not attempted: caller canceled; inspect earlier results before retrying"
				out.Meta.NotAttempted++
			}
			break
		}
		result, err := s.attempt(ctx, profileID, plan.Items[id], plan)
		if err != nil {
			out.Errors[id] = err.Error()
			out.Meta.Failed++
			out.Meta.CostUnknownCount++
			continue
		}
		out.Results[id] = result
		out.Meta.Succeeded++
		if result.Usage.Cost == nil {
			out.Meta.CostUnknownCount++
		} else {
			totalCost += *result.Usage.Cost
		}
		if len(replyJSON(out)) > 16<<20 {
			delete(out.Results, id)
			out.Meta.Succeeded--
			out.Meta.Failed++
			out.Errors[id] = "result exceeds response limit; use smaller batches"
			for _, remaining := range plan.ItemIDs[index+1:] {
				out.Errors[remaining] = "not attempted after response limit"
				out.Meta.NotAttempted++
			}
			break
		}
	}
	out.Meta.AllFailed = out.Meta.Succeeded == 0
	if out.Meta.CostUnknownCount == 0 {
		out.Meta.CostUSD = &totalCost
	}
	return replyJSON(out), nil
}

func (s *Service) attempt(ctx context.Context, profileID string, request ProviderRequest, plan Plan) (Evaluation, error) {
	if err := s.Audit.Append(profileID, "attempt", plan.Modes, nil); err != nil {
		return Evaluation{}, errors.New("audit unavailable; provider was not called")
	}
	body, err := s.Provider.Evaluate(ctx, request)
	if err != nil {
		_ = s.Audit.Append(profileID, "failed", plan.Modes, nil)
		return Evaluation{}, ErrProvider
	}
	result, err := ValidateReply(body, plan.Questions, s.Model)
	if err != nil {
		_ = s.Audit.Append(profileID, "failed", plan.Modes, nil)
		return Evaluation{}, ErrInvalidProviderReply
	}
	if err := s.Audit.Append(profileID, "ok", plan.Modes, result.Usage.Cost); err != nil {
		return Evaluation{}, errors.New("result audit failed after provider call; do not retry")
	}
	return result, nil
}
