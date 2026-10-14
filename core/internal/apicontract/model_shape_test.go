// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package apicontract

import (
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/account"
	"github.com/gablelbm/gable/internal/ap"
	"github.com/gablelbm/gable/internal/bankrecon"
	"github.com/gablelbm/gable/internal/chargecode"
	"github.com/gablelbm/gable/internal/configurator"
	"github.com/gablelbm/gable/internal/crm"
	"github.com/gablelbm/gable/internal/customer"
	"github.com/gablelbm/gable/internal/dashboard"
	"github.com/gablelbm/gable/internal/delivery"
	"github.com/gablelbm/gable/internal/deposit"
	"github.com/gablelbm/gable/internal/edi"
	"github.com/gablelbm/gable/internal/events"
	"github.com/gablelbm/gable/internal/gl"
	"github.com/gablelbm/gable/internal/governance"
	"github.com/gablelbm/gable/internal/integrations"
	"github.com/gablelbm/gable/internal/inventory"
	"github.com/gablelbm/gable/internal/invoice"
	"github.com/gablelbm/gable/internal/location"
	"github.com/gablelbm/gable/internal/matching"
	"github.com/gablelbm/gable/internal/millwork"
	"github.com/gablelbm/gable/internal/order"
	"github.com/gablelbm/gable/internal/parsing"
	"github.com/gablelbm/gable/internal/partner"
	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/internal/pim"
	"github.com/gablelbm/gable/internal/portal"
	"github.com/gablelbm/gable/internal/pos"
	"github.com/gablelbm/gable/internal/pricing"
	"github.com/gablelbm/gable/internal/product"
	"github.com/gablelbm/gable/internal/project"
	"github.com/gablelbm/gable/internal/purchase_order"
	"github.com/gablelbm/gable/internal/quote"
	"github.com/gablelbm/gable/internal/reporting"
	"github.com/gablelbm/gable/internal/routecensus"
	"github.com/gablelbm/gable/internal/salesdoc"
	"github.com/gablelbm/gable/internal/salesteam"
	"github.com/gablelbm/gable/internal/staff"
	"github.com/gablelbm/gable/internal/tax"
	"github.com/gablelbm/gable/internal/techadmin"
	"github.com/gablelbm/gable/internal/vendor"
	"github.com/gablelbm/gable/internal/vision"
	"github.com/gablelbm/gable/pkg/apps"
	"gopkg.in/yaml.v3"
)

// modelBoundSchemas ties every fragment schema the server ENCODES to the Go
// struct it is encoded from: response bodies and echoed request bodies. The
// table deliberately leaves request-only DTOs out: their json tags say what
// the server reads, not what it writes, and required on a request schema is
// a client obligation the Go tags cannot express.
var modelBoundSchemas = []struct {
	Schema string
	Model  any
}{
	// quote
	{"Quote", quote.Quote{}},
	{"QuoteSummary", quote.QuoteSummary{}},
	{"QuoteLine", quote.QuoteLine{}},
	// order
	{"Order", order.Order{}},
	{"OrderSummary", order.OrderSummary{}},
	{"SalesLine", salesdoc.Line{}},
	{"ChargeCode", chargecode.Code{}},
	{"QuoteAnalytics", quote.QuoteAnalytics{}},
	{"QuoteAnalyticsTrend", quote.QuoteAnalyticsTrend{}},
	// customer
	{"Customer", customer.Customer{}},
	{"PriceLevel", customer.PriceLevel{}},
	{"PaymentTermsRef", customer.PaymentTermsRef{}},
	{"EscalationPolicy", customer.EscalationPolicy{}},
	{"Contact", customer.Contact{}},
	{"ShipTo", customer.ShipTo{}},
	{"PaymentTermsRecord", customer.PaymentTerms{}},
	// order
	{"Order", order.Order{}},
	{"OrderLine", order.OrderLine{}},
	// invoice
	{"Invoice", invoice.Invoice{}},
	{"InvoiceLine", invoice.InvoiceLine{}},
	{"CreditMemo", invoice.CreditMemo{}},
	// payment
	{"Payment", payment.Payment{}},
	{"Refund", payment.Refund{}},
	{"PaymentIntentResponse", payment.PaymentIntentResponse{}},
	// product
	{"Geometry", product.Geometry{}},
	{"ReorderAlert", product.ReorderAlert{}},
	{"PimContent", pim.PIMContent{}},
	{"PimMedia", pim.PIMMedia{}},
	{"PimCollateral", pim.PIMCollateral{}},
	{"ProductDetail", pim.ProductDetail{}},
	{"ProductView", product.View{}},
	// location
	{"Location", location.Location{}},
	{"BranchSummary", location.BranchSummary{}},
	{"UserLocation", location.UserLocation{}},
	// integration (AI_LM)
	{"IntegrationVehicle", integrations.VehicleResponse{}},
	{"IntegrationDriver", integrations.DriverResponse{}},
	{"IntegrationLocation", integrations.LocationResponse{}},
	{"IntegrationProduct", integrations.ProductResponse{}},
	{"IntegrationOrderLine", integrations.IntegrationOrderLine{}},
	{"IntegrationOrder", integrations.IntegrationOrderResponse{}},
	{"IntegrationDeliveryRouteResponse", integrations.DeliveryRouteResponse{}},
	{"IntegrationValidateStaffResponse", integrations.ValidateStaffResponse{}},
	// delivery
	{"DeliveryVehicle", delivery.Vehicle{}},
	{"DeliveryDriver", delivery.Driver{}},
	{"DeliveryRoute", delivery.Route{}},
	{"Delivery", delivery.Delivery{}},
	{"DeliveryPodPhoto", delivery.PODPhoto{}},
	{"DeliveryCapacityWarning", delivery.CapacityWarning{}},
	{"DeliveryRouteLeg", delivery.RouteLeg{}},
	{"DeliveryRouteOptimizationResult", delivery.RouteOptimizationResult{}},
	// inventory
	{"InventoryLevel", inventory.Level{}},
	{"InventoryProductSummary", inventory.ProductSummary{}},
	// deposits
	{"Deposit", deposit.CustomerDeposit{}},
	{"DepositApplication", deposit.DepositApplication{}},
	// accounts
	{"AccountSummary", account.AccountSummary{}},
	{"CustomerTransaction", account.CustomerTransaction{}},
	// vendors
	{"Vendor", vendor.Vendor{}},
	// sales-team
	{"SalesPerson", salesteam.SalesPerson{}},
	// activities / crm
	{"Activity", crm.Activity{}},
	// quote / exposure
	{"QuoteExposureEvent", pricing.QuoteExposureEvent{}},
	{"ExposureRow", pricing.ExposureRow{}},
	{"EscalateNowResult", pricing.EscalateNowResult{}},
	{"EscalateNowLine", pricing.EscalateNowLine{}},
	// pos
	{"PosTransaction", pos.POSTransaction{}},
	{"PosLineItem", pos.POSLineItem{}},
	{"PosTender", pos.POSTender{}},
	{"PosTransactionSummary", pos.TransactionSummary{}},
	{"PosSearchResult", pos.QuickSearchResult{}},
	{"PosCatalogProduct", pos.CatalogProduct{}},
	{"PosTillSession", pos.TillSession{}},
	{"PosTillReport", pos.TillReport{}},
	{"PosZReport", pos.ZReport{}},
	{"PosReturn", pos.POSReturn{}},
	{"PosReturnLine", pos.POSReturnLine{}},
	{"PosSyncResponse", pos.OfflineSyncResponse{}},
	{"PosSyncError", pos.SyncError{}},
	// portal (R1-7c)
	// Health schemas (live, ready, metrics) are inline map[string]any in serve.go
	// with no struct to bind, so they are omitted from this table.
	{"PortalConfig", portal.PortalConfig{}},
	{"PortalDashboard", portal.PortalDashboardDTO{}},
	{"PortalOrder", portal.PortalOrderDTO{}},
	{"PortalOrderLine", portal.PortalLineDTO{}},
	{"PortalInvoice", portal.PortalInvoiceDTO{}},
	{"PortalDelivery", portal.PortalDeliveryDTO{}},
	{"PortalReorderResponse", portal.ReorderResponse{}},
	{"PortalCancelOrderResponse", portal.CancelOrderResponse{}},
	{"PortalCart", portal.CartDTO{}},
	{"PortalCartItem", portal.CartItemDTO{}},
	{"PortalCheckoutResponse", portal.CheckoutResponse{}},
	{"PortalCategoryNode", portal.CategoryNodeDTO{}},
	{"PortalVolumeBreak", portal.VolumeBreakDTO{}},
	{"PortalCatalogProduct", portal.CatalogProductDTO{}},
	// PortalCatalogDetail has CatalogProductDTO embedded and flattened in the
	// schema; its fields are already covered by PortalCatalogProduct, so it
	// is omitted to avoid false positives on the embedded struct name.
	// {"PortalCatalogDetail", portal.CatalogDetailDTO{}},
	{"PortalQuote", portal.PortalQuoteDTO{}},
	{"PortalQuoteLine", portal.PortalQuoteLineDTO{}},
	{"PortalInvite", portal.PortalInvite{}},
	// project (R1-7c)
	{"Project", project.Project{}},
	{"ProjectDashboard", project.ProjectDashboardDTO{}},
	{"ProjectItem", project.ProjectItem{}},
	// partner (R1-7c)
	{"PartnerDashboard", partner.DashboardDTO{}},
	// admin, apps, governance, millwork, configurator (R1-7f)
	{"TechAdminKey", techadmin.APIKey{}},
	{"TechAdminCreateKeyResponse", techadmin.CreateKeyResponse{}},
	{"TechAdminSettingsStatus", techadmin.AISettingsResponse{}},
	{"StaffMember", staff.Staff{}},
	{"StaffModule", staff.Module{}},
	// AppsManifestStatus is apps.Status, which embeds Manifest and adds enabled
	// and orphaned; the embedded struct is bound, the two extras checked by hand.
	{"AppsManifestStatus", apps.Manifest{}},
	{"GovernanceRFC", governance.RFC{}},
	{"MillworkOption", millwork.MillworkOption{}},
	{"ConfiguratorRule", configurator.ConfiguratorRule{}},
	{"ConfiguratorPreset", configurator.ConfiguratorPreset{}},
	{"ConfiguratorAvailableOption", configurator.AvailableOption{}},
	{"ConfiguratorValidationConflict", configurator.ValidationConflict{}},
	{"ConfiguratorValidateResponse", configurator.ValidateConfigResponse{}},
	{"ConfiguratorBuildSKUResponse", configurator.BuildSKUResponse{}},
	// reporting, reports, dashboard (R1-7f)
	{"ReportingSavedReport", reporting.SavedReport{}},
	{"ReportingSchedule", reporting.ReportSchedule{}},
	{"ReportingScheduleExecution", reporting.ScheduleExecution{}},
	{"ReportingScheduleResponse", reporting.ReportScheduleResponse{}},
	{"ReportingScheduleList", reporting.ReportScheduleListResponse{}},
	{"ReportingDailyTill", reporting.DailyTillReport{}},
	{"ReportingSalesSummary", reporting.SalesSummaryReport{}},
	{"ReportingArAgingBucket", reporting.ARAgingBucket{}},
	{"ReportingArAging", reporting.ARAgingReport{}},
	{"ReportingStatementLine", reporting.StatementLine{}},
	{"ReportingCustomerStatement", reporting.CustomerStatement{}},
	{"ReportsExposurePortfolio", pricing.PortfolioSummary{}},
	{"ReportsExposureCustomerRow", pricing.PortfolioCustomerRow{}},
	{"ReportsExposureSalespersonRow", pricing.PortfolioSalespersonRow{}},
	{"DashboardSummary", dashboard.DashboardSummary{}},
	{"DashboardInventoryAlert", dashboard.InventoryAlert{}},
	{"DashboardTopCustomer", dashboard.TopCustomer{}},
	{"DashboardOrderActivity", dashboard.OrderActivity{}},
	{"DashboardRecentOrder", dashboard.RecentOrder{}},
	{"DashboardRevenueTrendPoint", dashboard.RevenueTrendPoint{}},
	// vision, parsing (R1-7f)
	{"VisionBlueprintScanResponse", vision.BlueprintScanResponse{}},
	{"VisionMismatch", vision.Mismatch{}},
	{"ParsingParseResponse", parsing.ParseResponse{}},
	{"ParsingParsedItem", parsing.ParsedItem{}},
	{"ParsingMatchedProduct", parsing.MatchedProduct{}},
	// finance (R1-7e)
	{"PricingCalculatedPrice", pricing.CalculatedPriceView{}},
	{"PricingRule", pricing.PricingRule{}},
	{"PricingEscalationResult", pricing.EscalationResult{}},
	{"PricingProductCategory", pricing.ProductCategory{}},
	{"PricingCategoryRule", pricing.CategoryPricingRule{}},
	{"PricingCategoryRuleAudit", pricing.CategoryPricingAudit{}},
	{"PricingMatrixCell", pricing.MatrixCell{}},
	{"PricingMatrix", pricing.MatrixResponse{}},
	{"PricingResolvedCategoryPrice", pricing.ResolvedCategoryPrice{}},
	{"PricingRebateTier", pricing.RebateTier{}},
	{"PricingRebateProgram", pricing.RebateProgram{}},
	{"PricingRebateClaim", pricing.RebateClaim{}},
	{"MarketIndex", pricing.MarketIndex{}},
	{"MarketIndexHistory", pricing.MarketIndexHistory{}},
	{"MarketIndexRefreshPreview", pricing.IndexRefreshPreview{}},
	{"MarketIndexRefreshTopCustomer", pricing.IndexRefreshTopCustomer{}},
	{"PurchaseOrder", purchase_order.PurchaseOrder{}},
	{"PurchaseOrderLine", purchase_order.PurchaseOrderLine{}},
	{"PurchaseOrderFreightCharge", purchase_order.FreightCharge{}},
	{"PurchaseOrderFreightAllocation", purchase_order.FreightAllocation{}},
	{"PurchaseOrderFreightUploadResponse", purchase_order.FreightUploadResponse{}},
	{"PurchaseOrderReorderRun", purchase_order.ReorderRun{}},
	{"PurchaseOrderReorderRecommendation", purchase_order.ReorderRecommendation{}},
	{"PurchaseOrderReorderTargetProposal", purchase_order.ReorderTargetProposal{}},
	{"PurchaseOrderRefreshResult", purchase_order.RefreshResult{}},
	{"EdiTradingPartner", edi.TradingPartner{}},
	{"EdiCatalogEntry", edi.CatalogEntry{}},
	{"MatchingResult", matching.MatchResult{}},
	{"MatchingLineDetail", matching.MatchLineDetail{}},
	{"MatchingConfig", matching.MatchConfig{}},
	{"MatchingException", matching.MatchException{}},
	{"TaxExemption", tax.TaxExemption{}},
	{"TaxLine", tax.TaxLine{}},
	{"TaxResult", tax.TaxResult{}},
	{"GlAccount", gl.GLAccount{}},
	{"GlJournalEntry", gl.JournalEntry{}},
	{"GlJournalLine", gl.JournalLine{}},
	{"GlFiscalPeriod", gl.FiscalPeriod{}},
	{"GlTrialBalanceRow", gl.TrialBalanceRow{}},
	{"GlAccountLineItem", gl.AccountLineItem{}},
	{"GlProfitAndLossReport", gl.ProfitAndLossReport{}},
	{"GlBalanceSheetReport", gl.BalanceSheetReport{}},
	{"ApVendorInvoice", ap.VendorInvoice{}},
	{"ApVendorInvoiceLine", ap.VendorInvoiceLine{}},
	{"ApPayment", ap.APPayment{}},
	{"ApAgingSummary", ap.APAgingSummary{}},
	{"BankreconBankAccount", bankrecon.BankAccount{}},
	{"BankreconTransaction", bankrecon.BankTransaction{}},
	{"BankreconSession", bankrecon.ReconciliationSession{}},
	{"BankreconImportResult", bankrecon.ImportResult{}},
	// events (R1-12b)
	{"Event", events.Item{}},
	{"EventEntity", events.EntityRef{}},
}

// TestSchemasMatchModelJsonTags enforces the transcription rule CONTRACT.md
// states, so it is enforced and not remembered: encoding/json writes a field
// without omitempty on every response (a nil pointer as null), and omits a
// field with omitempty whenever it is empty. So, per mapped schema:
//
//   - every json tag without omitempty must be listed in required,
//   - every json tag with omitempty must not be,
//   - every pointer (or in-band nullable SQL type) without omitempty, which
//     is serialized as null today, must carry the null leg in its type.
//
// A pointer WITH omitempty is never serialized as null (it is absent), and
// the fragments keep some of those nullable where a published seam has
// always sent optional-or-null shapes, so no rule pins their null leg here.
func TestSchemasMatchModelJsonTags(t *testing.T) {
	root, err := routecensus.FindModuleRoot(".")
	if err != nil {
		t.Fatalf("find module root: %v", err)
	}
	raw, err := os.ReadFile(root + "/api/openapi.yaml")
	if err != nil {
		t.Fatalf("read contract: %v", err)
	}
	var doc struct {
		Components struct {
			Schemas map[string]struct {
				Required   []string `yaml:"required"`
				Properties map[string]struct {
					Type  any `yaml:"type"`
					OneOf []struct {
						Type any `yaml:"type"`
					} `yaml:"oneOf"`
				} `yaml:"properties"`
			} `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse contract: %v", err)
	}

	var problems []string
	for _, bound := range modelBoundSchemas {
		schema, ok := doc.Components.Schemas[bound.Schema]
		if !ok {
			problems = append(problems, bound.Schema+": no such schema in the assembled document")
			continue
		}
		required := map[string]bool{}
		for _, name := range schema.Required {
			required[name] = true
		}
		for _, f := range jsonFields(reflect.TypeOf(bound.Model)) {
			name, opts := parseJSONTag(f)
			if name == "" {
				continue
			}
			where := bound.Schema + "." + name
			prop, ok := schema.Properties[name]
			if !ok {
				problems = append(problems, where+": the model serializes this field but the schema has no property for it")
				continue
			}
			omitempty := opts["omitempty"]
			if !omitempty && !required[name] {
				problems = append(problems, where+": serialized on every response (no omitempty) but missing from required")
			}
			if omitempty && required[name] {
				problems = append(problems, where+": omitted when empty (omitempty) but listed in required")
			}
			if !omitempty && isNullableGoType(f.Type) && !carriesNullLeg(prop.Type) && !oneOfCarriesNull(prop.OneOf) {
				problems = append(problems, where+": a nil pointer serializes as null today but the type carries no null leg")
			}
		}
	}

	if len(problems) > 0 {
		sort.Strings(problems)
		t.Fatalf("model bound schemas drifted from their Go models with %d problem(s):\n\t%s",
			len(problems), strings.Join(problems, "\n\t"))
	}
}

// jsonFields lists the struct's fields as encoding/json sees them: an
// embedded struct with no json tag is flattened into its parent (its fields
// are the parent's properties), everything else is one field.
func jsonFields(t reflect.Type) []reflect.StructField {
	var out []reflect.StructField
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.Anonymous && f.Tag.Get("json") == "" && f.Type.Kind() == reflect.Struct {
			out = append(out, jsonFields(f.Type)...)
			continue
		}
		out = append(out, f)
	}
	return out
}

// parseJSONTag returns the wire name of the field ("" when the field is not
// serialized) and its options.
func parseJSONTag(f reflect.StructField) (string, map[string]bool) {
	tag := f.Tag.Get("json")
	if tag == "" {
		return f.Name, map[string]bool{}
	}
	parts := strings.Split(tag, ",")
	name := parts[0]
	if name == "-" {
		return "", nil
	}
	opts := map[string]bool{}
	for _, opt := range parts[1:] {
		opts[opt] = true
	}
	return name, opts
}

// isNullableGoType reports whether a nil value of the type is serialized as
// null: pointers, and the in-band nullable database/sql and pgtype types
// should one appear in a mapped model.
func isNullableGoType(t reflect.Type) bool {
	if t.Kind() == reflect.Pointer {
		return true
	}
	s := t.String()
	return strings.HasPrefix(s, "sql.Null") || strings.HasPrefix(s, "pgtype.")
}

// carriesNullLeg reports whether an OpenAPI 3.1 type includes "null". Only
// the type array form counts: the 3.0 nullable keyword does not exist in 3.1.
func carriesNullLeg(typ any) bool {
	list, ok := typ.([]any)
	if !ok {
		return false
	}
	for _, item := range list {
		if s, ok := item.(string); ok && s == "null" {
			return true
		}
	}
	return false
}

// oneOfCarriesNull reports whether a oneOf list has a leg typed "null": the
// 3.1 spelling of a nullable $ref, which cannot take a type array beside it.
func oneOfCarriesNull(legs []struct {
	Type any `yaml:"type"`
}) bool {
	for _, leg := range legs {
		if s, ok := leg.Type.(string); ok && s == "null" {
			return true
		}
	}
	return false
}
