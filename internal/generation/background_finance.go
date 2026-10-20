package generation

import (
	"context"
	"math"
	"time"

	bg "minigateway/internal/foundation/contracts/background/v1"
	v1 "minigateway/internal/foundation/contracts/execution/v1"
	v2 "minigateway/internal/foundation/contracts/execution/v2"
	bs "minigateway/internal/generation/backgroundstate"
)

func (s *BackgroundService) finishFinance(ctx context.Context, row bs.Record) error {
	if row.BillingMode != "strict" || row.BillingID == "" {
		return nil
	}
	if nilPort(s.finance) {
		return bg.ErrUnsupportedBilling
	}
	usage := backgroundFinancialUsage(row)
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	return s.finance.CompleteBackgroundReservation(cleanup, row.BillingID, row.TokenID, row.UserID, row.Model, usage, row.Stage == string(bg.StageTerminal) && usage != nil)
}

func backgroundFinancialUsage(row bs.Record) *v1.Usage {
	if detail := row.UsageMetadata.Usage; detail != nil && detail.Validate() == nil && detail.Completeness == v2.Complete {
		c := detail.Counts
		if c.InputTokens != nil && c.OutputTokens != nil && c.TotalTokens != nil && *c.InputTokens <= math.MaxInt && *c.OutputTokens <= math.MaxInt && *c.TotalTokens <= math.MaxInt {
			return &v1.Usage{PromptTokens: int(*c.InputTokens), CompletionTokens: int(*c.OutputTokens), TotalTokens: int(*c.TotalTokens)}
		}
	}
	return nil
}

// Keep the durable replay claim until the financial owner acknowledges its
// idempotent receipt. A failed write retries finance without another network RPC.
func (p *backgroundProgress) settleTerminalFinance(ctx context.Context) error {
	if p.row.BillingMode != "strict" {
		return nil
	}
	if err := p.service.finishFinance(ctx, p.row); err != nil {
		return err
	}
	eventsPending := p.row.Stream && !p.snapshot.EventComplete && !p.snapshot.EventUnavailable
	usagePending := backgroundFinancialUsage(p.row) == nil && p.row.ControlCalls < 32
	if !eventsPending && !usagePending && p.row.ReplayPending {
		row, err := p.service.repository.UpdateBackground(ctx, p.claim, p.row.Revision, bs.Update{Stage: bg.StageTerminal})
		if err != nil {
			return err
		}
		p.row = row
	}
	return nil
}

// Control commits the bounded control-call budget before acquiring network I/O.
func (p *backgroundProgress) Control(ctx context.Context) error {
	if e := p.refresh(ctx); e != nil {
		return e
	}
	row, err := p.service.repository.UpdateBackground(ctx, p.claim, p.row.Revision, bs.Update{Stage: bg.Stage(p.row.Stage), Snapshot: p.row.Snapshot, ReplayPending: p.row.ReplayPending, DueAt: p.row.DueAt, ControlCall: true})
	if err == nil {
		p.row = row
	}
	return err
}
