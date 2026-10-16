// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pricing

// The pricing catalogue: the price levels, the price profiles, the product
// prices, the vendor levels and costs, and the customer contracts (ADR 0006
// sections 5.2, 5.5 and 5.6). Prices are scale 4 values returned as stored
// and never rounded (R4.2); the stocking unit hold keeps every price unit at
// the product's stocking unit until C3-2B.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Event types the catalogue writes to the outbox.
const (
	EventContractCreated   = "contract.created"
	EventContractUpdated   = "contract.updated"
	EventContractDeleted   = "contract.deleted"
	EventLevelCreated      = "price_level.created"
	EventLevelUpdated      = "price_level.updated"
	EventProfileCreated    = "price_profile.created"
	EventProfileUpdated    = "price_profile.updated"
	EventVendorCostCreated = "vendor_cost.created"
	EventVendorCostUpdated = "vendor_cost.updated"
)

// PriceLevel is one dealer price level on the wire (ADR 0006 section 5.2).
type PriceLevel struct {
	ID            uuid.UUID       `json:"id"`
	Code          string          `json:"code"`
	Name          string          `json:"name"`
	Basis         string          `json:"basis"`
	AdjustPercent httpx.Quantity  `json:"adjust_percent"`
	Position      int             `json:"position"`
	IsActive      bool            `json:"is_active"`
	Revision      int64           `json:"revision"`
	CreatedAt     httpx.Timestamp `json:"created_at"`
	UpdatedAt     httpx.Timestamp `json:"updated_at"`
}

// PriceProfile is one shareable pricing program: the level it names.
type PriceProfile struct {
	ID           uuid.UUID       `json:"id"`
	Code         string          `json:"code"`
	Name         string          `json:"name"`
	PriceLevelID uuid.UUID       `json:"price_level_id"`
	IsActive     bool            `json:"is_active"`
	Revision     int64           `json:"revision"`
	CreatedAt    httpx.Timestamp `json:"created_at"`
	UpdatedAt    httpx.Timestamp `json:"updated_at"`
}

// VendorLevel is one vendor buying program.
type VendorLevel struct {
	ID        uuid.UUID       `json:"id"`
	VendorID  uuid.UUID       `json:"vendor_id"`
	Code      string          `json:"code"`
	Name      string          `json:"name"`
	IsDefault bool            `json:"is_default"`
	IsActive  bool            `json:"is_active"`
	Revision  int64           `json:"revision"`
	CreatedAt httpx.Timestamp `json:"created_at"`
	UpdatedAt httpx.Timestamp `json:"updated_at"`
}

// VendorCost is one vendor cost row on the wire.
type VendorCost struct {
	// costText is the scan buffer the repository reads the scaled column
	// into before parsing it exactly.
	costText string

	ID            uuid.UUID       `json:"id"`
	VendorID      uuid.UUID       `json:"vendor_id"`
	VendorLevelID uuid.UUID       `json:"vendor_price_level_id"`
	ProductID     uuid.UUID       `json:"product_id"`
	BranchID      *uuid.UUID      `json:"branch_id"`
	PurchaseUOM   string          `json:"purchase_uom"`
	UnitCost      httpx.Price     `json:"unit_cost_ten_thousandths"`
	VendorSKU     *string         `json:"vendor_sku"`
	EffectiveFrom string          `json:"effective_from"`
	EffectiveTo   *string         `json:"effective_to"`
	Revision      int64           `json:"revision"`
	CreatedAt     httpx.Timestamp `json:"created_at"`
	UpdatedAt     httpx.Timestamp `json:"updated_at"`
}

// ErrCatalogDuplicate names the catalogue's unique key conflicts.
var ErrCatalogDuplicate = errors.New("duplicate")

// CatalogRepository is the catalogue's write and read store.
type CatalogRepository interface {
	// Levels
	ListLevels(ctx context.Context, after *CatalogCursor, limit int) ([]PriceLevel, error)
	GetLevel(ctx context.Context, id uuid.UUID) (*PriceLevel, error)
	InsertLevel(ctx context.Context, l *PriceLevel) error
	UpdateLevel(ctx context.Context, l *PriceLevel, revision int64) error
	LevelByCode(ctx context.Context, code string) (*PriceLevel, error)

	// Profiles
	ListProfiles(ctx context.Context, after *CatalogCursor, limit int) ([]PriceProfile, error)
	GetProfile(ctx context.Context, id uuid.UUID) (*PriceProfile, error)
	InsertProfile(ctx context.Context, p *PriceProfile) error
	UpdateProfile(ctx context.Context, p *PriceProfile, revision int64) error

	// Product prices: the branch slice reads and the replace. The reads take
	// the wall beside the filter: the wall is the caller's context branch, a
	// nil wall meaning no wall (every row).
	ProductPrices(ctx context.Context, productID uuid.UUID, branch, wall *uuid.UUID) ([]ProductPrice, error)
	ReplaceProductPrices(ctx context.Context, productID uuid.UUID, branch *uuid.UUID, productRevision int64, rows []ProductPrice) error

	// Vendor levels and costs
	ListVendorLevels(ctx context.Context, vendorID *uuid.UUID, after *CatalogCursor, limit int) ([]VendorLevel, error)
	GetVendorLevel(ctx context.Context, id uuid.UUID) (*VendorLevel, error)
	InsertVendorLevel(ctx context.Context, v *VendorLevel) error
	UpdateVendorLevel(ctx context.Context, v *VendorLevel, revision int64) error
	ListVendorCosts(ctx context.Context, vendorID, productID, branch, wall *uuid.UUID, after *CatalogCursor, limit int) ([]VendorCost, error)
	GetVendorCost(ctx context.Context, id uuid.UUID) (*VendorCost, error)
	InsertVendorCost(ctx context.Context, c *VendorCost) error
	UpdateVendorCost(ctx context.Context, c *VendorCost, revision int64) error

	// Contracts
	ListContracts(ctx context.Context, customerID, productID, branch, wall *uuid.UUID, after *CatalogCursor, limit int) ([]CustomerContract, error)
	GetContractRow(ctx context.Context, id uuid.UUID) (*CustomerContract, error)
	LockContract(ctx context.Context, id uuid.UUID) (*CustomerContract, error)
	InsertContract(ctx context.Context, c *CustomerContract) error
	UpdateContract(ctx context.Context, c *CustomerContract, revision int64) error
	DeleteContract(ctx context.Context, id uuid.UUID) error

	// The product facts the writes hold prices against: the stocking unit and
	// the price unit, the revision.
	ProductPricingFacts(ctx context.Context, productID uuid.UUID) (*ProductPricingFacts, error)
	ProductRevision(ctx context.Context, productID uuid.UUID) (int64, error)
}

// CatalogCursor is the keyset position: created_at then id.
type CatalogCursor struct {
	CreatedAt time.Time
	ID        uuid.UUID
}

// CatalogService is the catalogue's service: the writes, their audit rows
// and their events, one transactional fact each.
type CatalogService struct {
	repo   CatalogRepository
	tx     TxRunner
	events EventRecorder
	audits AuditLogger
}

// NewCatalogService builds the catalogue service.
func NewCatalogService(repo CatalogRepository) *CatalogService {
	return &CatalogService{repo: repo}
}

// WithTxRunner wires the transaction boundary.
func (s *CatalogService) WithTxRunner(tx TxRunner) *CatalogService {
	s.tx = tx
	return s
}

// WithOutbox wires the event recorder.
func (s *CatalogService) WithOutbox(events EventRecorder) *CatalogService {
	s.events = events
	return s
}

// WithAudit wires the audit rows.
func (s *CatalogService) WithAudit(a AuditLogger) *CatalogService {
	s.audits = a
	return s
}

func (s *CatalogService) inTx(ctx context.Context, fn func(ctx context.Context) error) error {
	if s.tx == nil {
		return fn(ctx)
	}
	return s.tx.RunInTx(ctx, fn)
}

// record writes the event last in the transaction.
func (s *CatalogService) record(ctx context.Context, eventType, entityType string, entityID uuid.UUID, data map[string]any) error {
	if s.events == nil {
		return nil
	}
	raw, err := jsonMarshal(data)
	if err != nil {
		return err
	}
	return s.events.Write(ctx, outbox.Event{Type: eventType, EntityType: entityType, EntityID: entityID, Data: raw})
}

func (s *CatalogService) audit(ctx context.Context, action, entityType string, entityID uuid.UUID, changes map[string]any) error {
	if s.audits == nil {
		return nil
	}
	return s.audits.Log(ctx, AuditEntry{Action: action, EntityType: entityType, EntityID: entityID.String(), Changes: changes})
}

// CreateLevel writes a price level with its event.
func (s *CatalogService) CreateLevel(ctx context.Context, l *PriceLevel) error {
	return s.inTx(ctx, func(ctx context.Context) error {
		if err := s.repo.InsertLevel(ctx, l); err != nil {
			return err
		}
		return s.record(ctx, EventLevelCreated, "price_level", l.ID, map[string]any{
			"code": l.Code, "name": l.Name, "basis": l.Basis, "revision": l.Revision,
		})
	})
}

// UpdateLevel writes a level at its revision with its event.
func (s *CatalogService) UpdateLevel(ctx context.Context, l *PriceLevel, revision int64) error {
	return s.inTx(ctx, func(ctx context.Context) error {
		if err := s.repo.UpdateLevel(ctx, l, revision); err != nil {
			return err
		}
		if err := s.audit(ctx, "price_level.updated", "price_level", l.ID, map[string]any{
			"code": l.Code, "name": l.Name, "basis": l.Basis, "adjust_percent": l.AdjustPercent.WireString(), "revision": l.Revision,
		}); err != nil {
			return err
		}
		return s.record(ctx, EventLevelUpdated, "price_level", l.ID, map[string]any{"code": l.Code, "revision": l.Revision})
	})
}

// CreateProfile writes a price profile with its event.
func (s *CatalogService) CreateProfile(ctx context.Context, p *PriceProfile) error {
	return s.inTx(ctx, func(ctx context.Context) error {
		if err := s.repo.InsertProfile(ctx, p); err != nil {
			return err
		}
		return s.record(ctx, EventProfileCreated, "price_profile", p.ID, map[string]any{
			"code": p.Code, "name": p.Name, "price_level_id": p.PriceLevelID, "revision": p.Revision,
		})
	})
}

// UpdateProfile writes a profile at its revision with its event.
func (s *CatalogService) UpdateProfile(ctx context.Context, p *PriceProfile, revision int64) error {
	return s.inTx(ctx, func(ctx context.Context) error {
		if err := s.repo.UpdateProfile(ctx, p, revision); err != nil {
			return err
		}
		if err := s.audit(ctx, "price_profile.updated", "price_profile", p.ID, map[string]any{
			"code": p.Code, "name": p.Name, "revision": p.Revision,
		}); err != nil {
			return err
		}
		return s.record(ctx, EventProfileUpdated, "price_profile", p.ID, map[string]any{"code": p.Code, "revision": p.Revision})
	})
}

// ReplaceProductPrices replaces one branch's slice of the product's price
// list at the product's revision, in one transaction under the product row's
// lock, with the product.updated event naming the prices part.
func (s *CatalogService) ReplaceProductPrices(ctx context.Context, productID uuid.UUID, branch *uuid.UUID, productRevision int64, rows []ProductPrice) error {
	return s.inTx(ctx, func(ctx context.Context) error {
		if err := s.repo.ReplaceProductPrices(ctx, productID, branch, productRevision, rows); err != nil {
			return err
		}
		if err := s.audit(ctx, "product.prices_replaced", "product", productID, map[string]any{
			"branch_id": branch, "rows": len(rows),
		}); err != nil {
			return err
		}
		return s.record(ctx, "product.updated", "product", productID, map[string]any{"parts": []string{"prices"}})
	})
}

// CreateVendorLevel writes a vendor buying program.
func (s *CatalogService) CreateVendorLevel(ctx context.Context, v *VendorLevel) error {
	return s.inTx(ctx, func(ctx context.Context) error {
		return s.repo.InsertVendorLevel(ctx, v)
	})
}

// UpdateVendorLevel writes a vendor level at its revision.
func (s *CatalogService) UpdateVendorLevel(ctx context.Context, v *VendorLevel, revision int64) error {
	return s.inTx(ctx, func(ctx context.Context) error {
		return s.repo.UpdateVendorLevel(ctx, v, revision)
	})
}

// CreateVendorCost writes a vendor cost with its event. The cost's level
// must name the same vendor the row does.
func (s *CatalogService) CreateVendorCost(ctx context.Context, c *VendorCost) error {
	return s.inTx(ctx, func(ctx context.Context) error {
		if err := s.sameVendor(ctx, c.VendorID, c.VendorLevelID); err != nil {
			return err
		}
		if err := s.repo.InsertVendorCost(ctx, c); err != nil {
			return err
		}
		return s.record(ctx, EventVendorCostCreated, "vendor_cost", c.ID, map[string]any{
			"vendor_id": c.VendorID, "product_id": c.ProductID, "revision": c.Revision,
		})
	})
}

// UpdateVendorCost writes a vendor cost at its revision with its event.
func (s *CatalogService) UpdateVendorCost(ctx context.Context, c *VendorCost, revision int64) error {
	return s.inTx(ctx, func(ctx context.Context) error {
		if err := s.sameVendor(ctx, c.VendorID, c.VendorLevelID); err != nil {
			return err
		}
		if err := s.repo.UpdateVendorCost(ctx, c, revision); err != nil {
			return err
		}
		if err := s.audit(ctx, "vendor_cost.updated", "vendor_cost", c.ID, map[string]any{
			"unit_cost_ten_thousandths": int64(c.UnitCost), "revision": c.Revision,
		}); err != nil {
			return err
		}
		return s.record(ctx, EventVendorCostUpdated, "vendor_cost", c.ID, map[string]any{"revision": c.Revision})
	})
}

func (s *CatalogService) sameVendor(ctx context.Context, vendorID, levelID uuid.UUID) error {
	level, err := s.repo.GetVendorLevel(ctx, levelID)
	if err != nil {
		return err
	}
	if level == nil {
		return httpx.BadRequest("vendor_price_level_id does not name an existing row",
			httpx.FieldError{Field: "vendor_price_level_id", Message: "no such row"})
	}
	if level.VendorID != vendorID {
		return httpx.BadRequest("the vendor price level names another vendor",
			httpx.FieldError{Field: "vendor_price_level_id", Message: "names another vendor"})
	}
	return nil
}

// CreateContract writes a contract with its event. The route's create does
// not upsert: a second contract for the same customer, product and branch is
// a 409 duplicate.
func (s *CatalogService) CreateContract(ctx context.Context, c *CustomerContract) error {
	return s.inTx(ctx, func(ctx context.Context) error {
		if err := s.repo.InsertContract(ctx, c); err != nil {
			return err
		}
		return s.record(ctx, EventContractCreated, "contract", c.ID, map[string]any{
			"customer_id": c.CustomerID, "product_id": c.ProductID, "branch_id": c.BranchID, "revision": c.Revision,
		})
	})
}

// UpdateContract writes a contract at its revision with its event.
func (s *CatalogService) UpdateContract(ctx context.Context, c *CustomerContract, revision int64) error {
	return s.inTx(ctx, func(ctx context.Context) error {
		if err := s.repo.UpdateContract(ctx, c, revision); err != nil {
			return err
		}
		if err := s.audit(ctx, "contract.updated", "contract", c.ID, map[string]any{
			"contract_price_ten_thousandths": int64(c.ContractPrice), "revision": c.Revision,
		}); err != nil {
			return err
		}
		return s.record(ctx, EventContractUpdated, "contract", c.ID, map[string]any{"revision": c.Revision})
	})
}

// DeleteContract deletes a contract outright (it is an agreement, not a
// document) with an audit row carrying the deleted values and the event.
func (s *CatalogService) DeleteContract(ctx context.Context, id uuid.UUID, revision int64) error {
	return s.inTx(ctx, func(ctx context.Context) error {
		old, err := s.repo.LockContract(ctx, id)
		if err != nil {
			return err
		}
		if old == nil {
			return httpx.NotFound("no such contract")
		}
		if err := httpx.CheckRevision(old.Revision, "", &revision); err != nil {
			return err
		}
		if err := s.repo.DeleteContract(ctx, id); err != nil {
			return err
		}
		if err := s.audit(ctx, "contract.deleted", "contract", id, map[string]any{
			"customer_id": old.CustomerID, "product_id": old.ProductID, "branch_id": old.BranchID,
			"contract_price_ten_thousandths": int64(old.ContractPrice), "price_uom": old.PriceUOM,
		}); err != nil {
			return err
		}
		return s.record(ctx, EventContractDeleted, "contract", id, nil)
	})
}

// PostgresCatalogRepository reads and writes the catalogue tables.
type PostgresCatalogRepository struct {
	db *database.DB
}

// NewCatalogRepository builds the catalogue store.
func NewCatalogRepository(db *database.DB) *PostgresCatalogRepository {
	return &PostgresCatalogRepository{db: db}
}

const levelColumns = `id, code, name, basis, adjust_percent::text, position, is_active, revision, created_at, updated_at`

func scanLevel(row pgx.Row) (*PriceLevel, error) {
	var l PriceLevel
	var created, updated time.Time
	err := row.Scan(&l.ID, &l.Code, &l.Name, &l.Basis, &l.AdjustPercent, &l.Position, &l.IsActive, &l.Revision, &created, &updated)
	if err != nil {
		return nil, err
	}
	l.CreatedAt, l.UpdatedAt = httpx.TimestampOf(created), httpx.TimestampOf(updated)
	return &l, nil
}

func (r *PostgresCatalogRepository) ListLevels(ctx context.Context, after *CatalogCursor, limit int) ([]PriceLevel, error) {
	q := `SELECT ` + levelColumns + ` FROM price_levels`
	args := []any{}
	if after != nil {
		q += ` WHERE (created_at, id) < ($1, $2)`
		args = append(args, after.CreatedAt, after.ID)
	}
	q += ` ORDER BY created_at DESC, id DESC`
	args = append(args, limit)
	q += fmt.Sprintf(` LIMIT $%d`, len(args))
	rows, err := r.db.GetExecutor(ctx).Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list price levels: %w", err)
	}
	defer rows.Close()
	var out []PriceLevel
	for rows.Next() {
		l, err := scanLevel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *l)
	}
	return out, rows.Err()
}

func (r *PostgresCatalogRepository) GetLevel(ctx context.Context, id uuid.UUID) (*PriceLevel, error) {
	l, err := scanLevel(r.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT `+levelColumns+` FROM price_levels WHERE id = $1`, id))
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return l, nil
}

func (r *PostgresCatalogRepository) LevelByCode(ctx context.Context, code string) (*PriceLevel, error) {
	return scanLevel(r.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT `+levelColumns+` FROM price_levels WHERE code = $1`, code))
}

func (r *PostgresCatalogRepository) InsertLevel(ctx context.Context, l *PriceLevel) error {
	if l.ID == uuid.Nil {
		l.ID = uuid.New()
	}
	now := time.Now()
	l.CreatedAt, l.UpdatedAt = httpx.TimestampOf(now), httpx.TimestampOf(now)
	l.Revision = 1
	var position any
	if l.Position != 0 {
		position = l.Position
	}
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `
		INSERT INTO price_levels (id, code, name, basis, adjust_percent, position, is_active)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING revision, created_at, updated_at`,
		l.ID, l.Code, l.Name, l.Basis, l.AdjustPercent.DecimalString(), position, l.IsActive).
		Scan(&l.Revision, &l.CreatedAt.Time, &l.UpdatedAt.Time)
	return catalogWriteErr(err, "price level")
}

func (r *PostgresCatalogRepository) UpdateLevel(ctx context.Context, l *PriceLevel, revision int64) error {
	tag, err := r.db.GetExecutor(ctx).Exec(ctx, `
		UPDATE price_levels
		SET name = $2, basis = $3, adjust_percent = $4, is_active = $5,
		    revision = revision + 1, updated_at = NOW()
		WHERE id = $1 AND revision = $6`,
		l.ID, l.Name, l.Basis, l.AdjustPercent.DecimalString(), l.IsActive, revision)
	if err != nil {
		return catalogWriteErr(err, "price level")
	}
	return staleOrMissing(ctx, tag, l.ID, r.db, "price_levels")
}

func staleOrMissing(ctx context.Context, tag pgconnCommandTag, id uuid.UUID, db *database.DB, table string) error {
	if tag.RowsAffected() == 1 {
		return nil
	}
	var exists bool
	if err := db.GetExecutor(ctx).
		QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM `+table+` WHERE id = $1)`, id).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return ErrStaleRevision
	}
	return ErrNotFound
}

const profileColumns = `id, code, name, price_level_id, is_active, revision, created_at, updated_at`

func scanProfile(row pgx.Row) (*PriceProfile, error) {
	var p PriceProfile
	var created, updated time.Time
	err := row.Scan(&p.ID, &p.Code, &p.Name, &p.PriceLevelID, &p.IsActive, &p.Revision, &created, &updated)
	if err != nil {
		return nil, err
	}
	p.CreatedAt, p.UpdatedAt = httpx.TimestampOf(created), httpx.TimestampOf(updated)
	return &p, nil
}

func (r *PostgresCatalogRepository) ListProfiles(ctx context.Context, after *CatalogCursor, limit int) ([]PriceProfile, error) {
	q := `SELECT ` + profileColumns + ` FROM price_profiles`
	args := []any{}
	if after != nil {
		q += ` WHERE (created_at, id) < ($1, $2)`
		args = append(args, after.CreatedAt, after.ID)
	}
	q += ` ORDER BY created_at DESC, id DESC`
	args = append(args, limit)
	q += fmt.Sprintf(` LIMIT $%d`, len(args))
	rows, err := r.db.GetExecutor(ctx).Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list price profiles: %w", err)
	}
	defer rows.Close()
	var out []PriceProfile
	for rows.Next() {
		p, err := scanProfile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

func (r *PostgresCatalogRepository) GetProfile(ctx context.Context, id uuid.UUID) (*PriceProfile, error) {
	p, err := scanProfile(r.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT `+profileColumns+` FROM price_profiles WHERE id = $1`, id))
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return p, nil
}

func (r *PostgresCatalogRepository) InsertProfile(ctx context.Context, p *PriceProfile) error {
	if p.ID == uuid.Nil {
		p.ID = uuid.New()
	}
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `
		INSERT INTO price_profiles (id, code, name, price_level_id, is_active)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING revision, created_at, updated_at`,
		p.ID, p.Code, p.Name, p.PriceLevelID, p.IsActive).
		Scan(&p.Revision, &p.CreatedAt.Time, &p.UpdatedAt.Time)
	return catalogWriteErr(err, "price profile")
}

func (r *PostgresCatalogRepository) UpdateProfile(ctx context.Context, p *PriceProfile, revision int64) error {
	tag, err := r.db.GetExecutor(ctx).Exec(ctx, `
		UPDATE price_profiles
		SET name = $2, price_level_id = $3, is_active = $4, revision = revision + 1, updated_at = NOW()
		WHERE id = $1 AND revision = $5`,
		p.ID, p.Name, p.PriceLevelID, p.IsActive, revision)
	if err != nil {
		return catalogWriteErr(err, "price profile")
	}
	return staleOrMissing(ctx, tag, p.ID, r.db, "price_profiles")
}

func (r *PostgresCatalogRepository) ProductPrices(ctx context.Context, productID uuid.UUID, branch, wall *uuid.UUID) ([]ProductPrice, error) {
	q := `
		SELECT id, product_id, price_level_id, branch_id, price_uom, unit_price::text, revision
		FROM product_prices
		WHERE product_id = $1
		  AND ($2::uuid IS NULL OR branch_id IS NOT DISTINCT FROM $2)
		  AND ($3::uuid IS NULL OR branch_id = $3 OR branch_id IS NULL)
		ORDER BY price_level_id NULLS FIRST, branch_id NULLS FIRST`
	rows, err := r.db.GetExecutor(ctx).Query(ctx, q, productID, branch, wall)
	if err != nil {
		return nil, fmt.Errorf("list product prices: %w", err)
	}
	defer rows.Close()
	var out []ProductPrice
	for rows.Next() {
		var p ProductPrice
		if err := rows.Scan(&p.ID, &p.ProductID, &p.PriceLevelID, &p.BranchID, &p.PriceUOM, &p.priceText, &p.Revision); err != nil {
			return nil, err
		}
		if p.UnitPrice, err = httpx.ParsePrice(p.priceText); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ReplaceProductPrices replaces one branch's slice under the product row's
// lock: the DELETE and the inserts are one transactional fact with the
// product's revision check. The rows carry no branch of their own; the slice
// is the branch the query names.
func (r *PostgresCatalogRepository) ReplaceProductPrices(ctx context.Context, productID uuid.UUID, branch *uuid.UUID, productRevision int64, rows []ProductPrice) error {
	// Every statement runs on the context's executor: inside the service's
	// transaction it joins that transaction (one connection, one commit, the
	// audit and event rows share its fate); unwrapped it is the pool.
	exec := r.db.GetExecutor(ctx)

	var revision int64
	if err := exec.QueryRow(ctx, `SELECT revision FROM products WHERE id = $1 FOR UPDATE`, productID).Scan(&revision); err != nil {
		if err == pgx.ErrNoRows {
			return httpx.BadRequest("product_id does not name an existing row",
				httpx.FieldError{Field: "product_id", Message: "no such row"})
		}
		return err
	}
	if revision != productRevision {
		return ErrStaleRevision
	}
	if _, err := exec.Exec(ctx, `DELETE FROM product_prices WHERE product_id = $1 AND branch_id IS NOT DISTINCT FROM $2`, productID, branch); err != nil {
		return err
	}
	for i := range rows {
		rows[i].ID = uuid.New()
		rows[i].ProductID = productID
		rows[i].BranchID = branch
		rows[i].Revision = 1
		if _, err := exec.Exec(ctx, `
			INSERT INTO product_prices (id, product_id, price_level_id, branch_id, price_uom, unit_price)
			VALUES ($1, $2, $3, $4, $5, $6)`,
			rows[i].ID, productID, rows[i].PriceLevelID, branch, rows[i].PriceUOM, rows[i].UnitPrice.DecimalString()); err != nil {
			return catalogWriteErr(err, "product price")
		}
	}
	if _, err := exec.Exec(ctx, `UPDATE products SET revision = revision + 1, updated_at = NOW() WHERE id = $1`, productID); err != nil {
		return err
	}
	return nil
}

const vendorLevelColumns = `id, vendor_id, code, name, is_default, is_active, revision, created_at, updated_at`

func scanVendorLevel(row pgx.Row) (*VendorLevel, error) {
	var v VendorLevel
	var created, updated time.Time
	err := row.Scan(&v.ID, &v.VendorID, &v.Code, &v.Name, &v.IsDefault, &v.IsActive, &v.Revision, &created, &updated)
	if err != nil {
		return nil, err
	}
	v.CreatedAt, v.UpdatedAt = httpx.TimestampOf(created), httpx.TimestampOf(updated)
	return &v, nil
}

func (r *PostgresCatalogRepository) ListVendorLevels(ctx context.Context, vendorID *uuid.UUID, after *CatalogCursor, limit int) ([]VendorLevel, error) {
	q := `SELECT ` + vendorLevelColumns + ` FROM vendor_price_levels WHERE ($1::uuid IS NULL OR vendor_id = $1)`
	args := []any{vendorID}
	if after != nil {
		args = append(args, after.CreatedAt, after.ID)
		q += fmt.Sprintf(` AND (created_at, id) < ($%d, $%d)`, len(args)-1, len(args))
	}
	q += ` ORDER BY created_at DESC, id DESC`
	args = append(args, limit)
	q += fmt.Sprintf(` LIMIT $%d`, len(args))
	rows, err := r.db.GetExecutor(ctx).Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list vendor levels: %w", err)
	}
	defer rows.Close()
	var out []VendorLevel
	for rows.Next() {
		v, err := scanVendorLevel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *v)
	}
	return out, rows.Err()
}

func (r *PostgresCatalogRepository) GetVendorLevel(ctx context.Context, id uuid.UUID) (*VendorLevel, error) {
	v, err := scanVendorLevel(r.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT `+vendorLevelColumns+` FROM vendor_price_levels WHERE id = $1`, id))
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return v, nil
}

func (r *PostgresCatalogRepository) InsertVendorLevel(ctx context.Context, v *VendorLevel) error {
	if v.ID == uuid.Nil {
		v.ID = uuid.New()
	}
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `
		INSERT INTO vendor_price_levels (id, vendor_id, code, name, is_default, is_active)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING revision, created_at, updated_at`,
		v.ID, v.VendorID, v.Code, v.Name, v.IsDefault, v.IsActive).
		Scan(&v.Revision, &v.CreatedAt.Time, &v.UpdatedAt.Time)
	return catalogWriteErr(err, "vendor level")
}

func (r *PostgresCatalogRepository) UpdateVendorLevel(ctx context.Context, v *VendorLevel, revision int64) error {
	tag, err := r.db.GetExecutor(ctx).Exec(ctx, `
		UPDATE vendor_price_levels
		SET name = $2, is_default = $3, is_active = $4, revision = revision + 1, updated_at = NOW()
		WHERE id = $1 AND revision = $5`,
		v.ID, v.Name, v.IsDefault, v.IsActive, revision)
	if err != nil {
		return catalogWriteErr(err, "vendor level")
	}
	return staleOrMissing(ctx, tag, v.ID, r.db, "vendor_price_levels")
}

const vendorCostColumns = `id, vendor_id, vendor_price_level_id, product_id, branch_id, purchase_uom,
	unit_cost::text, vendor_sku, effective_from::text, effective_to::text, revision, created_at, updated_at`

func scanVendorCost(row interface{ Scan(dest ...any) error }) (*VendorCost, error) {
	var c VendorCost
	var created, updated time.Time
	if err := row.Scan(&c.ID, &c.VendorID, &c.VendorLevelID, &c.ProductID, &c.BranchID, &c.PurchaseUOM,
		&c.costText, &c.VendorSKU, &c.EffectiveFrom, &c.EffectiveTo, &c.Revision, &created, &updated); err != nil {
		return nil, err
	}
	var err error
	if c.UnitCost, err = httpx.ParsePrice(c.costText); err != nil {
		return nil, err
	}
	c.CreatedAt, c.UpdatedAt = httpx.TimestampOf(created), httpx.TimestampOf(updated)
	return &c, nil
}

func (r *PostgresCatalogRepository) ListVendorCosts(ctx context.Context, vendorID, productID, branch, wall *uuid.UUID, after *CatalogCursor, limit int) ([]VendorCost, error) {
	q := `SELECT ` + vendorCostColumns + ` FROM vendor_product_costs
		WHERE ($1::uuid IS NULL OR vendor_id = $1)
		  AND ($2::uuid IS NULL OR product_id = $2)
		  AND ($3::uuid IS NULL OR branch_id IS NOT DISTINCT FROM $3)
		  AND ($4::uuid IS NULL OR branch_id = $4 OR branch_id IS NULL)`
	args := []any{vendorID, productID, branch, wall}
	if after != nil {
		args = append(args, after.CreatedAt, after.ID)
		q += fmt.Sprintf(` AND (created_at, id) < ($%d, $%d)`, len(args)-1, len(args))
	}
	q += ` ORDER BY created_at DESC, id DESC`
	args = append(args, limit)
	q += fmt.Sprintf(` LIMIT $%d`, len(args))
	rows, err := r.db.GetExecutor(ctx).Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list vendor costs: %w", err)
	}
	defer rows.Close()
	var out []VendorCost
	for rows.Next() {
		c, err := scanVendorCost(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

func (r *PostgresCatalogRepository) GetVendorCost(ctx context.Context, id uuid.UUID) (*VendorCost, error) {
	c, err := scanVendorCost(r.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT `+vendorCostColumns+` FROM vendor_product_costs WHERE id = $1`, id))
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return c, nil
}

func (r *PostgresCatalogRepository) InsertVendorCost(ctx context.Context, c *VendorCost) error {
	if c.ID == uuid.Nil {
		c.ID = uuid.New()
	}
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `
		INSERT INTO vendor_product_costs (id, vendor_id, vendor_price_level_id, product_id, branch_id,
			purchase_uom, unit_cost, vendor_sku, effective_from, effective_to)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		RETURNING revision, created_at, updated_at`,
		c.ID, c.VendorID, c.VendorLevelID, c.ProductID, c.BranchID, c.PurchaseUOM,
		c.UnitCost.DecimalString(), c.VendorSKU, c.EffectiveFrom, c.EffectiveTo).
		Scan(&c.Revision, &c.CreatedAt.Time, &c.UpdatedAt.Time)
	return catalogWriteErr(err, "vendor cost")
}

func (r *PostgresCatalogRepository) UpdateVendorCost(ctx context.Context, c *VendorCost, revision int64) error {
	tag, err := r.db.GetExecutor(ctx).Exec(ctx, `
		UPDATE vendor_product_costs
		SET branch_id = $2, purchase_uom = $3, unit_cost = $4, vendor_sku = $5,
		    effective_from = $6, effective_to = $7, revision = revision + 1, updated_at = NOW()
		WHERE id = $1 AND revision = $8`,
		c.ID, c.BranchID, c.PurchaseUOM, c.UnitCost.DecimalString(), c.VendorSKU,
		c.EffectiveFrom, c.EffectiveTo, revision)
	if err != nil {
		return catalogWriteErr(err, "vendor cost")
	}
	return staleOrMissing(ctx, tag, c.ID, r.db, "vendor_product_costs")
}

const contractColumns = `id, customer_id, product_id, contract_price::text, price_uom, branch_id, revision, created_at, updated_at`

func scanContractRow(row interface{ Scan(dest ...any) error }) (*CustomerContract, error) {
	var c CustomerContract
	var price string
	var created, updated time.Time
	if err := row.Scan(&c.ID, &c.CustomerID, &c.ProductID, &price, &c.PriceUOM, &c.BranchID, &c.Revision, &created, &updated); err != nil {
		return nil, err
	}
	var err error
	if c.ContractPrice, err = httpx.ParsePrice(price); err != nil {
		return nil, err
	}
	c.CreatedAt, c.UpdatedAt = httpx.TimestampOf(created), httpx.TimestampOf(updated)
	return &c, nil
}

func (r *PostgresCatalogRepository) ListContracts(ctx context.Context, customerID, productID, branch, wall *uuid.UUID, after *CatalogCursor, limit int) ([]CustomerContract, error) {
	q := `SELECT ` + contractColumns + ` FROM customer_contracts
		WHERE ($1::uuid IS NULL OR customer_id = $1)
		  AND ($2::uuid IS NULL OR product_id = $2)
		  AND ($3::uuid IS NULL OR branch_id IS NOT DISTINCT FROM $3)
		  AND ($4::uuid IS NULL OR branch_id = $4 OR branch_id IS NULL)`
	args := []any{customerID, productID, branch, wall}
	if after != nil {
		args = append(args, after.CreatedAt, after.ID)
		q += fmt.Sprintf(` AND (created_at, id) < ($%d, $%d)`, len(args)-1, len(args))
	}
	q += ` ORDER BY created_at DESC, id DESC`
	args = append(args, limit)
	q += fmt.Sprintf(` LIMIT $%d`, len(args))
	rows, err := r.db.GetExecutor(ctx).Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list contracts: %w", err)
	}
	defer rows.Close()
	var out []CustomerContract
	for rows.Next() {
		c, err := scanContractRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

func (r *PostgresCatalogRepository) GetContractRow(ctx context.Context, id uuid.UUID) (*CustomerContract, error) {
	c, err := scanContractRow(r.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT `+contractColumns+` FROM customer_contracts WHERE id = $1`, id))
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return c, nil
}

func (r *PostgresCatalogRepository) LockContract(ctx context.Context, id uuid.UUID) (*CustomerContract, error) {
	c, err := scanContractRow(r.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT `+contractColumns+` FROM customer_contracts WHERE id = $1 FOR UPDATE`, id))
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return c, nil
}

func (r *PostgresCatalogRepository) InsertContract(ctx context.Context, c *CustomerContract) error {
	if c.ID == uuid.Nil {
		c.ID = uuid.New()
	}
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `
		INSERT INTO customer_contracts (id, customer_id, product_id, contract_price, price_uom, branch_id)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING revision, created_at, updated_at`,
		c.ID, c.CustomerID, c.ProductID, c.ContractPrice.DecimalString(), c.PriceUOM, c.BranchID).
		Scan(&c.Revision, &c.CreatedAt.Time, &c.UpdatedAt.Time)
	return catalogWriteErr(err, "contract")
}

func (r *PostgresCatalogRepository) UpdateContract(ctx context.Context, c *CustomerContract, revision int64) error {
	tag, err := r.db.GetExecutor(ctx).Exec(ctx, `
		UPDATE customer_contracts
		SET contract_price = $2, revision = revision + 1, updated_at = NOW()
		WHERE id = $1 AND revision = $3`,
		c.ID, c.ContractPrice.DecimalString(), revision)
	if err != nil {
		return catalogWriteErr(err, "contract")
	}
	return staleOrMissing(ctx, tag, c.ID, r.db, "customer_contracts")
}

func (r *PostgresCatalogRepository) DeleteContract(ctx context.Context, id uuid.UUID) error {
	_, err := r.db.GetExecutor(ctx).Exec(ctx, `DELETE FROM customer_contracts WHERE id = $1`, id)
	return err
}

func (r *PostgresCatalogRepository) ProductPricingFacts(ctx context.Context, productID uuid.UUID) (*ProductPricingFacts, error) {
	return NewResolveRepository(r.db).ProductPricingFacts(ctx, productID)
}

func (r *PostgresCatalogRepository) ProductRevision(ctx context.Context, productID uuid.UUID) (int64, error) {
	var revision int64
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `SELECT revision FROM products WHERE id = $1`, productID).Scan(&revision)
	return revision, err
}
