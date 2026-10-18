// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package middleware

import (
	"net/http"
	"sort"
	"strings"

	"github.com/gablelbm/gable/pkg/branchctx"
)

// The branch bound key's route allowlist (ADR 0007 section 5.5, deny by
// default). The opt in approach failed in kind: every route the sweep missed
// stayed open, and each review round found another. This table is the inverse:
// a branch bound machine key is refused every route it does not appear in, and
// every route a machine key can reach appears in it exactly once, with one of
// four classes:
//
//   - WALLED: the route runs behind the branch middleware, which honours the
//     pin the machine key core sets (lists, reads, drafts, the feed, links
//     and the payload guard of ADR 0007 section 2.3);
//   - CONFINED: the route mounts the key branch wall or the customer branch
//     wall, which holds the request to the pin by the branch, customer or row
//     it names (the note names the wall and the test that proves it);
//   - STATED LIMIT: the route is reachable unconfined by design (the note
//     carries its one line reason) and is listed in ADR 0007 section 5.5;
//   - REFUSED: a bound key is answered 403 forbidden with the
//     key.branch_refused audit row (the note carries the reason).
//
// The census test holds the table against core/api/ROUTES.txt in both
// directions, so a route added without a class decision, and a table entry
// whose route is gone, both fail CI. The one runtime check is the gate below
// (KeyBranchRouteGate), mounted once in serve's chain.

// KeyBranchRouteClass is the class of one route in the allowlist.
type KeyBranchRouteClass int

const (
	// KeyBranchClassWalled: the branch middleware holds the pin.
	KeyBranchClassWalled KeyBranchRouteClass = iota
	// KeyBranchClassConfined: the key branch wall or the customer branch wall
	// holds the pin.
	KeyBranchClassConfined
	// KeyBranchClassStatedLimit: reachable unconfined by design, stated in
	// ADR 0007 section 5.5.
	KeyBranchClassStatedLimit
	// KeyBranchClassRefused: a branch bound key is refused outright.
	KeyBranchClassRefused
)

// String names the class for tests, refusal messages and the census test's
// failure lines.
func (c KeyBranchRouteClass) String() string {
	switch c {
	case KeyBranchClassWalled:
		return "walled"
	case KeyBranchClassConfined:
		return "confined"
	case KeyBranchClassStatedLimit:
		return "stated_limit"
	case KeyBranchClassRefused:
		return "refused"
	}
	return "unknown"
}

// KeyBranchRouteNote pairs a class with its one line justification: a CONFINED
// entry names the wall and the test that proves it, a STATED LIMIT entry its
// reason, a REFUSED entry its reason, a WALLED entry its mount.
type KeyBranchRouteNote struct {
	Class KeyBranchRouteClass
	Note  string
}

// keyBranchRoutes is the allowlist itself: every route (method plus pattern,
// exactly as core/api/ROUTES.txt names them) a machine key can reach, with
// exactly one class. A route a machine key can reach that is not here, and a
// route here that no longer exists, fail the census test; at runtime either
// is refused, because the gate allows only what the table admits.
var keyBranchRoutes = map[string]KeyBranchRouteNote{
	"DELETE /api/v1/branches/{id}":                                 {KeyBranchClassConfined, "NamedBranch(id); TestKeyBranchPin_LocationWrites"},
	"DELETE /api/v1/locations/{id}":                                {KeyBranchClassConfined, "LocationRowBranch; TestKeyBranchPin_LocationWrites"},
	"DELETE /api/v1/pricing/category-rules/bulk":                   {KeyBranchClassConfined, "CustomerBranch(ids to rows customers); TestKeyBranchPin_CustomerConfinedWrites"},
	"DELETE /api/v1/pricing/category-rules/{id}":                   {KeyBranchClassConfined, "CustomerRowBranch; TestKeyBranchPin_CustomerConfinedWrites"},
	"DELETE /api/v1/tax/exemptions/{id}":                           {KeyBranchClassConfined, "CustomerRowBranch; TestKeyBranchPin_CustomerConfinedWrites"},
	"DELETE /api/v1/users/{sub}/branches/{branch_id}":              {KeyBranchClassConfined, "BodyBranch or NamedBranch(branch_id); TestKeyBranchPin_UserGrants"},
	"GET /api/v1/accounts/{id}":                                    {KeyBranchClassConfined, "CustomerBranch(path customer); TestKeyBranchPin_PosAndAccountRows"},
	"GET /api/v1/accounts/{id}/transactions":                       {KeyBranchClassConfined, "CustomerBranch(path customer); TestKeyBranchPin_PosAndAccountRows"},
	"GET /api/v1/ar/customers/{id}/credit-limit-changes":           {KeyBranchClassConfined, "CustomerBranch(path customer); TestKeyBranchPin_PosAndAccountRows"},
	"GET /api/v1/ar/customers/{id}/statement":                      {KeyBranchClassConfined, "CustomerBranch(path customer); TestKeyBranchPin_PosAndAccountRows"},
	"POST /api/v1/ar/customers/{id}/statement/email":               {KeyBranchClassConfined, "CustomerBranch(path customer); TestKeyBranchPin_DocumentRoutes"},
	"GET /api/v1/documents/print/statement/{id}":                   {KeyBranchClassConfined, "CustomerBranch(path customer); TestKeyBranchPin_DocumentRoutes"},
	"GET /api/v1/branches/{id}/users":                              {KeyBranchClassConfined, "NamedBranch(id); TestKeyBranchPin_LocationReads"},
	"POST /api/v1/gl/years/{year}/close":                           {KeyBranchClassRefused, "the year end close writes every branch's books"},
	"POST /api/v1/pricing/market-check":                            {KeyBranchClassRefused, "a run spends the dealer's FB Connect flow and reads the whole catalogue with the caller's yard as context; a bound key has no yard session"},
	"GET /api/v1/pricing/calculate":                                {KeyBranchClassConfined, "CustomerBranch(query customer_id, and job_id to projects.customer_id); TestKeyBranchPin_CustomerConfinedReads"},
	"GET /api/v1/pricing/category-rules":                           {KeyBranchClassConfined, "CustomerBranch(query customer_id, required); TestKeyBranchPin_CustomerConfinedReads"},
	"GET /api/v1/pricing/category-rules/{id}/audit":                {KeyBranchClassConfined, "CustomerRowBranch, tier rows refused; TestKeyBranchPin_CategoryTierRefusals"},
	"GET /api/v1/pricing/resolve":                                  {KeyBranchClassConfined, "CustomerBranch(query customer_id, when named); TestKeyBranchPin_CustomerConfinedReads"},
	"GET /api/v1/quotes/{id}/exposure":                             {KeyBranchClassConfined, "RowBranch(quote branch); TestKeyBranchPin_ExposureRoutes"},
	"GET /api/v1/tax/exemptions/{customerID}":                      {KeyBranchClassConfined, "CustomerBranch(path customer); TestKeyBranchPin_CustomerConfinedReads"},
	"GET /api/v1/users/{sub}/branches":                             {KeyBranchClassConfined, "the handler answers only the pin branch; TestKeyBranchPin_LocationReads"},
	"POST /api/v1/pricing/category-rules":                          {KeyBranchClassConfined, "CustomerBranch; an account rule element only, tier targets refused; TestKeyBranchPin_CategoryTierRefusals"},
	"POST /api/v1/pricing/category-rules/bulk":                     {KeyBranchClassConfined, "CustomerBranch; an account rule element only, tier elements refused; TestKeyBranchPin_CategoryTierRefusals"},
	"POST /api/v1/pricing/rules":                                   {KeyBranchClassConfined, "CustomerBranch(customer_id and job_id to projects.customer_id); TestKeyBranchPin_PricingRuleJobConfinement"},
	"POST /api/v1/quotes/{id}/exposure/acknowledge":                {KeyBranchClassConfined, "RowBranch(quote branch); TestKeyBranchPin_ExposureRoutes"},
	"POST /api/v1/quotes/{id}/exposure/escalate-now":               {KeyBranchClassConfined, "RowBranch(quote branch); TestKeyBranchPin_ExposureRoutes"},
	"POST /api/v1/quotes/{id}/exposure/override":                   {KeyBranchClassConfined, "RowBranch(quote branch); TestKeyBranchPin_ExposureRoutes"},
	"POST /api/v1/quotes/{id}/exposure/request-ack":                {KeyBranchClassConfined, "RowBranch(quote branch); TestKeyBranchPin_ExposureRoutes"},
	"POST /api/v1/tax/exemptions":                                  {KeyBranchClassConfined, "CustomerBodyBranch; TestKeyBranchPin_CustomerConfinedWrites"},
	"POST /api/v1/tax/preview":                                     {KeyBranchClassConfined, "CustomerBranch(body customer, when named); TestKeyBranchPin_CustomerConfinedReads"},
	"POST /api/v1/users/{sub}/branches":                            {KeyBranchClassConfined, "BodyBranch or NamedBranch(branch_id); TestKeyBranchPin_UserGrants"},
	"PUT /api/v1/branches/{id}":                                    {KeyBranchClassConfined, "NamedBranch(id); TestKeyBranchPin_LocationWrites"},
	"PUT /api/v1/locations/{id}":                                   {KeyBranchClassConfined, "LocationRowBranch; TestKeyBranchPin_LocationWrites"},
	"PUT /api/v1/pricing/category-rules/{id}":                      {KeyBranchClassConfined, "CustomerRowBranch; TestKeyBranchPin_CustomerConfinedWrites"},
	"PUT /api/v1/users/{sub}/home-branch":                          {KeyBranchClassConfined, "BodyBranch or NamedBranch(branch_id); TestKeyBranchPin_UserGrants"},
	"DELETE /api/v1/admin/settings/ai":                             {KeyBranchClassRefused, "a dealer wide configuration write (the provider key and base URL); the read stays a stated limit"},
	"DELETE /api/v1/admin/settings/routing":                        {KeyBranchClassRefused, "a dealer wide configuration write (the provider key and base URL); the read stays a stated limit"},
	"DELETE /api/v1/admin/staff/{id}/modules/{module_id}":          {KeyBranchClassRefused, "the staff roster and its module grants span every branch"},
	"DELETE /api/v1/delivery/drivers/{id}":                         {KeyBranchClassRefused, "driver rows carry personal data and no branch column"},
	"DELETE /api/v1/delivery/vehicles/{id}":                        {KeyBranchClassRefused, "the fleet is dealer wide, no branch column; the reads stay stated limits"},
	"DELETE /api/v1/edi/partners/{id}":                             {KeyBranchClassRefused, "a dealer wide trading partner write (raw transport configuration); the reads stay stated limits"},
	"PUT /api/v1/dealer-profile":                                   {KeyBranchClassRefused, "the dealer profile is one dealer wide row, no branch column; the reads stay stated limits"},
	"PUT /api/v1/dealer-profile/logo":                              {KeyBranchClassRefused, "the dealer profile is one dealer wide row, no branch column; the reads stay stated limits"},
	"DELETE /api/v1/products/{id}/pim/collateral/{collateralId}":   {KeyBranchClassRefused, "product content is dealer wide, no branch column; the reads stay stated limits"},
	"DELETE /api/v1/products/{id}/pim/media/{mediaId}":             {KeyBranchClassRefused, "product content is dealer wide, no branch column; the reads stay stated limits"},
	"DELETE /api/v1/reporting/saved/{id}":                          {KeyBranchClassRefused, "reports span every branch"},
	"DELETE /api/v1/reporting/schedules/{id}":                      {KeyBranchClassRefused, "reports span every branch"},
	"GET /api/v1/admin/staff":                                      {KeyBranchClassRefused, "the staff roster and its module grants span every branch"},
	"GET /api/v1/admin/staff/{id}":                                 {KeyBranchClassRefused, "the staff roster and its module grants span every branch"},
	"GET /api/v1/ap/aging":                                         {KeyBranchClassRefused, "payables stay dealer wide: AP bills carry a branch now, so this is a policy choice, not a limit"},
	"GET /api/v1/ap/invoices":                                      {KeyBranchClassRefused, "payables stay dealer wide: AP bills carry a branch now, so this is a policy choice, not a limit"},
	"GET /api/v1/ap/invoices/{id}":                                 {KeyBranchClassRefused, "payables stay dealer wide: AP bills carry a branch now, so this is a policy choice, not a limit"},
	"GET /api/v1/ap/payments":                                      {KeyBranchClassRefused, "payables stay dealer wide: AP bills carry a branch now, so this is a policy choice, not a limit"},
	"GET /api/v1/ar/finance-charges/rules":                         {KeyBranchClassRefused, "finance charge rules are dealer wide settings"},
	"GET /api/v1/ar/finance-charges/runs":                          {KeyBranchClassRefused, "finance charge runs span every branch's books"},
	"GET /api/v1/ar/finance-charges/runs/{id}":                     {KeyBranchClassRefused, "finance charge runs span every branch's books"},
	"GET /api/v1/ar/reconciliation":                                {KeyBranchClassRefused, "the reconciliation reads every branch's books; its ledger half carries no branch fact"},
	"GET /api/v1/ar/statements/runs":                               {KeyBranchClassRefused, "statement runs span every branch's books"},
	"GET /api/v1/ar/statements/runs/{id}":                          {KeyBranchClassRefused, "statement runs span every branch's books"},
	"GET /api/v1/bankrecon/accounts":                               {KeyBranchClassRefused, "bank reconciliation spans every branch"},
	"GET /api/v1/bankrecon/sessions":                               {KeyBranchClassRefused, "bank reconciliation spans every branch"},
	"GET /api/v1/bankrecon/sessions/{id}":                          {KeyBranchClassRefused, "bank reconciliation spans every branch"},
	"GET /api/v1/delivery/drivers":                                 {KeyBranchClassRefused, "driver rows carry personal data and no branch column"},
	"GET /api/v1/delivery/drivers/{id}":                            {KeyBranchClassRefused, "driver rows carry personal data and no branch column"},
	"GET /api/v1/events":                                           {KeyBranchClassRefused, "the feed carries every branch events"},
	"GET /api/v1/gl/accounts":                                      {KeyBranchClassRefused, "the books span every branch"},
	"GET /api/v1/gl/balance-sheet":                                 {KeyBranchClassRefused, "the books span every branch"},
	"GET /api/v1/gl/detail":                                        {KeyBranchClassRefused, "the books span every branch"},
	"GET /api/v1/gl/fiscal-periods":                                {KeyBranchClassRefused, "the books span every branch"},
	"GET /api/v1/gl/journal-entries":                               {KeyBranchClassRefused, "the books span every branch"},
	"GET /api/v1/gl/journal-entries/{id}":                          {KeyBranchClassRefused, "the books span every branch"},
	"GET /api/v1/gl/profit-and-loss":                               {KeyBranchClassRefused, "the books span every branch"},
	"GET /api/v1/gl/tie-out":                                       {KeyBranchClassRefused, "the books span every branch"},
	"GET /api/v1/gl/trial-balance":                                 {KeyBranchClassRefused, "the books span every branch"},
	"GET /api/v1/pricing/matrix":                                   {KeyBranchClassRefused, "the unfiltered matrix cannot be confined to a branch"},
	// The pricing catalogue (ADR 0006 section 5.6): the reads show the
	// caller's context branch rows and the every branch rows; a write
	// carrying a branch_id passes the payload branch rule, and a null branch
	// (the every branch slice, a contract or vendor cost for every branch) is
	// refused the bound key outright by CheckEveryBranch.
	"GET /api/v1/pricing/levels":               {KeyBranchClassStatedLimit, "level reference data; the write is dealer wide pricing policy"},
	"GET /api/v1/pricing/levels/{id}":          {KeyBranchClassStatedLimit, "level reference data"},
	"GET /api/v1/pricing/profiles":             {KeyBranchClassStatedLimit, "profile reference data"},
	"GET /api/v1/pricing/profiles/{id}":        {KeyBranchClassStatedLimit, "profile reference data"},
	"GET /api/v1/pricing/products/{id}/prices": {KeyBranchClassWalled, "BranchIDForQuery walls the rows; the query branch passes CheckPayloadBranch"},
	"GET /api/v1/pricing/vendor-costs":         {KeyBranchClassWalled, "the branch wall holds; a query branch passes CheckPayloadBranch"},
	"GET /api/v1/pricing/vendor-levels":        {KeyBranchClassRefused, "a vendor's buying programs are dealer wide purchasing data"},
	"GET /api/v1/pricing/vendor-levels/{id}":   {KeyBranchClassRefused, "a vendor's buying programs are dealer wide purchasing data"},
	"GET /api/v1/pricing/vendor-costs/{id}":    {KeyBranchClassWalled, "the row's branch holds"},
	"GET /api/v1/pricing/contracts":            {KeyBranchClassWalled, "the branch wall holds; a query branch passes CheckPayloadBranch"},
	"GET /api/v1/pricing/contracts/{id}":       {KeyBranchClassWalled, "the row's branch holds"},
	"GET /api/v1/pricing/cost":                 {KeyBranchClassWalled, "the branch wall holds on the read"},
	"POST /api/v1/pricing/levels":              {KeyBranchClassStatedLimit, "a level is dealer wide pricing policy"},
	"PUT /api/v1/pricing/levels/{id}":          {KeyBranchClassStatedLimit, "a level is dealer wide pricing policy"},
	"POST /api/v1/pricing/profiles":            {KeyBranchClassStatedLimit, "a profile is dealer wide pricing policy"},
	"PUT /api/v1/pricing/profiles/{id}":        {KeyBranchClassStatedLimit, "a profile is dealer wide pricing policy"},
	"PUT /api/v1/pricing/products/{id}/prices": {KeyBranchClassWalled, "the slice's branch passes CheckPayloadBranch; a null branch is CheckEveryBranch"},
	"POST /api/v1/pricing/vendor-levels":       {KeyBranchClassRefused, "a vendor's buying programs are dealer wide purchasing data"},
	"PUT /api/v1/pricing/vendor-levels/{id}":   {KeyBranchClassRefused, "a vendor's buying programs are dealer wide purchasing data"},
	"POST /api/v1/pricing/vendor-costs":        {KeyBranchClassWalled, "the branch named passes CheckPayloadBranch; a null branch is CheckEveryBranch"},
	"PUT /api/v1/pricing/vendor-costs/{id}":    {KeyBranchClassWalled, "the stored branch and the new one pass the rule"},
	"POST /api/v1/pricing/contracts":           {KeyBranchClassWalled, "the branch named passes CheckPayloadBranch; a null branch is CheckEveryBranch"},
	"PUT /api/v1/pricing/contracts/{id}":       {KeyBranchClassWalled, "the stored branch passes the rule"},
	"DELETE /api/v1/pricing/contracts/{id}":    {KeyBranchClassWalled, "the stored branch passes the rule"},
	"GET /api/v1/pricing/rules":                                    {KeyBranchClassRefused, "the unfiltered rule list names every customer"},
	"GET /api/v1/quotes/exposure":                                  {KeyBranchClassRefused, "the list returns every branch exposure"},
	"GET /api/v1/reporting/export/{entity}":                        {KeyBranchClassRefused, "reports span every branch"},
	"GET /api/v1/reporting/saved":                                  {KeyBranchClassRefused, "reports span every branch"},
	"GET /api/v1/reporting/saved/{id}":                             {KeyBranchClassRefused, "reports span every branch"},
	"GET /api/v1/reporting/schedules":                              {KeyBranchClassRefused, "reports span every branch"},
	"GET /api/v1/reports/ar-aging":                                 {KeyBranchClassRefused, "reports span every branch"},
	"GET /api/v1/reports/customer-statement/{id}":                  {KeyBranchClassRefused, "reports span every branch"},
	"GET /api/v1/reports/daily-till":                               {KeyBranchClassRefused, "reports span every branch"},
	"GET /api/v1/reports/exposure":                                 {KeyBranchClassRefused, "reports span every branch"},
	"GET /api/v1/reports/sales-summary":                            {KeyBranchClassRefused, "reports span every branch"},
	"GET /api/v1/sales-team":                                       {KeyBranchClassRefused, "the sales team roster spans every branch"},
	"GET /api/v1/sales-team/{id}":                                  {KeyBranchClassRefused, "the sales team roster spans every branch"},
	"GET /api/v1/users":                                            {KeyBranchClassRefused, "the known users list spans every branch"},
	"PATCH /api/v1/pos/transactions/{id}/items/{itemId}":           {KeyBranchClassWalled, "pos_transactions.branch_id (the sale is read and locked before the write); TestKeyBranchPin_PosAndAccountRows"},
	"PATCH /api/v1/products/{id}/dimensions":                       {KeyBranchClassRefused, "the product catalogue is dealer wide, no branch column; the reads stay stated limits"},
	"PATCH /api/v1/products/{id}/lead-time":                        {KeyBranchClassRefused, "the product catalogue is dealer wide, no branch column; the reads stay stated limits"},
	"PATCH /api/v1/products/{id}/margins":                          {KeyBranchClassRefused, "the product catalogue is dealer wide, no branch column; the reads stay stated limits"},
	"PATCH /api/v1/products/{id}/pim/media/{mediaId}/primary":      {KeyBranchClassRefused, "product content is dealer wide, no branch column; the reads stay stated limits"},
	"POST /api/v1/admin/exposure-scan":                             {KeyBranchClassRefused, "the scan re-checks every branch quotes"},
	"POST /api/v1/admin/staff":                                     {KeyBranchClassRefused, "the staff roster and its module grants span every branch"},
	"POST /api/v1/admin/staff/{id}/modules":                        {KeyBranchClassRefused, "the staff roster and its module grants span every branch"},
	"POST /api/v1/ap/invoices":                                     {KeyBranchClassRefused, "payables stay dealer wide: AP bills carry a branch now, so this is a policy choice, not a limit"},
	"POST /api/v1/ap/invoices/{id}/transitions":                    {KeyBranchClassRefused, "payables stay dealer wide: AP bills carry a branch now, so this is a policy choice, not a limit"},
	"POST /api/v1/ap/payments":                                     {KeyBranchClassRefused, "payables stay dealer wide: AP bills carry a branch now, so this is a policy choice, not a limit"},
	"POST /api/v1/apps/{key}/disable":                              {KeyBranchClassRefused, "an app enablement switch is a dealer wide write"},
	"POST /api/v1/apps/{key}/enable":                               {KeyBranchClassRefused, "an app enablement switch is a dealer wide write"},
	"POST /api/v1/ar/credit-holds/evaluate":                        {KeyBranchClassRefused, "credit hold evaluation sweeps every branch's customers and open invoices"},
	"POST /api/v1/ar/finance-charges/runs":                         {KeyBranchClassRefused, "finance charge runs span every branch's books"},
	"POST /api/v1/ar/statements/runs":                              {KeyBranchClassRefused, "statement runs span every branch's books"},
	"POST /api/v1/bankrecon/accounts":                              {KeyBranchClassRefused, "bank reconciliation spans every branch"},
	"POST /api/v1/bankrecon/import":                                {KeyBranchClassRefused, "bank reconciliation spans every branch"},
	"POST /api/v1/bankrecon/match":                                 {KeyBranchClassRefused, "bank reconciliation spans every branch"},
	"POST /api/v1/bankrecon/sessions":                              {KeyBranchClassRefused, "bank reconciliation spans every branch"},
	"POST /api/v1/bankrecon/sessions/{id}/complete":                {KeyBranchClassRefused, "bank reconciliation spans every branch"},
	"POST /api/v1/bankrecon/unmatch":                               {KeyBranchClassRefused, "bank reconciliation spans every branch"},
	"POST /api/v1/branches":                                        {KeyBranchClassRefused, "the branch directory create is outside every pin"},
	"POST /api/v1/charge-codes":                                    {KeyBranchClassRefused, "charge codes carry a revenue account and no branch column; the reads stay stated limits"},
	"POST /api/v1/delivery/drivers":                                {KeyBranchClassRefused, "driver rows carry personal data and no branch column"},
	"POST /api/v1/delivery/drivers/{id}/photo":                     {KeyBranchClassRefused, "driver rows carry personal data and no branch column"},
	"POST /api/v1/delivery/vehicles":                               {KeyBranchClassRefused, "the fleet is dealer wide, no branch column; the reads stay stated limits"},
	"POST /api/v1/delivery/vehicles/{id}/photo":                    {KeyBranchClassRefused, "the fleet is dealer wide, no branch column; the reads stay stated limits"},
	"POST /api/v1/edi/partners":                                    {KeyBranchClassRefused, "a dealer wide trading partner write (raw transport configuration); the reads stay stated limits"},
	"POST /api/v1/edi/partners/{id}/import-catalog":                {KeyBranchClassRefused, "the catalog import rewrites the vendor map for every branch"},
	"POST /api/v1/gl/accounts":                                     {KeyBranchClassRefused, "the books span every branch"},
	"POST /api/v1/gl/fiscal-periods/{id}/close":                    {KeyBranchClassRefused, "the books span every branch"},
	"POST /api/v1/gl/fiscal-periods/{id}/reopen":                   {KeyBranchClassRefused, "the books span every branch"},
	"POST /api/v1/gl/journal-entries":                              {KeyBranchClassRefused, "the books span every branch"},
	"POST /api/v1/gl/journal-entries/{id}/post":                    {KeyBranchClassRefused, "the books span every branch"},
	"POST /api/v1/gl/journal-entries/{id}/reverse":                 {KeyBranchClassRefused, "the books span every branch"},
	"POST /api/v1/gl/journal-entries/{id}/void":                    {KeyBranchClassRefused, "the books span every branch"},
	"POST /api/v1/governance/rfcs":                                 {KeyBranchClassRefused, "the rfc workflow carries no branch fact; the reads stay stated limits"},
	"POST /api/v1/governance/rfcs/{id}/transitions":                {KeyBranchClassRefused, "the rfc workflow carries no branch fact; the reads stay stated limits"},
	"POST /api/v1/market-indices/{id}/refresh":                     {KeyBranchClassRefused, "the index refresh re-checks every branch quotes"},
	"POST /api/v1/market-indices/{id}/refresh/preview":             {KeyBranchClassRefused, "the index refresh re-checks every branch quotes"},
	"POST /api/v1/millwork/options":                                {KeyBranchClassRefused, "the option catalogue is dealer wide; the reads stay stated limits"},
	"POST /api/v1/payment-terms":                                   {KeyBranchClassRefused, "payment terms are dealer wide, no branch column; the reads stay stated limits"},
	"POST /api/v1/pricing/categories":                              {KeyBranchClassRefused, "the product category tree is dealer wide; the reads stay stated limits"},
	"POST /api/v1/pricing/rebates/programs":                        {KeyBranchClassRefused, "a vendor rebate program is a dealer wide financial definition; the reads stay stated limits"},
	"POST /api/v1/products":                                        {KeyBranchClassRefused, "the product catalogue is dealer wide, no branch column; the reads stay stated limits"},
	"POST /api/v1/products/{id}/pim/generate/collateral":           {KeyBranchClassRefused, "product content is dealer wide and the generate routes spend the dealer AI credit"},
	"POST /api/v1/products/{id}/pim/generate/descriptions":         {KeyBranchClassRefused, "product content is dealer wide and the generate routes spend the dealer AI credit"},
	"POST /api/v1/products/{id}/pim/generate/image":                {KeyBranchClassRefused, "product content is dealer wide and the generate routes spend the dealer AI credit"},
	"POST /api/v1/products/{id}/pim/generate/seo":                  {KeyBranchClassRefused, "product content is dealer wide and the generate routes spend the dealer AI credit"},
	"POST /api/v1/purchase-orders/refresh-reorder-targets":         {KeyBranchClassRefused, "the reorder refresh rewrites reorder targets for every branch (dry_run or not)"},
	"POST /api/v1/reporting/builder/export":                        {KeyBranchClassRefused, "reports span every branch"},
	"POST /api/v1/reporting/builder/preview":                       {KeyBranchClassRefused, "reports span every branch"},
	"POST /api/v1/reporting/save":                                  {KeyBranchClassRefused, "reports span every branch"},
	"POST /api/v1/reporting/saved/{id}/run":                        {KeyBranchClassRefused, "reports span every branch"},
	"POST /api/v1/reporting/schedules":                             {KeyBranchClassRefused, "reports span every branch"},
	"POST /api/v1/units":                                           {KeyBranchClassRefused, "the unit catalogue is dealer wide; the reads stay stated limits"},
	"POST /api/v1/vendors":                                         {KeyBranchClassRefused, "the vendor master is dealer wide; the read stays a stated limit"},
	"PUT /api/v1/admin/modules/{id}":                               {KeyBranchClassRefused, "a module kill switch is a dealer wide write; the read stays a stated limit"},
	"PUT /api/v1/admin/settings/ai":                                {KeyBranchClassRefused, "a dealer wide configuration write (the provider key and base URL); the read stays a stated limit"},
	"POST /api/v1/admin/settings/ai/test":                          {KeyBranchClassRefused, "a dealer wide configuration action that spends the provider key; no branch binds it"},
	"PUT /api/v1/admin/market-check/settings":                      {KeyBranchClassRefused, "a dealer wide configuration write (the FB Connect address and the webhook id); the read stays a stated limit"},
	"PUT /api/v1/admin/settings/routing":                           {KeyBranchClassRefused, "a dealer wide configuration write (the provider key and base URL); the read stays a stated limit"},
	"PUT /api/v1/admin/staff/{id}":                                 {KeyBranchClassRefused, "the staff roster and its module grants span every branch"},
	"PUT /api/v1/ar/finance-charges/rules":                         {KeyBranchClassRefused, "finance charge rules are dealer wide settings"},
	"PUT /api/v1/charge-codes/{id}":                                {KeyBranchClassRefused, "charge codes carry a revenue account and no branch column; the reads stay stated limits"},
	"PUT /api/v1/delivery/drivers/{id}":                            {KeyBranchClassRefused, "driver rows carry personal data and no branch column"},
	"PUT /api/v1/delivery/vehicles/{id}":                           {KeyBranchClassRefused, "the fleet is dealer wide, no branch column; the reads stay stated limits"},
	"PUT /api/v1/edi/partners/{id}":                                {KeyBranchClassRefused, "a dealer wide trading partner write (raw transport configuration); the reads stay stated limits"},
	"PUT /api/v1/gl/accounts/{id}":                                 {KeyBranchClassRefused, "the books span every branch"},
	"PUT /api/v1/governance/rfcs/{id}":                             {KeyBranchClassRefused, "the rfc workflow carries no branch fact; the reads stay stated limits"},
	"PUT /api/v1/market-indices/{id}":                              {KeyBranchClassRefused, "index metadata feeds every branch escalators; the reads stay stated limits"},
	"PUT /api/v1/matching/config":                                  {KeyBranchClassRefused, "match tolerances are a dealer wide financial control; the read stays a stated limit"},
	"PUT /api/v1/payment-terms/{id}":                               {KeyBranchClassRefused, "payment terms are dealer wide, no branch column; the reads stay stated limits"},
	"PUT /api/v1/pricing/categories/{id}":                          {KeyBranchClassRefused, "the product category tree is dealer wide; the reads stay stated limits"},
	"PUT /api/v1/products/{id}/kit-components":                     {KeyBranchClassRefused, "the product catalogue is dealer wide, no branch column; the reads stay stated limits"},
	"PUT /api/v1/products/{id}/pim/content":                        {KeyBranchClassRefused, "product content is dealer wide, no branch column; the reads stay stated limits"},
	"PUT /api/v1/products/{id}/units":                              {KeyBranchClassRefused, "the product catalogue is dealer wide, no branch column; the reads stay stated limits"},
	"PUT /api/v1/reporting/saved/{id}":                             {KeyBranchClassRefused, "reports span every branch"},
	"PUT /api/v1/units/{code}":                                     {KeyBranchClassRefused, "the unit catalogue is dealer wide; the reads stay stated limits"},
	"GET /api/v1/admin/modules":                                    {KeyBranchClassStatedLimit, "module kill switches change dealer wide behaviour"},
	"GET /api/v1/admin/settings/ai":                                {KeyBranchClassStatedLimit, "dealer wide configuration"},
	"GET /api/v1/admin/market-check/settings":                      {KeyBranchClassStatedLimit, "dealer wide configuration; the webhook id is never answered"},
	"GET /api/v1/admin/settings/routing":                           {KeyBranchClassStatedLimit, "dealer wide configuration"},
	"GET /api/v1/apps":                                             {KeyBranchClassStatedLimit, "app enablement switches change dealer wide behaviour"},
	"GET /api/v1/branches":                                         {KeyBranchClassStatedLimit, "branch directory reference data (the PR 39 decision)"},
	"GET /api/v1/branches/{id}":                                    {KeyBranchClassStatedLimit, "branch directory reference data (the PR 39 decision)"},
	"GET /api/v1/charge-codes":                                     {KeyBranchClassStatedLimit, "the dealer wide charge code vocabulary"},
	"GET /api/v1/charge-codes/{id}":                                {KeyBranchClassStatedLimit, "the dealer wide charge code vocabulary"},
	"GET /api/v1/configurator/options":                             {KeyBranchClassStatedLimit, "option catalog configuration, no branch fact"},
	"GET /api/v1/configurator/presets":                             {KeyBranchClassStatedLimit, "option catalog configuration, no branch fact"},
	"GET /api/v1/configurator/rules":                               {KeyBranchClassStatedLimit, "option catalog configuration, no branch fact"},
	"GET /api/v1/delivery/vehicles":                                {KeyBranchClassStatedLimit, "the fleet is dealer wide, no branch column"},
	"GET /api/v1/delivery/vehicles/{id}":                           {KeyBranchClassStatedLimit, "the fleet is dealer wide, no branch column"},
	"GET /api/v1/edi/partners":                                     {KeyBranchClassStatedLimit, "trading partners and catalogs, no branch fact"},
	"GET /api/v1/edi/partners/{id}":                                {KeyBranchClassStatedLimit, "trading partners and catalogs, no branch fact"},
	"GET /api/v1/edi/partners/{id}/catalog":                        {KeyBranchClassStatedLimit, "trading partners and catalogs, no branch fact"},
	"GET /api/v1/governance/rfcs":                                  {KeyBranchClassStatedLimit, "the rfc workflow carries no branch fact"},
	"GET /api/v1/governance/rfcs/{id}":                             {KeyBranchClassStatedLimit, "the rfc workflow carries no branch fact"},
	"GET /api/v1/links/products/{id}":                              {KeyBranchClassStatedLimit, "the dealer wide product link"},
	"GET /api/v1/market-indices":                                   {KeyBranchClassStatedLimit, "dealer wide index reference data"},
	"GET /api/v1/market-indices/{id}/history":                      {KeyBranchClassStatedLimit, "dealer wide index reference data"},
	"GET /api/v1/matching/config":                                  {KeyBranchClassStatedLimit, "match tolerances are dealer wide configuration"},
	"GET /api/v1/millwork/options":                                 {KeyBranchClassStatedLimit, "option catalog data, no branch fact"},
	"GET /api/v1/millwork/options/{id}":                            {KeyBranchClassStatedLimit, "option catalog data, no branch fact"},
	"GET /api/v1/payment-terms":                                    {KeyBranchClassStatedLimit, "the dealer wide payment terms vocabulary"},
	"GET /api/v1/payment-terms/{id}":                               {KeyBranchClassStatedLimit, "the dealer wide payment terms vocabulary"},
	"GET /api/v1/pos/catalog":                                      {KeyBranchClassStatedLimit, "the dealer wide product catalogue"},
	"GET /api/v1/pos/customers":                                    {KeyBranchClassWalled, "customer_branches (ruling A at the counter: a user finds every customer, a bound key keeps its pin); the counter's lookup reads only what the pin reaches"},
	"GET /api/v1/pos/registers":                                    {KeyBranchClassWalled, "the register's location branch, else pos_registers.branch_id, active registers only (a bound key sees only its pin's registers); TestRegistersListHoldsTheBranchWalls"},
	"GET /api/v1/pos/products/search":                              {KeyBranchClassStatedLimit, "the dealer wide product catalogue"},
	"GET /api/v1/pos/tiles":                                        {KeyBranchClassWalled, "the yard's sales history and on hand follow the caller's branch (a bound key keeps its pin); the product list itself is the dealer wide catalogue; TestTilesHoldTheCallersReach"},
	"GET /api/v1/pricing/categories":                               {KeyBranchClassStatedLimit, "product category reference data, no branch fact"},
	"GET /api/v1/pricing/rebates/programs":                         {KeyBranchClassStatedLimit, "programs are dealer wide and a claim uses supplied volumes"},
	"GET /api/v1/pricing/rebates/programs/{id}":                    {KeyBranchClassStatedLimit, "programs are dealer wide and a claim uses supplied volumes"},
	"GET /api/v1/pricing/rebates/programs/{id}/claims":             {KeyBranchClassStatedLimit, "programs are dealer wide and a claim uses supplied volumes"},
	"GET /api/v1/products":                                         {KeyBranchClassStatedLimit, "the dealer wide product catalogue (stock totals narrow by branch)"},
	"GET /api/v1/products/{id}":                                    {KeyBranchClassStatedLimit, "the dealer wide product catalogue (stock totals narrow by branch)"},
	"GET /api/v1/products/{id}/detail":                             {KeyBranchClassStatedLimit, "the dealer wide product catalogue (stock totals narrow by branch)"},
	"GET /api/v1/products/{id}/kit-components":                     {KeyBranchClassStatedLimit, "the dealer wide product catalogue"},
	"GET /api/v1/products/{id}/pim/collateral":                     {KeyBranchClassStatedLimit, "the dealer wide product content"},
	"GET /api/v1/products/{id}/pim/content":                        {KeyBranchClassStatedLimit, "the dealer wide product content"},
	"GET /api/v1/products/{id}/pim/media":                          {KeyBranchClassStatedLimit, "the dealer wide product content"},
	"GET /api/v1/products/{id}/units":                              {KeyBranchClassStatedLimit, "the dealer wide product unit sets"},
	"GET /api/v1/units":                                            {KeyBranchClassStatedLimit, "the dealer wide unit catalogue"},
	"GET /api/v1/dealer-profile":                                   {KeyBranchClassStatedLimit, "the one dealer wide profile every printed form shows"},
	"GET /api/v1/dealer-profile/locale":                            {KeyBranchClassStatedLimit, "the one dealer wide locale, read alone so every role's pages format their dates for it"},
	"GET /api/v1/dealer-profile/logo":                              {KeyBranchClassStatedLimit, "the one dealer wide profile every printed form shows"},
	"GET /api/v1/units/{code}":                                     {KeyBranchClassStatedLimit, "the dealer wide unit catalogue"},
	"GET /api/v1/vendors":                                          {KeyBranchClassStatedLimit, "the dealer wide vendor master"},
	"GET /api/v1/vendors/{id}":                                     {KeyBranchClassStatedLimit, "the dealer wide vendor master"},
	"POST /api/v1/configurator/build-sku":                          {KeyBranchClassStatedLimit, "option catalog configuration, no branch fact"},
	"POST /api/v1/configurator/validate":                           {KeyBranchClassStatedLimit, "option catalog configuration, no branch fact"},
	"POST /api/v1/parsing/upload":                                  {KeyBranchClassStatedLimit, "the upload parses dealer wide product data"},
	"POST /api/v1/pricing/calculate-escalation":                    {KeyBranchClassStatedLimit, "a calculator over supplied inputs, no branch fact"},
	"POST /api/v1/pricing/rebates/programs/{id}/claims/calculate":  {KeyBranchClassStatedLimit, "programs are dealer wide and a claim uses supplied volumes"},
	"POST /api/v1/vision/scan":                                     {KeyBranchClassStatedLimit, "the blueprint scan prototype carries no branch fact"},
	"DELETE /api/v1/activities/{id}":                               {KeyBranchClassWalled, "crm_activities.customer_id to customer_branches; on a shared customer a bound key never deletes one (no branch tag; TestKeyBranchPin_ActivityFollowsTheContactRule)"},
	"DELETE /api/v1/contacts/{id}":                                 {KeyBranchClassWalled, "contacts.customer_id to customer_branches"},
	"DELETE /api/v1/pos/transactions/{id}/items/{itemId}":          {KeyBranchClassWalled, "pos_transactions.branch_id (the delete joins it); TestKeyBranchPin_PosAndAccountRows"},
	"GET /api/v1/activities/{id}":                                  {KeyBranchClassWalled, "crm_activities.customer_id to customer_branches"},
	"GET /api/v1/ar/aging":                                         {KeyBranchClassWalled, "invoices and credit memos branch_id (the account repository wall)"},
	"GET /api/v1/ar/aging/summary":                                 {KeyBranchClassWalled, "invoices and credit memos branch_id (the account repository wall)"},
	"GET /api/v1/ar/credit-holds":                                  {KeyBranchClassWalled, "credit_holds.customer_id to customer_branches"},
	"GET /api/v1/ar/worklist":                                      {KeyBranchClassWalled, "invoices and credit memos branch_id (the account repository wall)"},
	"GET /api/v1/branches/{id}/tree":                               {KeyBranchClassWalled, "locations.branch_id (the tree walks the branch middleware context)"},
	"GET /api/v1/contacts/{id}":                                    {KeyBranchClassWalled, "contacts.customer_id to customer_branches"},
	"GET /api/v1/credit-memos":                                     {KeyBranchClassWalled, "credit_memos.branch_id"},
	"GET /api/v1/credit-memos/{id}":                                {KeyBranchClassWalled, "credit_memos.branch_id"},
	"GET /api/v1/customers":                                        {KeyBranchClassWalled, "customer_branches (the customer repository predicate)"},
	"GET /api/v1/customers/{customerId}/activities":                {KeyBranchClassWalled, "crm_activities.customer_id to customer_branches"},
	"GET /api/v1/customers/{customerId}/contacts":                  {KeyBranchClassWalled, "contacts.customer_id to customer_branches"},
	"GET /api/v1/customers/{id}":                                   {KeyBranchClassWalled, "customer_branches (a customer of another branch reads as missing)"},
	"GET /api/v1/customers/{id}/escalation-policy":                 {KeyBranchClassWalled, "customer_branches"},
	"GET /api/v1/customers/{id}/ship-tos":                          {KeyBranchClassWalled, "customer_ship_tos.customer_id to customer_branches"},
	"GET /api/v1/dashboard/inventory-alerts":                       {KeyBranchClassWalled, "inventory.location_id to locations.branch_id (the dashboard predicate)"},
	"GET /api/v1/dashboard/order-activity":                         {KeyBranchClassWalled, "orders.branch_id (the dashboard predicate)"},
	"GET /api/v1/dashboard/revenue-trend":                          {KeyBranchClassWalled, "invoices.branch_id (the dashboard predicate)"},
	"GET /api/v1/dashboard/summary":                                {KeyBranchClassWalled, "orders and invoices branch_id (the dashboard predicate)"},
	"GET /api/v1/dashboard/top-customers":                          {KeyBranchClassWalled, "invoices.branch_id (the dashboard predicate)"},
	"GET /api/v1/delivery/deliveries/{id}":                         {KeyBranchClassWalled, "deliveries.order_id to orders.branch_id (stopVisible)"},
	"GET /api/v1/delivery/deliveries/{id}/pod-photos":              {KeyBranchClassWalled, "deliveries.order_id to orders.branch_id (stopVisible)"},
	"GET /api/v1/scheduling/windows":                               {KeyBranchClassWalled, "the branch middleware holds the pin; the list is filtered by the caller's reach"},
	"GET /api/v1/scheduling/windows/{id}":                          {KeyBranchClassWalled, "the branch middleware holds the pin"},
	"POST /api/v1/scheduling/windows":                              {KeyBranchClassWalled, "the branch middleware holds the pin; the body's yard is held by the branch guard"},
	"PUT /api/v1/scheduling/windows/{id}":                          {KeyBranchClassWalled, "the branch middleware holds the pin; the window's yard never moves"},
	"DELETE /api/v1/scheduling/windows/{id}":                       {KeyBranchClassWalled, "the branch middleware holds the pin"},
	"GET /api/v1/delivery/routes":                                  {KeyBranchClassWalled, "delivery_routes stops to their orders branch (routeVisible; a route with no stops is visible by design)"},
	"GET /api/v1/delivery/routes/{id}":                             {KeyBranchClassWalled, "delivery_routes stops to their orders branch (routeVisible)"},
	"GET /api/v1/delivery/routes/{id}/deliveries":                  {KeyBranchClassWalled, "delivery_routes stops to their orders branch (routeVisible)"},
	"GET /api/v1/documents/print/invoice/{id}":                     {KeyBranchClassWalled, "invoices.branch_id"},
	"GET /api/v1/documents/print/pickticket/{id}":                  {KeyBranchClassWalled, "orders.branch_id"},
	"GET /api/v1/documents/print/purchase-order/{id}":              {KeyBranchClassWalled, "purchase_orders.branch_id (the handler holds the record to the wall)"},
	"GET /api/v1/documents/print/quote/{id}":                       {KeyBranchClassWalled, "quotes.branch_id"},
	"GET /api/v1/email-deliveries":                                 {KeyBranchClassWalled, "email_deliveries.branch_id (the carried document's branch); a row of no branch is hidden from a walled caller"},
	"GET /api/v1/drafts/orders":                                    {KeyBranchClassWalled, "drafts.branch_id"},
	"GET /api/v1/drafts/orders/feed":                               {KeyBranchClassWalled, "drafts.branch_id"},
	"GET /api/v1/drafts/orders/{id}":                               {KeyBranchClassWalled, "drafts.branch_id"},
	"GET /api/v1/drafts/quotes":                                    {KeyBranchClassWalled, "drafts.branch_id"},
	"GET /api/v1/drafts/quotes/feed":                               {KeyBranchClassWalled, "drafts.branch_id"},
	"GET /api/v1/drafts/quotes/{id}":                               {KeyBranchClassWalled, "drafts.branch_id"},
	"GET /api/v1/inventory":                                        {KeyBranchClassWalled, "inventory.location_id to locations.branch_id"},
	"GET /api/v1/invoices":                                         {KeyBranchClassWalled, "invoices.branch_id"},
	"GET /api/v1/invoices/{id}":                                    {KeyBranchClassWalled, "invoices.branch_id"},
	"GET /api/v1/invoices/{id}/payments":                           {KeyBranchClassWalled, "payments.branch_id beside InvoiceVisible"},
	"GET /api/v1/links/customers/{id}":                             {KeyBranchClassWalled, "customer_branches (the link resolves through the walled customer Get)"},
	"GET /api/v1/links/drafts/orders/{id}":                         {KeyBranchClassWalled, "drafts.branch_id (the link resolves through the walled draft Get)"},
	"GET /api/v1/links/drafts/quotes/{id}":                         {KeyBranchClassWalled, "drafts.branch_id (the link resolves through the walled draft Get)"},
	"GET /api/v1/links/invoices/{id}":                              {KeyBranchClassWalled, "invoices.branch_id (the link resolves through the walled invoice Get)"},
	"GET /api/v1/links/orders/{id}":                                {KeyBranchClassWalled, "orders.branch_id (the link resolves through the walled order Get)"},
	"GET /api/v1/links/quotes/{id}":                                {KeyBranchClassWalled, "quotes.branch_id (the link resolves through the walled quote Get)"},
	"GET /api/v1/locations":                                        {KeyBranchClassWalled, "locations.branch_id (the branch middleware context)"},
	"GET /api/v1/locations/{id}":                                   {KeyBranchClassWalled, "locations.branch_id (the payload guard)"},
	"GET /api/v1/matching/exceptions":                              {KeyBranchClassWalled, "purchase_orders.branch_id (the exceptions join their order)"},
	"GET /api/v1/matching/results/{po_id}":                         {KeyBranchClassWalled, "purchase_orders.branch_id"},
	"GET /api/v1/orders":                                           {KeyBranchClassWalled, "orders.branch_id"},
	"GET /api/v1/orders/fulfillment-requests":                      {KeyBranchClassWalled, "orders.branch_id (the requests join their order)"},
	"GET /api/v1/orders/{id}":                                      {KeyBranchClassWalled, "orders.branch_id"},
	"GET /api/v1/orders/{id}/exposure-gate":                        {KeyBranchClassWalled, "orders.branch_id"},
	"GET /api/v1/payments":                                         {KeyBranchClassWalled, "payments.branch_id (the payment repository wall)"},
	"GET /api/v1/payments/{id}":                                    {KeyBranchClassWalled, "payments.branch_id (the payment repository wall)"},
	"GET /api/v1/pos/returns":                                      {KeyBranchClassWalled, "pos_returns.branch_id; TestKeyBranchPin_PosAndAccountRows"},
	"GET /api/v1/pos/returns/{id}":                                 {KeyBranchClassWalled, "pos_returns.branch_id; TestKeyBranchPin_PosAndAccountRows"},
	"GET /api/v1/pos/till/current":                                 {KeyBranchClassWalled, "till_sessions.branch_id (the register's open session); TestKeyBranchPin_PosAndAccountRows"},
	"GET /api/v1/pos/till/{id}/report":                             {KeyBranchClassWalled, "till_sessions.branch_id (a session the caller's branch context hides reads as missing, the 404 the Z report read answers, round 8); TestKeyBranchPin_PosAndAccountRows, TestKeyBranchPin_HiddenTillReportAnswers404"},
	"GET /api/v1/pos/till/{id}/zreport":                            {KeyBranchClassWalled, "till_z_reports.branch_id; TestKeyBranchPin_PosAndAccountRows"},
	"GET /api/v1/pos/transactions":                                 {KeyBranchClassWalled, "pos_transactions.branch_id"},
	"GET /api/v1/pos/transactions/{id}":                            {KeyBranchClassWalled, "pos_transactions.branch_id"},
	"GET /api/v1/pos/transactions/{id}/receipt":                    {KeyBranchClassWalled, "pos_transactions.branch_id (the sale is read as the sale read reads it, before the invoice is named); TestPosReceipt_CashierPrintsAndEmailsTheSalesInvoice"},
	"GET /api/v1/pos/zreports":                                     {KeyBranchClassWalled, "till_z_reports.branch_id; TestKeyBranchPin_PosAndAccountRows"},
	"GET /api/v1/products/reorder-alerts":                          {KeyBranchClassWalled, "inventory.location_id to locations.branch_id (stock narrows by branch)"},
	"GET /api/v1/purchase-orders":                                  {KeyBranchClassWalled, "purchase_orders.branch_id"},
	"GET /api/v1/purchase-orders/recommendations":                  {KeyBranchClassWalled, "inventory.location_id to locations.branch_id and products"},
	"GET /api/v1/purchase-orders/reorder-runs":                     {KeyBranchClassWalled, "purchase_orders.branch_id"},
	"GET /api/v1/purchase-orders/source-summary":                   {KeyBranchClassWalled, "purchase_orders.branch_id"},
	"GET /api/v1/purchase-orders/{id}":                             {KeyBranchClassWalled, "purchase_orders.branch_id"},
	"GET /api/v1/purchase-orders/{id}/freight":                     {KeyBranchClassWalled, "purchase_orders.branch_id"},
	"GET /api/v1/quotes":                                           {KeyBranchClassWalled, "quotes.branch_id"},
	"GET /api/v1/quotes/analytics":                                 {KeyBranchClassWalled, "quotes.branch_id"},
	"GET /api/v1/quotes/{id}":                                      {KeyBranchClassWalled, "quotes.branch_id"},
	"GET /api/v1/quotes/{id}/file":                                 {KeyBranchClassWalled, "quotes.branch_id"},
	"GET /api/v1/ship-tos/{id}":                                    {KeyBranchClassWalled, "customer_ship_tos.customer_id to customer_branches"},
	"PATCH /api/v1/customers/{id}/salesperson":                     {KeyBranchClassWalled, "customer_branches"},
	"POST /api/v1/ar/applications/{id}/reverse":                    {KeyBranchClassWalled, "ar_applications to invoices.branch_id (GetApplication wall)"},
	"POST /api/v1/ar/credit-holds":                                 {KeyBranchClassWalled, "credit_holds.customer_id to customer_branches (CustomerInWall)"},
	"POST /api/v1/ar/credit-holds/{id}/override":                   {KeyBranchClassWalled, "credit_holds.customer_id to customer_branches (CustomerInWall)"},
	"POST /api/v1/ar/credit-holds/{id}/release":                    {KeyBranchClassWalled, "credit_holds.customer_id to customer_branches (CustomerInWall)"},
	"POST /api/v1/ar/customers/{id}/credit-limit-changes":          {KeyBranchClassWalled, "customer_branches (CustomerInWall)"},
	"POST /api/v1/credit-memos":                                    {KeyBranchClassWalled, "credit_memos.branch_id (the create takes branch_id from the caller context)"},
	"POST /api/v1/credit-memos/{id}/applications":                  {KeyBranchClassWalled, "credit_memos.branch_id (MemoFactsFor and LockMemo wall)"},
	"POST /api/v1/credit-memos/{id}/refunds":                       {KeyBranchClassWalled, "credit_memos.branch_id (MemoFactsFor and LockMemo wall)"},
	"POST /api/v1/credit-memos/{id}/transitions":                   {KeyBranchClassWalled, "credit_memos.branch_id"},
	"POST /api/v1/customers":                                       {KeyBranchClassWalled, "customers.primary_branch_id (the branch middleware holds it to the pin)"},
	"POST /api/v1/customers/{customerId}/activities":               {KeyBranchClassWalled, "crm_activities.customer_id to customer_branches"},
	"POST /api/v1/customers/{customerId}/contacts":                 {KeyBranchClassWalled, "contacts.customer_id to customer_branches"},
	"POST /api/v1/customers/{id}/ship-tos":                         {KeyBranchClassWalled, "customer_ship_tos.customer_id to customer_branches"},
	"POST /api/v1/delivery/deliveries":                             {KeyBranchClassWalled, "deliveries.order_id to orders.branch_id"},
	"POST /api/v1/delivery/deliveries/{id}/adjust-qty":             {KeyBranchClassWalled, "deliveries.order_id to orders.branch_id (stopVisible)"},
	"POST /api/v1/delivery/deliveries/{id}/pod-photo":              {KeyBranchClassWalled, "deliveries.order_id to orders.branch_id (stopVisible)"},
	"POST /api/v1/delivery/deliveries/{id}/transitions":            {KeyBranchClassWalled, "deliveries.order_id to orders.branch_id (stopVisible)"},
	"POST /api/v1/delivery/routes":                                 {KeyBranchClassWalled, "the route create carries the caller context branch into its stops orders"},
	"POST /api/v1/delivery/routes/{id}/optimize":                   {KeyBranchClassWalled, "delivery_routes stops to their orders branch (routeVisible)"},
	"POST /api/v1/delivery/routes/{id}/reorder":                    {KeyBranchClassWalled, "delivery_routes stops to their orders branch (routeVisible)"},
	"POST /api/v1/delivery/routes/{id}/transitions":                {KeyBranchClassWalled, "delivery_routes stops to their orders branch (routeVisible)"},
	"POST /api/v1/drafts/orders":                                   {KeyBranchClassWalled, "drafts.branch_id (the draft carries the caller context branch)"},
	"POST /api/v1/drafts/orders/{id}/promote":                      {KeyBranchClassWalled, "drafts.branch_id"},
	"POST /api/v1/drafts/orders/{id}/transitions":                  {KeyBranchClassWalled, "drafts.branch_id"},
	"POST /api/v1/drafts/quotes":                                   {KeyBranchClassWalled, "drafts.branch_id (the draft carries the caller context branch)"},
	"POST /api/v1/drafts/quotes/{id}/promote":                      {KeyBranchClassWalled, "drafts.branch_id"},
	"POST /api/v1/drafts/quotes/{id}/transitions":                  {KeyBranchClassWalled, "drafts.branch_id"},
	"POST /api/v1/inventory/adjust":                                {KeyBranchClassWalled, "inventory.location_id to locations.branch_id (the guard checks both ends)"},
	"POST /api/v1/inventory/transfer":                              {KeyBranchClassWalled, "inventory.location_id to locations.branch_id (the guard checks both ends)"},
	"POST /api/v1/invoices/{id}/email":                             {KeyBranchClassWalled, "invoices.branch_id"},
	"POST /api/v1/quotes/{id}/email":                               {KeyBranchClassWalled, "quotes.branch_id"},
	"POST /api/v1/invoices/{id}/transitions":                       {KeyBranchClassWalled, "invoices.branch_id"},
	"POST /api/v1/invoices/{id}/write-offs":                        {KeyBranchClassWalled, "invoices.branch_id"},
	"POST /api/v1/locations":                                       {KeyBranchClassWalled, "locations.branch_id (the guard on the parent the row hangs under)"},
	"POST /api/v1/matching/run/{po_id}":                            {KeyBranchClassWalled, "purchase_orders.branch_id (the run reads the order under the wall)"},
	"POST /api/v1/orders":                                          {KeyBranchClassWalled, "orders.branch_id (the payload branch rule); the body's customer_id and job_id through the customer branch wall (round 9 P1-1, round 11 P1-1)"},
	"POST /api/v1/orders/fulfillment-requests/{delivery_id}/retry": {KeyBranchClassWalled, "deliveries.order_id to orders.branch_id"},
	"POST /api/v1/orders/{id}/allocate":                            {KeyBranchClassWalled, "orders.branch_id"},
	"POST /api/v1/orders/{id}/exposure-override":                   {KeyBranchClassWalled, "orders.branch_id"},
	"POST /api/v1/orders/{id}/fulfillments":                        {KeyBranchClassWalled, "orders.branch_id"},
	"POST /api/v1/orders/{id}/transitions":                         {KeyBranchClassWalled, "orders.branch_id"},
	"POST /api/v1/payments":                                        {KeyBranchClassWalled, "payments.branch_id (the create takes branch_id from the caller context)"},
	"POST /api/v1/payments/card":                                   {KeyBranchClassWalled, "payments.branch_id (the card charge writes its payment under the caller context)"},
	"POST /api/v1/payments/intent":                                 {KeyBranchClassWalled, "payments.branch_id (the intent writes under the caller context); the route takes no customer, so its own 400 answers a body naming one (round 11 P3-2)"},
	"POST /api/v1/payments/{id}/applications":                      {KeyBranchClassWalled, "payments.branch_id beside InvoiceVisible"},
	"POST /api/v1/payments/{id}/refunds":                           {KeyBranchClassWalled, "payments.branch_id (MemoFactsFor and the refund wall)"},
	"POST /api/v1/payments/{id}/transitions":                       {KeyBranchClassWalled, "payments.branch_id (the payment repository wall)"},
	"POST /api/v1/pos/refund-intents/{id}/payback":                 {KeyBranchClassRefused, "a held credit is paid back by a person through the counter terminal rail: finance and owner only, and the route itself refuses a machine key, bound or not, whatever scopes it holds (C2-5, FROM-PRINCIPAL 60b)"},
	"POST /api/v1/pos/refund-intents/{id}/resolve":                 {KeyBranchClassRefused, "a refund claim is resolved by a person: finance and owner only, and the route itself refuses a machine key, bound or not, whatever scopes it holds (C2-5, FROM-PRINCIPAL 41)"},
	"POST /api/v1/pos/returns":                                     {KeyBranchClassWalled, "the return is stamped with the register's branch (its location's branch, else pos_registers.branch_id) and the caller's branch context is held against it (403 on a mismatch, round 7); a customer the return names, or its original sale's (a return of a sale credits that sale's customer only, 400 otherwise), passes the counter's one customer rule (404 when hidden, nothing written, round 22); no machine key cashier"},
	"POST /api/v1/pos/sync":                                        {KeyBranchClassWalled, "the register's branch (its location's branch, else pos_registers.branch_id) on every replayed create and on the batch register itself (a batch of another branch's register is refused 403 before any item runs and before the pos_sync_log row is written, round 7, and a batch register that is empty or names no register is refused 400 the same way, rounds 8 and 9), and items[].customer_id through the customer branch wall (round 10 P1-1); TestKeyBranchPin_PosSyncCustomerWall, TestKeyBranchPin_SyncRefusesAnotherBranchsBatchRegister, TestKeyBranchPin_SyncRefusesAnUnknownBatchRegister"},
	"POST /api/v1/pos/till/open":                                   {KeyBranchClassWalled, "the till session is stamped with the register's branch (its location's branch, else pos_registers.branch_id) and the caller's branch context is held against it (403 on a mismatch, round 7); no machine key cashier"},
	"POST /api/v1/pos/till/{id}/close":                             {KeyBranchClassWalled, "till_sessions.branch_id (the close reads the session first); TestKeyBranchPin_PosAndAccountRows"},
	"POST /api/v1/pos/till/{id}/movements":                         {KeyBranchClassWalled, "till_sessions.branch_id (the session is held before the write, and a movement carries its register's branch); no machine key cashier (the handler refuses every key); TestTheMovementRoutesCarryTheBranchWall"},
	"GET /api/v1/pos/till/{id}/movements":                          {KeyBranchClassWalled, "till_movements.branch_id (a session the caller's branch context hides reads as missing); TestTheMovementRoutesCarryTheBranchWall"},
	"GET /api/v1/pos/till/signoffs":                                {KeyBranchClassRefused, "the sign off queue is the managers' (finance and owner): a person's screen, not a key's (CP-08)"},
	"GET /api/v1/pos/till/policy":                                  {KeyBranchClassRefused, "the variance threshold is a manager's setting (finance and owner): the read stays with the write (CP-08)"},
	"GET /api/v1/pos/end-of-day":                                   {KeyBranchClassRefused, "the branch end of day is the managers' (finance and owner): a person's screen, with the close and the deposit match it leads to (CP-09)"},
	"POST /api/v1/pos/till/{id}/signoff":                           {KeyBranchClassRefused, "a variance is signed off by a manager, a person: finance and owner only, and the route itself refuses a machine key, bound or not (CP-08)"},
	"PUT /api/v1/pos/till/policy":                                  {KeyBranchClassRefused, "the variance threshold is a manager's setting: finance and owner only, and the route itself refuses a machine key, bound or not (CP-08)"},
	"POST /api/v1/pos/end-of-day/{id}/reopen":                      {KeyBranchClassRefused, "a branch day is reopened by a manager, a person: finance and owner only, and the route itself refuses a machine key, bound or not (CP-09)"},
	"POST /api/v1/pos/end-of-day/close":                            {KeyBranchClassRefused, "a branch day is closed by a manager, a person: finance and owner only, and the route itself refuses a machine key, bound or not (CP-09)"},
	"GET /api/v1/pos/end-of-day/{id}/deposit-candidates":           {KeyBranchClassRefused, "the bank statement spans every branch (bank reconciliation is refused to a bound key); finance and owner only (CP-09)"},
	"POST /api/v1/pos/end-of-day/{id}/deposit-match":               {KeyBranchClassRefused, "the bank statement spans every branch (bank reconciliation is refused to a bound key); finance and owner only, and the route itself refuses a machine key (CP-09)"},
	"POST /api/v1/pos/transactions":                                {KeyBranchClassWalled, "the register's branch (its location's branch, else pos_registers.branch_id) on create, pos_transactions.branch_id after, and a register of another branch is refused 403 (round 7); the customer it names goes through the customer branch wall (a hidden customer reads as missing, nothing written); no machine key cashier"},
	"POST /api/v1/pos/transactions/{id}/complete":                  {KeyBranchClassWalled, "pos_transactions.branch_id (the transaction is read before any write)"},
	"POST /api/v1/pos/transactions/{id}/items":                     {KeyBranchClassWalled, "pos_transactions.branch_id (the transaction is read before the insert); TestKeyBranchPin_PosAndAccountRows"},
	"POST /api/v1/pos/transactions/{id}/receipt/email":             {KeyBranchClassWalled, "pos_transactions.branch_id (the sale is read as the sale read reads it, before the invoice is named); TestPosReceipt_CashierPrintsAndEmailsTheSalesInvoice"},
	"POST /api/v1/pos/transactions/{id}/void":                      {KeyBranchClassWalled, "pos_transactions.branch_id (the transaction is read before any write)"},
	"POST /api/v1/purchase-orders":                                 {KeyBranchClassWalled, "purchase_orders.branch_id (the create takes the caller context branch)"},
	"POST /api/v1/purchase-orders/reorder-check":                   {KeyBranchClassWalled, "purchase_orders.branch_id (the check reads stock and velocity under the caller context)"},
	"POST /api/v1/purchase-orders/{id}/freight":                    {KeyBranchClassWalled, "purchase_orders.branch_id"},
	"POST /api/v1/purchase-orders/{id}/freight/{freightId}/apply":  {KeyBranchClassWalled, "purchase_orders.branch_id"},
	"POST /api/v1/purchase-orders/{id}/receive":                    {KeyBranchClassWalled, "purchase_orders.branch_id"},
	"POST /api/v1/purchase-orders/{id}/submit":                     {KeyBranchClassWalled, "purchase_orders.branch_id"},
	"POST /api/v1/quotes":                                          {KeyBranchClassWalled, "quotes.branch_id (the payload branch rule); the body's customer_id and job_id through the customer branch wall (round 9 P1-1, round 11 P1-1)"},
	"POST /api/v1/quotes/{id}/convert":                             {KeyBranchClassWalled, "quotes.branch_id (the convert reads the quote under the wall); the quote's stored customer through the customer branch wall, rechecked where it becomes an order (round 11 P2-1); TestKeyBranchPin_QuoteConvertCustomerWall"},
	"POST /api/v1/quotes/{id}/transitions":                         {KeyBranchClassWalled, "quotes.branch_id"},
	"POST /api/v1/quotes/{id}/share-link":                          {KeyBranchClassRefused, "the link is a public credential to a quote, issued by a named desk user; a branch bound key is not a principal there"},
	"PUT /api/v1/activities/{id}":                                  {KeyBranchClassWalled, "crm_activities.customer_id to customer_branches; on a shared customer a bound key never changes one (no branch tag; TestKeyBranchPin_ActivityFollowsTheContactRule)"},
	"PUT /api/v1/contacts/{id}":                                    {KeyBranchClassWalled, "contacts.customer_id to customer_branches"},
	"PUT /api/v1/credit-memos/{id}":                                {KeyBranchClassWalled, "credit_memos.branch_id"},
	"PUT /api/v1/customers/{id}":                                   {KeyBranchClassWalled, "customer_branches (a customer of another branch reads as missing)"},
	"PUT /api/v1/customers/{id}/escalation-policy":                 {KeyBranchClassWalled, "customer_branches"},
	"PUT /api/v1/drafts/orders/{id}":                               {KeyBranchClassWalled, "drafts.branch_id"},
	"PUT /api/v1/drafts/quotes/{id}":                               {KeyBranchClassWalled, "drafts.branch_id"},
	"PUT /api/v1/orders/{id}":                                      {KeyBranchClassWalled, "orders.branch_id"},
	"PUT /api/v1/quotes/{id}":                                      {KeyBranchClassWalled, "quotes.branch_id"},
	"PUT /api/v1/quotes/{id}/file":                                 {KeyBranchClassWalled, "quotes.branch_id"},
	"PUT /api/v1/ship-tos/{id}":                                    {KeyBranchClassWalled, "customer_ship_tos.customer_id to customer_branches"},
	"PUT /api/v1/pos/transactions/{id}/customer":                   {KeyBranchClassWalled, "pos_transactions.branch_id (the sale is held to the caller's branch before any write: 404 for a hidden sale, 403 by name with no branch in context and no grant) and the named customer through ruling A (a user names any customer; a bound key's pin hides the customers of other yards, read as missing); no machine key cashier"},
}

// KeyBranchRouteClassFor returns the class the table gives a method and
// registered pattern. ok is false for a route the table does not carry: deny
// by default, the caller refuses.
func KeyBranchRouteClassFor(method, pattern string) (KeyBranchRouteClass, bool) {
	note, ok := keyBranchRoutes[method+" "+pattern]
	return note.Class, ok
}

// KeyBranchRouteAllowed reports whether a branch bound machine key may pass
// to the route the method and registered pattern name: true only for a
// table entry of class WALLED, CONFINED or STATED LIMIT. A REFUSED entry, a
// route the table does not carry, and an empty pattern (the router matched
// nothing) are all refused.
func KeyBranchRouteAllowed(method, pattern string) bool {
	class, ok := KeyBranchRouteClassFor(method, pattern)
	return ok && class != KeyBranchClassRefused
}

// KeyBranchAllowlistRoutes returns every table entry, sorted by route, for
// the census test and tooling.
func KeyBranchAllowlistRoutes() []string {
	out := make([]string, 0, len(keyBranchRoutes))
	for route := range keyBranchRoutes {
		out = append(out, route)
	}
	sort.Strings(out)
	return out
}

// KeyBranchAllowlistNoteFor returns an entry's one line justification. It is
// empty for a route the table does not carry; the class accessor reports
// that case.
func KeyBranchAllowlistNoteFor(method, pattern string) string {
	return keyBranchRoutes[method+" "+pattern].Note
}

// KeyBranchRouteReachable reports whether a machine key can reach a census
// route at all: under /api/v1, on a module the scope vocabulary allows, and
// not under a user only prefix (key management, the me routes). It is the
// census test's rule in one place, so the middleware table and the test can
// never disagree about which routes need a class.
func KeyBranchRouteReachable(method, pattern string) bool {
	if !strings.HasPrefix(pattern, "/api/v1/") {
		return false
	}
	module, ok := PolicyModuleForPath(method, pattern)
	if !ok || ModuleScopePolicyFor(module) != ModuleScopeAllowed {
		return false
	}
	path := pattern
	if i := strings.IndexByte(path, ' '); i >= 0 {
		path = path[i+1:]
	}
	for _, prefix := range MachineKeyUserOnlyPrefixes() {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return false
		}
	}
	return true
}

// patternResolver is the surface of *http.ServeMux the gate resolves a
// request's registered pattern through. Serving still goes through the mux
// itself (only the mux sets r.Pattern and the path values on the request it
// serves), so the gate resolves once for its decision and never serves the
// pre matched handler.
type patternResolver interface {
	Handler(r *http.Request) (h http.Handler, pattern string)
}

// KeyBranchRouteGate is the one runtime check: a middleware that refuses a
// branch bound machine key every route the allowlist does not admit, before
// the request reaches the confirm gate, the idempotency layer or any handler.
// A request with no pin (a user, an unbound key, another seam) passes
// untouched, so only a bound key ever pays the routing lookup. mux is the
// serve mux the patterns resolve against; auditor writes the
// key.branch_refused row and may be nil (the refusal is still served).
func KeyBranchRouteGate(mux http.Handler, auditor BranchRefusalAuditor) func(http.Handler) http.Handler {
	g := &keyBranchRouteGate{mux: mux, auditor: auditor}
	if resolver, ok := mux.(patternResolver); ok {
		g.resolve = resolver
	}
	return g.wrap
}

type keyBranchRouteGate struct {
	mux     http.Handler
	resolve patternResolver
	auditor BranchRefusalAuditor
}

func (g *keyBranchRouteGate) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pin, ok := branchctx.KeyBranchFromContext(r.Context())
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		// The resolver answers the registered pattern with its method prefix
		// ("GET /api/v1/quotes/{id}"), and the method that matched, which a
		// HEAD request reads from its GET pattern. An empty answer (no route,
		// or only another method on the path) fails the cut and is refused.
		method, pattern := "", ""
		if g.resolve != nil {
			_, resolved := g.resolve.Handler(r)
			if m, rest, cut := strings.Cut(resolved, " "); cut {
				method, pattern = m, rest
			}
		}
		if KeyBranchRouteAllowed(method, pattern) {
			next.ServeHTTP(w, r)
			return
		}
		if keyID, ok := KeyIDFromContext(r.Context()); ok && g.auditor != nil {
			g.auditor.AuditKeyBranchRefusal(r.Context(), keyID, pin, r.Method, r.URL.Path)
		}
		respondAuthError(w, r, http.StatusForbidden, "forbidden",
			"a branch bound key may act only on its own branch: the route is not admitted to it")
	})
}
