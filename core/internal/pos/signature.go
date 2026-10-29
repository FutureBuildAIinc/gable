// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gablelbm/gable/internal/document"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/salesdoc"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/google/uuid"
)

// maxSignatureBytes caps the drawn image a sale may carry, the same cap the
// table holds.
const maxSignatureBytes = 131072

// pngMagic is the eight bytes every PNG starts with.
var pngMagic = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}

// Signature is a sale's sealed signature as the wire states it. The image
// itself is never answered: it rides the receipt.
type Signature struct {
	SaleID      uuid.UUID       `json:"sale_id"`
	SignerName  string          `json:"signer_name"`
	SignedAt    httpx.Timestamp `json:"signed_at"`
	TotalCents  httpx.Cents     `json:"total_cents"`
	ImageSHA256 string          `json:"image_sha256"`
	Seal        string          `json:"seal"`
	CapturedBy  string          `json:"captured_by"`
}

// storedSignature is the row with its image, for the receipt.
type storedSignature struct {
	Signature
	Image []byte
}

// SealOf is the SHA-256 seal over the sale id, the total in cents, the
// signer, the time and the hash of the image. Any change to one of them is a
// different seal; a sealed row never changes.
func SealOf(saleID uuid.UUID, totalCents int64, signer string, signedAt time.Time, imageSHA256 string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%s|%s|%s",
		saleID, totalCents, signer, signedAt.UTC().Format(time.RFC3339Nano), imageSHA256)))
	return hex.EncodeToString(sum[:])
}

// SignatureIn is a parsed signature capture.
type SignatureIn struct {
	SignerName string
	PNG        []byte
}

// signatureRequest is POST /pos/transactions/{id}/signature.
type signatureRequest struct {
	SignerName     *string `json:"signer_name"`
	ImagePNGBase64 *string `json:"image_png_base64"`
}

func (r *signatureRequest) parse() (*SignatureIn, error) {
	v := &httpx.Validator{}
	out := &SignatureIn{}
	if r.SignerName != nil {
		out.SignerName = strings.TrimSpace(*r.SignerName)
	}
	v.Required("signer_name", out.SignerName)
	v.Check(len(out.SignerName) <= 200, "signer_name", "must be at most 200 characters")
	raw := ""
	if r.ImagePNGBase64 != nil {
		raw = strings.TrimSpace(*r.ImagePNGBase64)
	}
	// a canvas answers a data URL: take its payload
	if i := strings.Index(raw, ";base64,"); i >= 0 && strings.HasPrefix(raw, "data:") {
		raw = raw[i+len(";base64,"):]
	}
	v.Required("image_png_base64", raw)
	if raw != "" {
		if base64.StdEncoding.DecodedLen(len(raw)) > maxSignatureBytes+4 {
			v.Check(false, "image_png_base64", fmt.Sprintf("must be at most %d bytes of PNG", maxSignatureBytes))
		} else if img, err := base64.StdEncoding.DecodeString(raw); err != nil {
			v.Check(false, "image_png_base64", "must be standard base64")
		} else if len(img) > maxSignatureBytes {
			v.Check(false, "image_png_base64", fmt.Sprintf("must be at most %d bytes of PNG", maxSignatureBytes))
		} else if !bytes.HasPrefix(img, pngMagic) {
			v.Check(false, "image_png_base64", "must be a PNG image")
		} else {
			out.PNG = img
		}
	}
	if err := v.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// CaptureSignature seals the signer's typed name and drawn image to a
// completed sale. A sale is signed once: the row never changes after it is
// written, so a second capture is refused 409 signature_exists.
func (s *Service) CaptureSignature(ctx context.Context, saleID uuid.UUID, in *SignatureIn, actor string) (*Signature, error) {
	if err := s.holdSaleBranch(ctx, saleID); err != nil {
		return nil, err
	}
	var out *Signature
	err := s.inTx(ctx, func(ctx context.Context) error {
		if err := s.repo.LockSale(ctx, saleID); err != nil {
			return err
		}
		sale, err := s.repo.GetSale(ctx, saleID)
		if err != nil {
			return err
		}
		if sale.Status != StatusCompleted {
			return conflict("not_completed", fmt.Sprintf("only a completed sale is signed: this one is %s", sale.Status.Status()))
		}
		sum := sha256.Sum256(in.PNG)
		imageHash := hex.EncodeToString(sum[:])
		// the time at the precision the table keeps, so the seal recomputes
		signedAt := s.now().UTC().Truncate(time.Microsecond)
		sig := &storedSignature{Signature: Signature{SaleID: saleID, SignerName: in.SignerName,
			SignedAt: httpx.TimestampOf(signedAt), TotalCents: sale.TotalCents, ImageSHA256: imageHash,
			CapturedBy: actor}, Image: in.PNG}
		sig.Seal = SealOf(saleID, int64(sale.TotalCents), in.SignerName, signedAt, imageHash)
		if err := s.repo.InsertSignature(ctx, sig); err != nil {
			return err
		}
		if s.auditLog != nil {
			if err := s.auditLog.Log(ctx, audit.Entry{
				Action: "pos.signature.captured", EntityType: "pos_transaction", EntityID: saleID, UserID: actor,
				Changes: map[string]any{"number": sale.Number, "signer_name": in.SignerName, "seal": sig.Seal,
					"total_cents": int64(sale.TotalCents)}}); err != nil {
				return fmt.Errorf("failed to write the audit row: %w", err)
			}
		}
		out = &sig.Signature
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// StartReceiptPrint counts a print of the sale's receipt and hands back what
// the print needs: its number and the sealed signature when there is one. The
// sale is read through the caller's wall first, so a sale the caller cannot
// read is the module's 404 and no print is counted.
func (s *Service) StartReceiptPrint(ctx context.Context, saleID uuid.UUID) (document.ReceiptPrint, error) {
	if err := s.holdSaleBranch(ctx, saleID); err != nil {
		return document.ReceiptPrint{}, err
	}
	var out document.ReceiptPrint
	err := s.inTx(ctx, func(ctx context.Context) error {
		sale, err := s.repo.GetSale(ctx, saleID)
		if err != nil {
			return err
		}
		if sale.Status != StatusCompleted {
			return conflict("no_receipt", "this sale has no receipt: only a completed sale has one")
		}
		n, err := s.repo.BumpPrintCount(ctx, saleID)
		if err != nil {
			return err
		}
		out.Number = n
		sig, found, err := s.repo.GetSignature(ctx, saleID)
		if err != nil {
			return err
		}
		if found {
			out.Signature = &document.ReceiptSignature{SignerName: sig.SignerName, SignedAt: sig.SignedAt.Time,
				SealHex: sig.Seal, PNG: sig.Image}
		}
		return nil
	})
	return out, err
}

// CancelReceiptPrint takes one print back off the count after a render that
// failed, so a print that never reached the paper is never a reprint.
func (s *Service) CancelReceiptPrint(ctx context.Context, saleID uuid.UUID) {
	if err := s.repo.UnbumpPrintCount(context.WithoutCancel(ctx), saleID); err != nil {
		s.logger.Error("the print count of a failed render was not given back", "sale_id", saleID, "error", err)
	}
}

// ThermalReceipt builds the plain 80mm receipt input for the completed counter
// sale that owns invoiceID and the given print facts.
func (s *Service) ThermalReceipt(ctx context.Context, invoiceID uuid.UUID, print document.ReceiptPrint) (uuid.UUID, document.ThermalReceipt, error) {
	facts, err := s.repo.ReceiptSaleByInvoice(ctx, invoiceID)
	if err != nil {
		return uuid.Nil, document.ThermalReceipt{}, err
	}
	sale := &facts.sale
	lines, err := s.repo.GetLines(ctx, sale.ID)
	if err != nil {
		return uuid.Nil, document.ThermalReceipt{}, err
	}
	tenders, err := s.repo.GetTenders(ctx, sale.ID)
	if err != nil {
		return uuid.Nil, document.ThermalReceipt{}, err
	}
	soldAt := sale.CreatedAt.Time
	if sale.CompletedAt != nil {
		soldAt = sale.CompletedAt.Time
	}
	cashier := facts.cashierName
	if cashier == "" && sale.CashierID != uuid.Nil {
		cashier = sale.CashierID.String()[:8]
	}
	r := document.ThermalReceipt{
		SaleNumber:    sale.Number,
		SoldAt:        soldAt,
		Cashier:       cashier,
		Customer:      facts.customerName,
		AccountNumber: facts.accountNumber,
		PONumber:      derefString(sale.CustomerPO),
		JobName:       facts.jobName,
		SubtotalCents: sale.SubtotalCents,
		TaxCents:      sale.TaxCents,
		TotalCents:    sale.TotalCents,
	}
	for i := range lines {
		l := &lines[i]
		if l.LineType == salesdoc.LineText || l.Quantity == nil || l.UnitPrice == nil || l.LineTotal == nil {
			continue
		}
		sku := derefString(l.SKU)
		if sku == "" {
			sku = derefString(l.ChargeCode)
		}
		uom := derefString(l.UOM)
		priceUOM := derefString(l.PriceUOM)
		if priceUOM == "" {
			priceUOM = uom
		}
		tl := document.ThermalLine{
			SKU:         sku,
			Description: l.Description,
			Quantity:    *l.Quantity,
			UOM:         uom,
			UnitPrice:   *l.UnitPrice,
			PriceUOM:    priceUOM,
			LineTotal:   *l.LineTotal,
		}
		// The weighed line's marking (CP-19): the ticket on the paper, MAN
		// WT when the weight was typed.
		if l.ScaleTicketNumber != nil {
			tl.ScaleTicket = *l.ScaleTicketNumber
			tl.WeightGrossLb, tl.WeightTareLb, tl.WeightNetLb = l.WeightGrossLb, l.WeightTareLb, l.WeightNetLb
			tl.ManualWeight = l.WeightSource != nil && *l.WeightSource == "manual"
		}
		r.Lines = append(r.Lines, tl)
	}
	changeLeft := sale.ChangeCents
	var accountCents int64
	// The cash rounding the branch's rule made (CP-18), read from the
	// drawer's cash: the cash the sale's payments kept, less the exact cash
	// the sale needed (the total less the non-cash legs). An exchange's
	// credit is one of those legs, the store credit tender row the
	// completion added, so it is counted once here. A tender row the change
	// swallowed whole has no payment and never reached the drawer, so it
	// counts for nothing here; a sale the rule did not touch shows no
	// rounding.
	var cashKept, nonCash int64
	for i := range tenders {
		switch tenders[i].Method {
		case TenderCash:
			if tenders[i].PaymentID != nil {
				cashKept += int64(tenders[i].AmountCents)
			}
		default:
			nonCash += int64(tenders[i].AmountCents)
		}
	}
	exactCash := int64(sale.TotalCents) - nonCash
	if exactCash < 0 {
		exactCash = 0
	}
	r.RoundingCents = httpx.Cents(cashKept - exactCash)
	for i := range tenders {
		t := &tenders[i]
		tt := document.ThermalTender{
			Method:      string(t.Method),
			AmountCents: t.AmountCents,
			LastFour:    derefString(t.CardLast4),
		}
		if t.Method == TenderCash && changeLeft > 0 {
			if t.PaymentID != nil {
				tt.AmountCents += changeLeft
			}
			tt.ChangeCents = changeLeft
			changeLeft = 0
		}
		if t.Method == TenderAccount {
			accountCents += int64(t.AmountCents)
		}
		r.Tenders = append(r.Tenders, tt)
	}
	if accountCents > 0 {
		r.BalanceCents = httpx.Cents(accountCents)
		if sale.CustomerID != nil {
			if open, oerr := s.repo.OpenReceivableCents(ctx, *sale.CustomerID); oerr == nil && open > 0 {
				r.BalanceCents = httpx.Cents(open)
			}
		}
	}
	if print.Signature != nil {
		prefix := print.Signature.SealHex
		if len(prefix) > 12 {
			prefix = prefix[:12]
		}
		r.Signature = &document.ThermalSignature{
			Name:       print.Signature.SignerName,
			SignedAt:   print.Signature.SignedAt,
			SealPrefix: prefix,
			PNG:        print.Signature.PNG,
		}
	}
	if print.Reprint() {
		r.Reprint = &document.ThermalReprint{PrintNumber: print.Number}
	}
	return sale.BranchID, r, nil
}

// ThermalRenderer adapts the counter's ThermalReceipt builder to doc.GenerateThermalReceiptPDF.
func (s *Service) ThermalRenderer(doc *document.Service) document.ThermalRenderer {
	if doc == nil {
		return nil
	}
	return func(ctx context.Context, invoiceID uuid.UUID, print document.ReceiptPrint) ([]byte, error) {
		branch, r, err := s.ThermalReceipt(ctx, invoiceID, print)
		if err != nil {
			return nil, err
		}
		return doc.GenerateThermalReceiptPDF(ctx, branch, r)
	}
}

// CaptureSignatureHandler is POST /pos/transactions/{id}/signature.
func (h *Handler) CaptureSignatureHandler(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteError(w, r, httpx.BadRequest("one or more fields failed validation", httpx.FieldError{Field: "id", Message: "must be a UUID"}))
		return
	}
	var req signatureRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	in, err := req.parse()
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	sig, err := h.service.CaptureSignature(r.Context(), id, in, h.actorFrom(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, sig)
}
