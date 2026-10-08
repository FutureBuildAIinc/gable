// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package product

// A kit's component list (ADR 0005 section 2.6): what a kit product
// contains, per one kit, in each component's own stocking unit. The routes
// are on the contract from birth; until the products module itself converts,
// the PUT replaces the whole list last write wins (no product revision to
// precondition on), stated in the fragment. A kit cannot contain a kit and
// cannot contain itself; the database CHECK backs both.

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

// KitComponentList is GET /products/{id}/kit-components and the PUT body.
type KitComponentList struct {
	KitProductID uuid.UUID      `json:"kit_product_id"`
	Components   []KitComponent `json:"components"`
}

// ComponentRequest is one entry of the PUT body.
type ComponentRequest struct {
	ComponentProductID *string         `json:"component_product_id"`
	Quantity           json.RawMessage `json:"quantity"`
}

// PutKitComponentsRequest is the body of PUT /products/{id}/kit-components.
type PutKitComponentsRequest struct {
	Components []ComponentRequest `json:"components"`
}

// GetKitComponents reads a kit's definition.
func (s *Service) GetKitComponents(ctx context.Context, kitID uuid.UUID) (*KitComponentList, error) {
	prod, err := s.repo.GetProduct(ctx, kitID)
	if err != nil {
		return nil, kitNotFound(err)
	}
	rows, err := s.repo.ListKitComponents(ctx, kitID)
	if err != nil {
		return nil, err
	}
	out := &KitComponentList{KitProductID: kitID, Components: []KitComponent{}}
	for _, c := range rows {
		out.Components = append(out.Components, c)
	}
	_ = prod
	return out, nil
}

// ReplaceKitComponents replaces the whole component list. A product with a
// non empty list becomes a kit (products.is_kit); an empty list clears the
// definition. Every component must be a real product that is not itself a
// kit, and the quantity per kit is positive.
func (s *Service) ReplaceKitComponents(ctx context.Context, kitID uuid.UUID, req *PutKitComponentsRequest) (*KitComponentList, error) {
	v := &httpx.Validator{}
	if _, err := s.repo.GetProduct(ctx, kitID); err != nil {
		return nil, kitNotFound(err)
	}
	var draft []KitComponent
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
		qty, okQ := v.Quantity(path+".quantity", c.Quantity, true)
		if !okQ {
			continue
		}
		v.Check(qty > 0, path+".quantity", "must be greater than zero")
		var comp struct {
			SKU, Description, UOM string
			IsKit                 bool
		}
		err := s.repo.ProductKitRef(ctx, id, &comp.SKU, &comp.Description, &comp.UOM, &comp.IsKit)
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
	if err := s.repo.ReplaceKitComponents(ctx, kitID, draft); err != nil {
		return nil, err
	}
	sort.Slice(draft, func(i, j int) bool { return draft[i].Position < draft[j].Position })
	return &KitComponentList{KitProductID: kitID, Components: draft}, nil
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

func (r *PostgresRepository) ReplaceKitComponents(ctx context.Context, kitID uuid.UUID, comps []KitComponent) error {
	exec := r.db.GetExecutor(ctx)
	if _, err := exec.Exec(ctx, `DELETE FROM product_kit_components WHERE kit_product_id = $1`, kitID); err != nil {
		return fmt.Errorf("failed to clear kit components: %w", err)
	}
	for _, c := range comps {
		if _, err := exec.Exec(ctx, `
			INSERT INTO product_kit_components (kit_product_id, component_product_id, quantity, position)
			VALUES ($1, $2, $3::numeric / 10000, $4)`,
			kitID, c.ComponentProductID, int64(c.Quantity), c.Position); err != nil {
			return fmt.Errorf("failed to insert kit component: %w", err)
		}
	}
	isKit := len(comps) > 0
	if _, err := exec.Exec(ctx, `UPDATE products SET is_kit = $2 WHERE id = $1`, kitID, isKit); err != nil {
		return fmt.Errorf("failed to set the kit flag: %w", err)
	}
	return nil
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

// HandlePutKitComponents replaces the whole component list: last write wins
// until the products module converts and the PUT can take the product's
// revision (ADR 0005 2.6).
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
	list, err := h.service.ReplaceKitComponents(r.Context(), id, &req)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
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
