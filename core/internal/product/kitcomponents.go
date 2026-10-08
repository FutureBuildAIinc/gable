// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package product

// A kit's component list (ADR 0005 section 2.6): what a kit product
// contains, per one kit, in each component's own stocking unit. The PUT
// replaces the whole list in one transaction that takes the product's
// revision (C3-1 gave products one), bumps it, and writes its audit row and
// a product.updated event beside the write: a fault partway leaves the kit
// exactly as it was. A kit cannot contain a kit, cannot contain itself and
// lists a component once; the database CHECK backs the first two.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// KitComponent is one row of a kit's definition on the wire.
type KitComponent struct {
	ComponentProductID uuid.UUID      `json:"component_product_id"`
	SKU                string         `json:"sku"`
	Description        string         `json:"description"`
	Quantity           httpx.Quantity `json:"quantity"`
	UOM                string         `json:"uom"`
	Position           int            `json:"position"`
}

// KitComponentList is GET /products/{id}/kit-components and the PUT's
// answer; it carries the product's current revision so the client can send
// it back on its next write.
type KitComponentList struct {
	KitProductID uuid.UUID      `json:"kit_product_id"`
	Components   []KitComponent `json:"components"`
	Revision     int64          `json:"revision"`
}

// ComponentRequest is one entry of the PUT body.
type ComponentRequest struct {
	ComponentProductID *string         `json:"component_product_id"`
	Quantity           json.RawMessage `json:"quantity"`
}

// PutKitComponentsRequest is the body of PUT /products/{id}/kit-components.
type PutKitComponentsRequest struct {
	Revision   *int64             `json:"revision"`
	Components []ComponentRequest `json:"components"`
}

// GetKitComponents reads a kit's definition.
func (s *Service) GetKitComponents(ctx context.Context, kitID uuid.UUID) (*KitComponentList, error) {
	p, err := s.repo.GetProduct(ctx, kitID)
	if err != nil {
		return nil, kitNotFound(err)
	}
	if s.kits == nil {
		return nil, &httpx.Error{Status: http.StatusServiceUnavailable, Code: httpx.CodeUnavailable,
			Message: "this deployment's product store cannot serve kit definitions"}
	}
	rows, err := s.kits.ListKitComponents(ctx, kitID)
	if err != nil {
		return nil, err
	}
	out := &KitComponentList{KitProductID: kitID, Components: []KitComponent{}, Revision: p.Revision}
	out.Components = append(out.Components, rows...)
	return out, nil
}

// ReplaceKitComponents replaces the whole component list in ONE transaction
// (the recipe's rule): the product row is locked FOR UPDATE and its revision
// checked inside the transaction (428 without a precondition, 409 stale),
// the delete, the inserts and the is_kit update with the revision bump are
// one database act, and the audit row and the product.updated event join
// the same transaction, the event last. A product with a non empty list
// becomes a kit (products.is_kit); an empty list clears the definition.
// Every component must be a real product that is not itself a kit, listed
// once, and the quantity per kit is positive.
func (s *Service) ReplaceKitComponents(ctx context.Context, kitID uuid.UUID, req *PutKitComponentsRequest, pre RevisionPrecondition) (*KitComponentList, error) {
	var out *KitComponentList
	err := s.inTx(ctx, func(ctx context.Context) error {
		if s.kits == nil {
			return &httpx.Error{Status: http.StatusServiceUnavailable, Code: httpx.CodeUnavailable,
				Message: "this deployment's product store cannot serve kit definitions"}
		}
		revision, sku, err := s.kits.LockKitProduct(ctx, kitID)
		if err != nil {
			return kitNotFound(err)
		}
		if err := httpx.CheckRevision(revision, pre.IfMatch, pre.Revision); err != nil {
			return err
		}
		draft, err := s.validateComponents(ctx, kitID, req)
		if err != nil {
			return err
		}
		newRevision, err := s.kits.ReplaceKitComponents(ctx, kitID, draft, revision)
		if err != nil {
			return resolveRevision(err)
		}
		comps := make([]map[string]any, 0, len(draft))
		for _, c := range draft {
			comps = append(comps, map[string]any{
				"component_product_id": c.ComponentProductID, "quantity": c.Quantity.WireString(),
			})
		}
		if err := s.auditChange(ctx, kitID, sku, newRevision, EventProductUpdated,
			map[string]any{"parts": []string{"kit_components"}, "components": comps}); err != nil {
			return err
		}
		if err := s.recordEvent(ctx, kitID, sku, newRevision, EventProductUpdated, "kit_components"); err != nil {
			return err
		}
		sort.Slice(draft, func(i, j int) bool { return draft[i].Position < draft[j].Position })
		out = &KitComponentList{KitProductID: kitID, Components: draft, Revision: newRevision}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// validateComponents parses and validates the PUT's component list against
// the store, inside the caller's transaction. Every read goes through the
// transaction the context carries.
func (s *Service) validateComponents(ctx context.Context, kitID uuid.UUID, req *PutKitComponentsRequest) ([]KitComponent, error) {
	v := &httpx.Validator{}
	var draft []KitComponent
	seen := make(map[uuid.UUID]bool)
	for i, c := range req.Components {
		path := fmt.Sprintf("components[%d]", i)
		id, ok := v.UUID(path+".component_product_id", c.ComponentProductID, true)
		if !ok {
			continue
		}
		if id == kitID {
			v.Check(false, path+".component_product_id", "a kit cannot contain itself")
			continue
		}
		if seen[id] {
			v.Check(false, path+".component_product_id", "a kit lists a component once")
			continue
		}
		seen[id] = true
		qty, okQ := v.Quantity(path+".quantity", c.Quantity, true)
		if !okQ {
			continue
		}
		v.Check(qty > 0, path+".quantity", "must be greater than zero")
		var comp struct {
			SKU, Description, UOM string
			IsKit                 bool
		}
		err := s.kits.ProductKitRef(ctx, id, &comp.SKU, &comp.Description, &comp.UOM, &comp.IsKit)
		if err != nil {
			v.Check(false, path+".component_product_id", "no such product")
			continue
		}
		v.Check(!comp.IsKit, path+".component_product_id",
			"is itself a kit: a kit holds one level")
		draft = append(draft, KitComponent{
			ComponentProductID: id, SKU: comp.SKU, Description: comp.Description,
			Quantity: qty, UOM: comp.UOM, Position: i,
		})
	}
	if err := v.Err(); err != nil {
		return nil, err
	}
	return draft, nil
}

// kitNotFound maps the repository's plain "product not found" to the wire's
// 404. GetProduct wraps pgx.ErrNoRows in that exact string.
func kitNotFound(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) || err.Error() == "product not found" {
		return httpx.NotFound("product not found")
	}
	return err
}

// ListKitComponents and ReplaceKitComponents on the repository.
func (r *PostgresRepository) ListKitComponents(ctx context.Context, kitID uuid.UUID) ([]KitComponent, error) {
	rows, err := r.db.GetExecutor(ctx).Query(ctx, `
		SELECT k.component_product_id, COALESCE(p.sku, ''), COALESCE(p.description, ''),
		       ROUND(k.quantity * 10000)::bigint, COALESCE(p.uom_primary::text, ''), k.position
		FROM product_kit_components k
		LEFT JOIN products p ON p.id = k.component_product_id
		WHERE k.kit_product_id = $1
		ORDER BY k.position`, kitID)
	if err != nil {
		return nil, fmt.Errorf("failed to list kit components: %w", err)
	}
	defer rows.Close()
	var out []KitComponent
	for rows.Next() {
		var c KitComponent
		var qty int64
		if err := rows.Scan(&c.ComponentProductID, &c.SKU, &c.Description, &qty, &c.UOM, &c.Position); err != nil {
			return nil, fmt.Errorf("failed to scan kit component: %w", err)
		}
		c.Quantity = httpx.Quantity(qty)
		out = append(out, c)
	}
	return out, rows.Err()
}

// LockKitProduct takes the product row FOR UPDATE and reads the revision
// and the sku the write's audit row and event carry. The caller checks the
// revision inside the same transaction (ADR 0001 section 11).
func (r *PostgresRepository) LockKitProduct(ctx context.Context, kitID uuid.UUID) (int64, string, error) {
	var revision int64
	var sku string
	err := r.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT revision, sku FROM products WHERE id = $1 FOR UPDATE`, kitID).Scan(&revision, &sku)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, "", ErrNotFound
		}
		return 0, "", fmt.Errorf("failed to lock the kit product: %w", err)
	}
	return revision, sku, nil
}

// ReplaceKitComponents clears the list, inserts the new one and moves the
// kit flag with the product's revision, all through the caller's executor:
// inside the service's transaction the three are one database act that
// commits or rolls back together. The revision bump is conditional on the
// revision the caller checked under the lock (belt and braces: the row
// cannot move under a held lock, and a fault here is a 409, never a silent
// overwrite).
func (r *PostgresRepository) ReplaceKitComponents(ctx context.Context, kitID uuid.UUID, comps []KitComponent, revision int64) (int64, error) {
	exec := r.db.GetExecutor(ctx)
	if _, err := exec.Exec(ctx, `DELETE FROM product_kit_components WHERE kit_product_id = $1`, kitID); err != nil {
		return 0, fmt.Errorf("failed to clear kit components: %w", err)
	}
	for _, c := range comps {
		if _, err := exec.Exec(ctx, `
			INSERT INTO product_kit_components (kit_product_id, component_product_id, quantity, position)
			VALUES ($1, $2, $3::numeric / 10000, $4)`,
			kitID, c.ComponentProductID, int64(c.Quantity), c.Position); err != nil {
			return 0, fmt.Errorf("failed to insert kit component: %w", err)
		}
	}
	var newRevision int64
	err := exec.QueryRow(ctx, `
		UPDATE products SET is_kit = $2, revision = revision + 1, updated_at = NOW()
		WHERE id = $1 AND revision = $3
		RETURNING revision`, kitID, len(comps) > 0, revision).Scan(&newRevision)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			var exists bool
			if qerr := exec.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM products WHERE id = $1)`, kitID).Scan(&exists); qerr == nil && exists {
				return 0, ErrStaleRevision
			}
			return 0, ErrNotFound
		}
		return 0, fmt.Errorf("failed to set the kit flag: %w", err)
	}
	return newRevision, nil
}

// ProductKitRef reads one product's kit-relevant fields. A missing product
// answers pgx.ErrNoRows so the caller reports it as a field error.
func (r *PostgresRepository) ProductKitRef(ctx context.Context, id uuid.UUID, sku, description, uom *string, isKit *bool) error {
	err := r.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT sku, COALESCE(description, ''), uom_primary::text, is_kit FROM products WHERE id = $1`, id).
		Scan(sku, description, uom, isKit)
	if err != nil {
		return err
	}
	return nil
}

// HandleGetKitComponents answers a kit's component list.
func (h *Handler) HandleGetKitComponents(w http.ResponseWriter, r *http.Request) {
	if _, err := httpx.StrictQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	id, err := parseKitID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	list, err := h.service.GetKitComponents(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeKitJSON(w, http.StatusOK, list)
}

// HandlePutKitComponents replaces the whole component list in one
// transaction at the product's revision: If-Match or the body revision, as
// every write (ADR 0001 section 11), and the new revision and its ETag on
// the answer.
func (h *Handler) HandlePutKitComponents(w http.ResponseWriter, r *http.Request) {
	if _, err := httpx.StrictQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	id, err := parseKitID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var req PutKitComponentsRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	pre := RevisionPrecondition{IfMatch: r.Header.Get("If-Match"), Revision: req.Revision}
	list, err := h.service.ReplaceKitComponents(r.Context(), id, &req, pre)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteRevisionETag(w, list.Revision)
	writeKitJSON(w, http.StatusOK, list)
}

func parseKitID(r *http.Request) (uuid.UUID, error) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		return uuid.Nil, httpx.BadRequest("invalid product id",
			httpx.FieldError{Field: "id", Message: "must be a UUID"})
	}
	return id, nil
}

func writeKitJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
