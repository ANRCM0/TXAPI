package billing

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
	"minigateway/internal/model"
	relaymodel "minigateway/internal/protocol/model"
)

var (
	ErrLedgerNotFound    = errors.New("billing reservation not found")
	ErrLedgerInput       = errors.New("invalid billing query or resolution")
	ErrNotResolvable     = errors.New("only pending reservations with expired leases can be resolved")
	ErrCostExceedsHold   = errors.New("verified cost exceeds hold; explicitly cap the charge or waive it")
	ErrBillingForbidden  = errors.New("active administrator required")
	reservationIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)
)

const (
	ResolveUsage = "settle_usage"
	ResolveCap   = "cap_usage"
	ResolveWaive = "waive"
)

// Monetary units are decimal strings on the wire, preserving int64 precision
// in browsers. These DTOs intentionally exclude internal fingerprints.
type ReservationView struct {
	ID            string    `json:"id"`
	UserID        uint      `json:"user_id"`
	TokenID       uint      `json:"token_id"`
	PriceVersion  string    `json:"price_version"`
	OriginModel   string    `json:"origin_model"`
	ReservedUnits string    `json:"reserved_units"`
	ChargeUnits   string    `json:"charge_units"`
	State         string    `json:"state"`
	Reason        string    `json:"reason"`
	LeaseUntil    int64     `json:"lease_until"`
	CanResolve    bool      `json:"can_resolve"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type AttemptView struct {
	Number       int       `json:"number"`
	ChannelID    uint      `json:"channel_id"`
	CredentialID uint      `json:"credential_id"`
	ActualModel  string    `json:"actual_model"`
	Group        string    `json:"group"`
	Status       int       `json:"status"`
	Termination  string    `json:"termination"`
	PromptTokens int       `json:"prompt_tokens"`
	OutputTokens int       `json:"output_tokens"`
	CostUnits    string    `json:"cost_units"`
	CostKnown    bool      `json:"cost_known"`
	State        string    `json:"state"`
	CreatedAt    time.Time `json:"created_at"`
}

type ResolutionView struct {
	ActorID         uint      `json:"actor_id"`
	Action          string    `json:"action"`
	Reason          string    `json:"reason"`
	Evidence        string    `json:"evidence"`
	PromptTokens    int       `json:"prompt_tokens"`
	OutputTokens    int       `json:"output_tokens"`
	CostKnown       bool      `json:"cost_known"`
	CalculatedUnits string    `json:"calculated_units"`
	ChargeUnits     string    `json:"charge_units"`
	CreatedAt       time.Time `json:"created_at"`
}

type ReservationDetail struct {
	Reservation   ReservationView `json:"reservation"`
	Attempts      []AttemptView   `json:"attempts"`
	Resolution    *ResolutionView `json:"resolution"`
	PriceSnapshot string          `json:"price_snapshot"`
}

type LedgerQuery struct {
	State           string
	UserID, TokenID uint
	Limit, Offset   int
}

type ResolutionInput struct {
	Action   string           `json:"action"`
	Reason   string           `json:"reason"`
	Evidence string           `json:"evidence"`
	Usage    *ResolutionUsage `json:"usage,omitempty"`
}

type ResolutionUsage struct {
	PromptTokens *int `json:"prompt_tokens"`
	OutputTokens *int `json:"completion_tokens"`
}

func reservationView(row model.BillingReservation, ready bool) ReservationView {
	return ReservationView{
		ID: row.ID, UserID: row.UserID, TokenID: row.TokenID,
		PriceVersion: row.PriceVersion, OriginModel: row.OriginModel,
		ReservedUnits: strconv.FormatInt(row.ReservedUnits, 10), ChargeUnits: strconv.FormatInt(row.ChargeUnits, 10),
		State: row.State, Reason: row.Reason, LeaseUntil: row.LeaseUntil,
		CanResolve: ready && row.State == Pending && row.LeaseUntil <= time.Now().UnixMilli(),
		CreatedAt:  row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

func resolutionView(row model.BillingResolution) *ResolutionView {
	return &ResolutionView{
		ActorID: row.ActorID, Action: row.Action, Reason: row.Reason, Evidence: row.Evidence,
		PromptTokens: row.PromptTokens, OutputTokens: row.OutputTokens, CostKnown: row.CostKnown,
		CalculatedUnits: strconv.FormatInt(row.CalculatedUnits, 10), ChargeUnits: strconv.FormatInt(row.ChargeUnits, 10),
		CreatedAt: row.CreatedAt,
	}
}

func (e *Engine) ListReservations(ctx context.Context, q LedgerQuery) ([]ReservationView, error) {
	if e == nil || e.db == nil {
		return nil, ErrStrictUnavailable
	}
	if q.Limit < 1 || q.Limit > 100 || q.Offset < 0 || q.Offset > 1_000_000 {
		return nil, ErrLedgerInput
	}
	db := e.db.WithContext(ctx).Model(&model.BillingReservation{})
	if q.State != "" {
		switch q.State {
		case Reserved, Active, Pending, Settled:
		default:
			return nil, ErrLedgerInput
		}
		db = db.Where("state=?", q.State)
	}
	if q.UserID != 0 {
		db = db.Where("user_id=?", q.UserID)
	}
	if q.TokenID != 0 {
		db = db.Where("token_id=?", q.TokenID)
	}
	var rows []model.BillingReservation
	if err := db.Order("created_at DESC, id DESC").Limit(q.Limit).Offset(q.Offset).Find(&rows).Error; err != nil {
		return nil, err
	}
	result := make([]ReservationView, 0, len(rows))
	for _, row := range rows {
		result = append(result, reservationView(row, e.StrictReady()))
	}
	return result, nil
}

func (e *Engine) ReservationDetail(ctx context.Context, id string) (*ReservationDetail, error) {
	if e == nil || e.db == nil {
		return nil, ErrStrictUnavailable
	}
	if !reservationIDPattern.MatchString(id) {
		return nil, ErrLedgerInput
	}
	detail := &ReservationDetail{Attempts: []AttemptView{}}
	err := e.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row model.BillingReservation
		if err := tx.First(&row, "id=?", id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrLedgerNotFound
			}
			return err
		}
		detail.Reservation = reservationView(row, e.StrictReady())
		var attempts []model.BillingAttempt
		if err := tx.Where("reservation_id=?", id).Order("number ASC").Find(&attempts).Error; err != nil {
			return err
		}
		for _, a := range attempts {
			detail.Attempts = append(detail.Attempts, AttemptView{
				Number: a.Number, ChannelID: a.ChannelID, CredentialID: a.CredentialID, ActualModel: a.ActualModel, Group: a.Group,
				Status: a.Status, Termination: a.Termination, PromptTokens: a.PromptTokens, OutputTokens: a.OutputTokens,
				CostUnits: strconv.FormatInt(a.CostUnits, 10), CostKnown: a.CostKnown, State: a.State, CreatedAt: a.CreatedAt,
			})
		}
		var decision model.BillingResolution
		result := tx.Where("reservation_id=?", id).Limit(1).Find(&decision)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 1 {
			detail.Resolution = resolutionView(decision)
		}
		var history model.BillingPriceVersion
		if err := tx.First(&history, "version=?", row.PriceVersion).Error; err != nil {
			return err
		}
		detail.PriceSnapshot = history.Payload
		return nil
	})
	return detail, err
}

func (in *ResolutionInput) normalize() (*relaymodel.Usage, error) {
	in.Reason, in.Evidence = strings.TrimSpace(in.Reason), strings.TrimSpace(in.Evidence)
	if in.Reason == "" || len(in.Reason) > 1000 || in.Evidence == "" || len(in.Evidence) > 2000 {
		return nil, ErrLedgerInput
	}
	switch in.Action {
	case ResolveWaive:
		if in.Usage != nil {
			return nil, ErrLedgerInput
		}
		return nil, nil
	case ResolveUsage, ResolveCap:
		if in.Usage == nil || in.Usage.PromptTokens == nil || in.Usage.OutputTokens == nil {
			return nil, ErrLedgerInput
		}
		u := &relaymodel.Usage{PromptTokens: *in.Usage.PromptTokens, CompletionTokens: *in.Usage.OutputTokens}
		if !completeUsage(u) {
			return nil, ErrLedgerInput
		}
		return u, nil
	default:
		return nil, ErrLedgerInput
	}
}

// ResolveReservation requires an expired pending hold. Prices and target group
// come from durable records, never from a request-supplied tariff or model.
// The decision, both balances and terminal state commit together.
func (e *Engine) ResolveReservation(ctx context.Context, id string, actorID uint, in ResolutionInput) (*ResolutionView, error) {
	if !e.StrictReady() {
		return nil, ErrStrictUnavailable
	}
	if !reservationIDPattern.MatchString(id) {
		return nil, ErrLedgerInput
	}
	u, err := in.normalize()
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(payload)
	fingerprint := hex.EncodeToString(sum[:])
	var decision model.BillingResolution
	err = e.strict.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var admin model.User
		if err := tx.Where("id=? AND role=? AND status=?", actorID, model.RoleAdmin, model.UserStatusActive).First(&admin).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrBillingForbidden
			}
			return err
		}
		var row model.BillingReservation
		if err := tx.First(&row, "id=?", id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrLedgerNotFound
			}
			return err
		}
		existing := tx.Where("reservation_id=?", id).Limit(1).Find(&decision)
		if existing.Error != nil {
			return existing.Error
		}
		if existing.RowsAffected == 1 {
			if row.State != Settled || decision.Fingerprint != fingerprint {
				return ErrSettlementConflict
			}
			return nil
		}
		if row.State != Pending || row.LeaseUntil > time.Now().UnixMilli() {
			return ErrNotResolvable
		}
		decision = model.BillingResolution{
			ReservationID: id, ActorID: actorID, Action: in.Action, Reason: in.Reason,
			Evidence: in.Evidence, Fingerprint: fingerprint,
		}
		if u != nil {
			var attempt model.BillingAttempt
			if err := tx.Where("reservation_id=?", id).Order("number DESC").First(&attempt).Error; err != nil {
				return err
			}
			var history model.BillingPriceVersion
			if err := tx.First(&history, "version=?", row.PriceVersion).Error; err != nil {
				return err
			}
			prices, err := restoreSnapshot(history.Version, history.Payload)
			if err != nil {
				return err
			}
			cost, err := snapshotCost(prices, u, attempt.ActualModel, row.OriginModel, attempt.Group)
			if err != nil {
				return fmt.Errorf("%w: usage exceeds supported price range", ErrLedgerInput)
			}
			decision.PromptTokens, decision.OutputTokens = u.PromptTokens, u.CompletionTokens
			decision.CostKnown, decision.CalculatedUnits, decision.ChargeUnits = true, cost, cost
			if cost > row.ReservedUnits {
				if in.Action != ResolveCap {
					return ErrCostExceedsHold
				}
				decision.ChargeUnits = row.ReservedUnits
			} else if in.Action == ResolveCap {
				return fmt.Errorf("%w: cap_usage requires verified cost above the hold", ErrLedgerInput)
			}
		}
		if err := settleBalances(tx, row, decision.ChargeUnits); err != nil {
			return err
		}
		if err := tx.Create(&decision).Error; err != nil {
			return err
		}
		return tx.Model(&row).Updates(map[string]any{
			"state": Settled, "reason": "admin_" + in.Action, "charge_units": decision.ChargeUnits, "fingerprint": fingerprint,
		}).Error
	})
	if err != nil {
		return nil, err
	}
	return resolutionView(decision), nil
}
