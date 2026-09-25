package budget

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"sort"
	"time"

	"github.com/gatemux-dev/gatemux/internal/providers"
	"github.com/gatemux-dev/gatemux/internal/store"
	"github.com/jackc/pgx/v5"
)

type Target struct {
	ProviderType  string
	UpstreamModel string
}

type AdmissionRequest struct {
	RequestID        string
	Team             *store.Team
	User             *store.User
	ServiceAccount   *store.ServiceAccount
	Key              *store.VirtualKey
	Customer         *store.Customer
	Alias            string
	PromptTokens     int
	CompletionTokens int
	Targets          []Target
}

type Reservation struct {
	RequestID          string
	EstimatedCostCents int64
}

type ExceededError struct {
	Scope      string
	LimitCents int64
	UsedCents  int64
	NeedCents  int64
}

func (e *ExceededError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%s budget exceeded", e.Scope)
}

type PricingUnavailableError struct {
	ProviderType  string
	UpstreamModel string
}

func (e *PricingUnavailableError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("pricing unavailable for %s/%s", e.ProviderType, e.UpstreamModel)
}

type Service struct {
	Store *store.Store
}

func New(store *store.Store) *Service {
	return &Service{Store: store}
}

func (s *Service) Admit(ctx context.Context, req AdmissionRequest) (*Reservation, error) {
	if s == nil || s.Store == nil || req.Team == nil || req.RequestID == "" || req.Alias == "" {
		return nil, nil
	}

	teamBudget := req.Team.UsdLimitCents != nil && *req.Team.UsdLimitCents > 0
	userBudget := req.User != nil && req.User.UsdLimitCents != nil && *req.User.UsdLimitCents > 0
	saBudget := req.ServiceAccount != nil && req.ServiceAccount.UsdLimitCents != nil && *req.ServiceAccount.UsdLimitCents > 0
	keyBudget := req.Key != nil && req.Key.ScopedUsdLimitCents != nil && *req.Key.ScopedUsdLimitCents > 0
	customerBudget := req.Customer != nil && req.Customer.UsdLimitCents != nil
	if req.Customer != nil && (req.Customer.TeamID != req.Team.ID || req.Customer.ID <= 0 && customerBudget) {
		return nil, fmt.Errorf("invalid customer budget identity")
	}
	if !teamBudget && !userBudget && !saBudget && !keyBudget && !customerBudget {
		return nil, nil
	}

	estimatedCost, err := s.maxEstimatedCost(ctx, req.PromptTokens, req.CompletionTokens, req.Targets)
	if err != nil {
		return nil, err
	}

	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	lockKeys := []int64{teamLockKey(req.Team.ID)}
	if userBudget {
		lockKeys = append(lockKeys, userLockKey(req.User.ID))
	}
	if saBudget {
		lockKeys = append(lockKeys, serviceAccountLockKey(req.ServiceAccount.ID))
	}
	if keyBudget {
		lockKeys = append(lockKeys, keyLockKey(req.Key.ID))
	}
	if customerBudget {
		lockKeys = append(lockKeys, customerLockKey(req.Customer.ID))
	}
	sort.Slice(lockKeys, func(i, j int) bool { return lockKeys[i] < lockKeys[j] })
	for _, key := range lockKeys {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, key); err != nil {
			return nil, fmt.Errorf("acquire budget lock: %w", err)
		}
	}

	now := time.Now().UTC()
	if teamBudget {
		start, end := budgetWindow(now, req.Team.Period)
		used, err := budgetUsedForTeam(ctx, tx, req.Team.ID, start, end)
		if err != nil {
			return nil, err
		}
		if wouldExceed(used, estimatedCost, *req.Team.UsdLimitCents) {
			return nil, &ExceededError{
				Scope:      "team",
				LimitCents: *req.Team.UsdLimitCents,
				UsedCents:  used,
				NeedCents:  estimatedCost,
			}
		}
	}
	if userBudget {
		start, end := budgetWindow(now, req.User.Period)
		used, err := budgetUsedForUser(ctx, tx, req.User.ID, start, end)
		if err != nil {
			return nil, err
		}
		if wouldExceed(used, estimatedCost, *req.User.UsdLimitCents) {
			return nil, &ExceededError{
				Scope:      "user",
				LimitCents: *req.User.UsdLimitCents,
				UsedCents:  used,
				NeedCents:  estimatedCost,
			}
		}
	}
	if saBudget {
		start, end := budgetWindow(now, req.ServiceAccount.Period)
		used, err := budgetUsedForServiceAccount(ctx, tx, req.ServiceAccount.ID, start, end)
		if err != nil {
			return nil, err
		}
		if wouldExceed(used, estimatedCost, *req.ServiceAccount.UsdLimitCents) {
			return nil, &ExceededError{
				Scope:      "service_account",
				LimitCents: *req.ServiceAccount.UsdLimitCents,
				UsedCents:  used,
				NeedCents:  estimatedCost,
			}
		}
	}
	if keyBudget {
		// Key budgets share the team's period — keeps the windowing
		// straightforward; per-key custom periods can come later if asked.
		start, end := budgetWindow(now, req.Team.Period)
		used, err := budgetUsedForKey(ctx, tx, req.Key.ID, start, end)
		if err != nil {
			return nil, err
		}
		if wouldExceed(used, estimatedCost, *req.Key.ScopedUsdLimitCents) {
			return nil, &ExceededError{
				Scope:      "key",
				LimitCents: *req.Key.ScopedUsdLimitCents,
				UsedCents:  used,
				NeedCents:  estimatedCost,
			}
		}
	}

	if customerBudget {
		start, end := budgetWindow(now, req.Customer.Period)
		used, err := budgetUsedForCustomer(ctx, tx, req.Customer.ID, start, end)
		if err != nil {
			return nil, err
		}
		if wouldExceed(used, estimatedCost, *req.Customer.UsdLimitCents) {
			return nil, &ExceededError{Scope: "customer", LimitCents: *req.Customer.UsdLimitCents, UsedCents: used, NeedCents: estimatedCost}
		}
	}
	var customerID *int64
	if req.Customer != nil && req.Customer.ID > 0 {
		customerID = &req.Customer.ID
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO budget_reservations (request_id, team_id, user_id, service_account_id, key_id, alias, estimated_cost_cents, settled_cost_cents, status, customer_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, 0, 'reserved', $8)
	`, req.RequestID, req.Team.ID, nullableUserID(req.User), nullableServiceAccountID(req.ServiceAccount), nullableKeyID(req.Key), req.Alias, estimatedCost, customerID); err != nil {
		return nil, fmt.Errorf("insert budget reservation: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &Reservation{RequestID: req.RequestID, EstimatedCostCents: estimatedCost}, nil
}

func (s *Service) ComputeActualCost(ctx context.Context, providerType, upstreamModel string, promptTokens, completionTokens int) (int64, error) {
	return s.ComputeUsageCost(ctx, providerType, upstreamModel, providers.Usage{PromptTokens: promptTokens, CompletionTokens: completionTokens})
}

func (s *Service) ComputeUsageCost(ctx context.Context, providerType, upstreamModel string, usage providers.Usage) (int64, error) {
	if s == nil || s.Store == nil || providerType == "" || upstreamModel == "" {
		return 0, nil
	}
	pricing, err := s.Store.GetCurrentPricing(ctx, providerType, upstreamModel)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return 0, &PricingUnavailableError{ProviderType: providerType, UpstreamModel: upstreamModel}
		}
		return 0, err
	}
	return CostUsage(pricing, usage)
}

func (s *Service) Settle(ctx context.Context, requestID string, costCents int64) error {
	if costCents < 0 {
		return fmt.Errorf("settled cost must not be negative")
	}
	if s == nil || s.Store == nil || requestID == "" {
		return nil
	}
	tag, err := s.Store.Pool.Exec(ctx, `
		UPDATE budget_reservations
		SET settled_cost_cents = $2, status = 'settled', settled_at = NOW()
		WHERE request_id = $1 AND status = 'reserved'
	`, requestID, costCents)
	if err != nil {
		return fmt.Errorf("settle budget reservation: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil
	}
	return nil
}

// SettleEstimated conservatively reconciles an interrupted request whose actual
// usage cannot be recovered. Idempotent: a successful settlement is never changed.
// This releases the reservation state without inventing zero provider charges.
func (s *Service) SettleEstimated(ctx context.Context, requestID string) (int64, error) {
	if s == nil || s.Store == nil || requestID == "" {
		return 0, nil
	}
	var cost int64
	err := s.Store.Pool.QueryRow(ctx, `
		UPDATE budget_reservations SET settled_cost_cents=estimated_cost_cents,
		status='settled', settled_at=NOW()
		WHERE request_id=$1 AND status='reserved' RETURNING settled_cost_cents
	`, requestID).Scan(&cost)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	return cost, err
}

func (s *Service) maxEstimatedCost(ctx context.Context, promptTokens, completionTokens int, targets []Target) (int64, error) {
	if len(targets) == 0 {
		return 0, &PricingUnavailableError{}
	}
	var maxCost int64
	for _, target := range targets {
		pricing, err := s.Store.GetCurrentPricing(ctx, target.ProviderType, target.UpstreamModel)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return 0, &PricingUnavailableError{ProviderType: target.ProviderType, UpstreamModel: target.UpstreamModel}
			}
			return 0, err
		}
		cost, err := estimatedTierCost(pricing, promptTokens, completionTokens)
		if err != nil {
			return 0, err
		}
		if cost > maxCost {
			maxCost = cost
		}
	}
	return maxCost, nil
}

func estimateCostCents(pricing *store.Pricing, promptTokens, completionTokens int) int64 {
	if pricing == nil {
		return 0
	}
	cost, _ := estimatedTierCost(pricing, promptTokens, completionTokens)
	return cost // legacy test helper; request paths propagate overflow errors
}

func budgetWindow(now time.Time, period string) (time.Time, time.Time) {
	now = now.UTC()
	switch period {
	case "day":
		start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
		return start, start.Add(24 * time.Hour)
	default:
		start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
		return start, start.AddDate(0, 1, 0)
	}
}

func budgetUsedForTeam(ctx context.Context, tx customerBudgetReader, teamID int64, from, to time.Time) (int64, error) {
	return budgetUsedForScope(ctx, tx, "team_id", teamID, from, to)
}

func budgetUsedForUser(ctx context.Context, tx customerBudgetReader, userID int64, from, to time.Time) (int64, error) {
	return budgetUsedForScope(ctx, tx, "user_id", userID, from, to)
}

func budgetUsedForServiceAccount(ctx context.Context, tx customerBudgetReader, saID int64, from, to time.Time) (int64, error) {
	return budgetUsedForScope(ctx, tx, "service_account_id", saID, from, to)
}

func serviceAccountLockKey(saID int64) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(fmt.Sprintf("sa:%d", saID)))
	return int64(h.Sum64())
}

func nullableServiceAccountID(sa *store.ServiceAccount) *int64 {
	if sa == nil {
		return nil
	}
	v := sa.ID
	return &v
}

func nullableKeyID(vk *store.VirtualKey) *int64 {
	if vk == nil || vk.ID <= 0 {
		return nil
	}
	v := vk.ID
	return &v
}

func wouldExceed(used, need, limit int64) bool {
	return used < 0 || need < 0 || limit < 0 || used > limit || need > limit-used
}

func customerLockKey(customerID int64) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(fmt.Sprintf("customer:%d", customerID)))
	return int64(h.Sum64())
}

type customerBudgetReader interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func budgetUsedForCustomer(ctx context.Context, tx customerBudgetReader, customerID int64, from, to time.Time) (int64, error) {
	return budgetUsedForScope(ctx, tx, "customer_id", customerID, from, to)
}

// Transactional daily totals include usage before budget activation and count
// matching reservations once, including active conservative estimates. A day
// window reads at most one row; a month reads at most 31, independent of traffic.
// The column is selected only from this closed internal set, never from input.
func budgetUsedForScope(ctx context.Context, tx customerBudgetReader, column string, id int64, from, to time.Time) (int64, error) {
	switch column {
	case "team_id", "user_id", "service_account_id", "key_id", "customer_id":
	default:
		return 0, fmt.Errorf("invalid budget scope")
	}
	var used int64
	scope := column[:len(column)-3] // closed set above; remove "_id"
	err := tx.QueryRow(ctx, `SELECT COALESCE(SUM(cost_cents),0) FROM budget_daily_totals
		WHERE scope=$1 AND subject_id=$2 AND day >= ($3::timestamptz AT TIME ZONE 'UTC')::date
		AND day < ($4::timestamptz AT TIME ZONE 'UTC')::date`, scope, id, from, to).Scan(&used)
	return used, err
}

func (s *Service) CustomerSummary(ctx context.Context, c *store.Customer) (*UserSummary, error) {
	start, end := budgetWindow(time.Now().UTC(), c.Period)
	used, err := budgetUsedForCustomer(ctx, s.Store.Pool, c.ID, start, end)
	if err != nil {
		return nil, err
	}
	return &UserSummary{LimitCents: c.UsdLimitCents, Period: c.Period, WindowStart: start, WindowEnd: end, UsedCents: used}, nil
}

// KeySummary uses the same UTC team window and aggregate as admission, including
// pending reservations and pre-cap usage. Clearing/changing the cap resets no data.
func (s *Service) KeySummary(ctx context.Context, key *store.VirtualKey, team *store.Team) (*UserSummary, error) {
	if key == nil || team == nil || key.TeamID != team.ID || key.ID <= 0 {
		return nil, errors.New("invalid key budget identity")
	}
	period := team.Period
	if period != "day" && period != "month" {
		period = "month"
	}
	start, end := budgetWindow(time.Now().UTC(), period)
	used, err := budgetUsedForKey(ctx, s.Store.Pool, key.ID, start, end)
	if err != nil {
		return nil, err
	}
	return &UserSummary{LimitCents: key.ScopedUsdLimitCents, Period: period, WindowStart: start, WindowEnd: end, UsedCents: used}, nil
}

func teamLockKey(teamID int64) int64 {
	return 1_000_000_000 + teamID
}

func userLockKey(userID int64) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(fmt.Sprintf("user:%d", userID)))
	return int64(h.Sum64())
}

func keyLockKey(keyID int64) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(fmt.Sprintf("key:%d", keyID)))
	return int64(h.Sum64())
}

// budgetUsedForKey is the sum of reserved + settled cents on this key's
// reservations within the window. Mirrors the team/user variants — reads
// from budget_reservations so concurrent admissions see each other's
// in-flight reservations and can't both pass against an empty bucket.
func budgetUsedForKey(ctx context.Context, tx customerBudgetReader, keyID int64, from, to time.Time) (int64, error) {
	return budgetUsedForScope(ctx, tx, "key_id", keyID, from, to)
}

// UserSummary describes a user's effective budget state in their current
// period window: the limit (if any), the window boundaries, and how much
// has already been reserved or settled against budget_reservations. The
// "used" figure mirrors the value the admission check compares against,
// so the dashboard agrees with what would actually be denied.
type UserSummary struct {
	LimitCents  *int64    `json:"limit_cents,omitempty"`
	Period      string    `json:"period"`
	WindowStart time.Time `json:"window_start"`
	WindowEnd   time.Time `json:"window_end"`
	UsedCents   int64     `json:"used_cents"`
}

func (s *Service) UserSummary(ctx context.Context, user *store.User) (*UserSummary, error) {
	if s == nil || s.Store == nil || user == nil {
		return nil, nil
	}
	period := user.Period
	if period != "day" && period != "month" {
		period = "month"
	}
	start, end := budgetWindow(time.Now().UTC(), period)
	used, err := budgetUsedForUser(ctx, s.Store.Pool, user.ID, start, end)
	if err != nil {
		return nil, fmt.Errorf("sum user budget usage: %w", err)
	}
	return &UserSummary{
		LimitCents:  user.UsdLimitCents,
		Period:      period,
		WindowStart: start,
		WindowEnd:   end,
		UsedCents:   used,
	}, nil
}

func nullableUserID(user *store.User) *int64 {
	if user == nil {
		return nil
	}
	return &user.ID
}
