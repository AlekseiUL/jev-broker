package broker

import (
	"context"
	"errors"
	"math"
	"sync"
	"time"
)

const MaxPaidRequestsPerCall = 8
const MaxProfileRequestsPerUTCDate = 32

var ErrCostLimit = errors.New("broker paid-request limit reached; no provider call was made")

type profileBudget struct {
	slot     chan struct{}
	date     string
	reserved int
}

type Service struct {
	Provider Provider
	Audit    *Audit
	Model    string
	budgetMu sync.Mutex
	budgets  map[string]*profileBudget
}

func NewService(provider Provider, audit *Audit, model string) (*Service, error) {
	if provider == nil || audit == nil || model == "" {
		return nil, errors.New("broker service is incomplete")
	}
	return &Service{Provider: provider, Audit: audit, Model: model, budgets: make(map[string]*profileBudget)}, nil
}

// Admission is local to this process. A reservation is never refunded: a failed
// or canceled request may already have been billed upstream. Restarting the
// broker resets the counter; only provider-side account limits bound dollars.
func (s *Service) reserve(profileID string, count int) error {
	s.budgetMu.Lock()
	defer s.budgetMu.Unlock()
	b := s.budgets[profileID]
	today := time.Now().UTC().Format("2006-01-02")
	if b.date != today {
		b.date, b.reserved = today, 0
	}
	if b.reserved+count > MaxProfileRequestsPerUTCDate {
		return ErrCostLimit
	}
	b.reserved += count
	return nil
}

func (s *Service) profileSlot(profileID string) chan struct{} {
	s.budgetMu.Lock()
	defer s.budgetMu.Unlock()
	if s.budgets == nil {
		s.budgets = make(map[string]*profileBudget)
	}
	if s.budgets[profileID] == nil {
		s.budgets[profileID] = &profileBudget{slot: make(chan struct{}, 1)}
	}
	return s.budgets[profileID].slot
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
	count := 1
	if plan.Single == nil {
		count = len(plan.ItemIDs)
	}
	if count > MaxPaidRequestsPerCall {
		return nil, ErrCostLimit
	}
	slot := s.profileSlot(profileID)
	select {
	case slot <- struct{}{}:
		defer func() { <-slot }()
	case <-ctx.Done():
		return nil, errors.New("broker call canceled before provider request")
	}
	if ctx.Err() != nil {
		return nil, errors.New("broker call canceled before provider request")
	}
	if err := s.reserve(profileID, count); err != nil {
		return nil, err
	}
	if plan.Single != nil {
		result, err := s.attempt(ctx, profileID, *plan.Single, plan)
		if err != nil {
			return nil, err
		}
		return replyJSON(result)
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
		encoded, encodeErr := replyJSON(out)
		if encodeErr != nil || len(encoded) > 16<<20 {
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
		if math.IsInf(totalCost, 0) || math.IsNaN(totalCost) {
			return nil, ErrInvalidProviderReply
		}
		out.Meta.CostUSD = &totalCost
	}
	return replyJSON(out)
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
