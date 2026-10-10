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
	"GET /api/v1/branches/{id}/users":                              {KeyBranchClassConfined, "NamedBranch(id); TestKeyBranchPin_LocationReads"},
	"GET /api/v1/pricing/calculate":                                {KeyBranchClassConfined, "CustomerBranch(query customer_id); TestKeyBranchPin_CustomerConfinedReads"},
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
	"DELETE /api/v1/admin/staff/{id}/modules/{module_id}":          {KeyBranchClassRefused, "the staff roster and its module grants span every branch"},
	"DELETE /api/v1/reporting/saved/{id}":                          {KeyBranchClassRefused, "reports span every branch"},
	"DELETE /api/v1/reporting/schedules/{id}":                      {KeyBranchClassRefused, "reports span every branch"},
	"GET /api/v1/admin/staff":                                      {KeyBranchClassRefused, "the staff roster and its module grants span every branch"},
	"GET /api/v1/admin/staff/{id}":                                 {KeyBranchClassRefused, "the staff roster and its module grants span every branch"},
	"GET /api/v1/ap/aging":                                         {KeyBranchClassRefused, "payables span every branch"},
	"GET /api/v1/ap/invoices":                                      {KeyBranchClassRefused, "payables span every branch"},
	"GET /api/v1/ap/invoices/{id}":                                 {KeyBranchClassRefused, "payables span every branch"},
	"GET /api/v1/ap/payments":                                      {KeyBranchClassRefused, "payables span every branch"},
	"GET /api/v1/bankrecon/accounts":                               {KeyBranchClassRefused, "bank reconciliation spans every branch"},
	"GET /api/v1/bankrecon/sessions":                               {KeyBranchClassRefused, "bank reconciliation spans every branch"},
	"GET /api/v1/bankrecon/sessions/{id}":                          {KeyBranchClassRefused, "bank reconciliation spans every branch"},
	"GET /api/v1/events":                                           {KeyBranchClassRefused, "the feed carries every branch events"},
	"GET /api/v1/gl/accounts":                                      {KeyBranchClassRefused, "the books span every branch"},
	"GET /api/v1/gl/balance-sheet":                                 {KeyBranchClassRefused, "the books span every branch"},
	"GET /api/v1/gl/fiscal-periods":                                {KeyBranchClassRefused, "the books span every branch"},
	"GET /api/v1/gl/journal-entries":                               {KeyBranchClassRefused, "the books span every branch"},
	"GET /api/v1/gl/journal-entries/{id}":                          {KeyBranchClassRefused, "the books span every branch"},
	"GET /api/v1/gl/profit-and-loss":                               {KeyBranchClassRefused, "the books span every branch"},
	"GET /api/v1/gl/trial-balance":                                 {KeyBranchClassRefused, "the books span every branch"},
	"GET /api/v1/pricing/matrix":                                   {KeyBranchClassRefused, "the unfiltered matrix cannot be confined to a branch"},
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
	"POST /api/v1/admin/exposure-scan":                             {KeyBranchClassRefused, "the scan re-checks every branch quotes"},
	"POST /api/v1/admin/staff":                                     {KeyBranchClassRefused, "the staff roster and its module grants span every branch"},
	"POST /api/v1/admin/staff/{id}/modules":                        {KeyBranchClassRefused, "the staff roster and its module grants span every branch"},
	"POST /api/v1/ap/invoices":                                     {KeyBranchClassRefused, "payables span every branch"},
	"POST /api/v1/ap/invoices/{id}/approve":                        {KeyBranchClassRefused, "payables span every branch"},
	"POST /api/v1/ap/payments":                                     {KeyBranchClassRefused, "payables span every branch"},
	"POST /api/v1/bankrecon/accounts":                              {KeyBranchClassRefused, "bank reconciliation spans every branch"},
	"POST /api/v1/bankrecon/import":                                {KeyBranchClassRefused, "bank reconciliation spans every branch"},
	"POST /api/v1/bankrecon/match":                                 {KeyBranchClassRefused, "bank reconciliation spans every branch"},
	"POST /api/v1/bankrecon/sessions":                              {KeyBranchClassRefused, "bank reconciliation spans every branch"},
	"POST /api/v1/bankrecon/sessions/{id}/complete":                {KeyBranchClassRefused, "bank reconciliation spans every branch"},
	"POST /api/v1/bankrecon/unmatch":                               {KeyBranchClassRefused, "bank reconciliation spans every branch"},
	"POST /api/v1/branches":                                        {KeyBranchClassRefused, "the branch directory create is outside every pin"},
	"POST /api/v1/gl/accounts":                                     {KeyBranchClassRefused, "the books span every branch"},
	"POST /api/v1/gl/fiscal-periods/{id}/close":                    {KeyBranchClassRefused, "the books span every branch"},
	"POST /api/v1/gl/fiscal-periods/{id}/reopen":                   {KeyBranchClassRefused, "the books span every branch"},
	"POST /api/v1/gl/journal-entries":                              {KeyBranchClassRefused, "the books span every branch"},
	"POST /api/v1/gl/journal-entries/{id}/post":                    {KeyBranchClassRefused, "the books span every branch"},
	"POST /api/v1/gl/journal-entries/{id}/reverse":                 {KeyBranchClassRefused, "the books span every branch"},
	"POST /api/v1/gl/journal-entries/{id}/void":                    {KeyBranchClassRefused, "the books span every branch"},
	"POST /api/v1/market-indices/{id}/refresh":                     {KeyBranchClassRefused, "the index refresh re-checks every branch quotes"},
	"POST /api/v1/market-indices/{id}/refresh/preview":             {KeyBranchClassRefused, "the index refresh re-checks every branch quotes"},
	"POST /api/v1/reporting/builder/export":                        {KeyBranchClassRefused, "reports span every branch"},
	"POST /api/v1/reporting/builder/preview":                       {KeyBranchClassRefused, "reports span every branch"},
	"POST /api/v1/reporting/save":                                  {KeyBranchClassRefused, "reports span every branch"},
	"POST /api/v1/reporting/saved/{id}/run":                        {KeyBranchClassRefused, "reports span every branch"},
	"POST /api/v1/reporting/schedules":                             {KeyBranchClassRefused, "reports span every branch"},
	"PUT /api/v1/admin/staff/{id}":                                 {KeyBranchClassRefused, "the staff roster and its module grants span every branch"},
	"PUT /api/v1/gl/accounts/{id}":                                 {KeyBranchClassRefused, "the books span every branch"},
	"PUT /api/v1/reporting/saved/{id}":                             {KeyBranchClassRefused, "reports span every branch"},
	"DELETE /api/v1/admin/settings/ai":                             {KeyBranchClassStatedLimit, "dealer wide configuration"},
	"DELETE /api/v1/admin/settings/routing":                        {KeyBranchClassStatedLimit, "dealer wide configuration"},
	"DELETE /api/v1/delivery/drivers/{id}":                         {KeyBranchClassStatedLimit, "the fleet is dealer wide, no branch column"},
	"DELETE /api/v1/delivery/vehicles/{id}":                        {KeyBranchClassStatedLimit, "the fleet is dealer wide, no branch column"},
	"DELETE /api/v1/edi/partners/{id}":                             {KeyBranchClassStatedLimit, "trading partners and catalogs, no branch fact"},
	"GET /api/v1/admin/modules":                                    {KeyBranchClassStatedLimit, "module kill switches change dealer wide behaviour"},
	"GET /api/v1/admin/settings/ai":                                {KeyBranchClassStatedLimit, "dealer wide configuration"},
	"GET /api/v1/admin/settings/routing":                           {KeyBranchClassStatedLimit, "dealer wide configuration"},
	"GET /api/v1/apps":                                             {KeyBranchClassStatedLimit, "app enablement switches change dealer wide behaviour"},
	"GET /api/v1/branches":                                         {KeyBranchClassStatedLimit, "branch directory reference data (the PR 39 decision)"},
	"GET /api/v1/branches/{id}":                                    {KeyBranchClassStatedLimit, "branch directory reference data (the PR 39 decision)"},
	"GET /api/v1/configurator/options":                             {KeyBranchClassStatedLimit, "option catalog configuration, no branch fact"},
	"GET /api/v1/configurator/presets":                             {KeyBranchClassStatedLimit, "option catalog configuration, no branch fact"},
	"GET /api/v1/configurator/rules":                               {KeyBranchClassStatedLimit, "option catalog configuration, no branch fact"},
	"GET /api/v1/delivery/drivers":                                 {KeyBranchClassStatedLimit, "the fleet is dealer wide, no branch column"},
	"GET /api/v1/delivery/drivers/{id}":                            {KeyBranchClassStatedLimit, "the fleet is dealer wide, no branch column"},
	"GET /api/v1/delivery/vehicles":                                {KeyBranchClassStatedLimit, "the fleet is dealer wide, no branch column"},
	"GET /api/v1/delivery/vehicles/{id}":                           {KeyBranchClassStatedLimit, "the fleet is dealer wide, no branch column"},
	"GET /api/v1/edi/partners":                                     {KeyBranchClassStatedLimit, "trading partners and catalogs, no branch fact"},
	"GET /api/v1/edi/partners/{id}":                                {KeyBranchClassStatedLimit, "trading partners and catalogs, no branch fact"},
	"GET /api/v1/edi/partners/{id}/catalog":                        {KeyBranchClassStatedLimit, "trading partners and catalogs, no branch fact"},
	"GET /api/v1/governance/rfcs":                                  {KeyBranchClassStatedLimit, "the rfc workflow carries no branch fact"},
	"GET /api/v1/governance/rfcs/{id}":                             {KeyBranchClassStatedLimit, "the rfc workflow carries no branch fact"},
	"GET /api/v1/market-indices":                                   {KeyBranchClassStatedLimit, "dealer wide index reference data"},
	"GET /api/v1/market-indices/{id}/history":                      {KeyBranchClassStatedLimit, "dealer wide index reference data"},
	"GET /api/v1/matching/config":                                  {KeyBranchClassStatedLimit, "match tolerances are dealer wide configuration"},
	"GET /api/v1/millwork/options":                                 {KeyBranchClassStatedLimit, "option catalog data, no branch fact"},
	"GET /api/v1/millwork/options/{id}":                            {KeyBranchClassStatedLimit, "option catalog data, no branch fact"},
	"GET /api/v1/price_levels":                                     {KeyBranchClassStatedLimit, "the dealer wide price level vocabulary"},
	"GET /api/v1/pricing/categories":                               {KeyBranchClassStatedLimit, "product category reference data, no branch fact"},
	"GET /api/v1/pricing/rebates/programs":                         {KeyBranchClassStatedLimit, "programs are dealer wide and a claim uses supplied volumes"},
	"GET /api/v1/pricing/rebates/programs/{id}":                    {KeyBranchClassStatedLimit, "programs are dealer wide and a claim uses supplied volumes"},
	"GET /api/v1/pricing/rebates/programs/{id}/claims":             {KeyBranchClassStatedLimit, "programs are dealer wide and a claim uses supplied volumes"},
	"GET /api/v1/units":                                            {KeyBranchClassStatedLimit, "the dealer wide unit catalogue"},
	"GET /api/v1/units/{code}":                                     {KeyBranchClassStatedLimit, "the dealer wide unit catalogue"},
	"GET /api/v1/vendors":                                          {KeyBranchClassStatedLimit, "the dealer wide vendor master"},
	"GET /api/v1/vendors/{id}":                                     {KeyBranchClassStatedLimit, "the dealer wide vendor master"},
	"POST /api/v1/apps/{key}/disable":                              {KeyBranchClassStatedLimit, "app enablement switches change dealer wide behaviour"},
	"POST /api/v1/apps/{key}/enable":                               {KeyBranchClassStatedLimit, "app enablement switches change dealer wide behaviour"},
	"POST /api/v1/configurator/build-sku":                          {KeyBranchClassStatedLimit, "option catalog configuration, no branch fact"},
	"POST /api/v1/configurator/validate":                           {KeyBranchClassStatedLimit, "option catalog configuration, no branch fact"},
	"POST /api/v1/delivery/drivers":                                {KeyBranchClassStatedLimit, "the fleet is dealer wide, no branch column"},
	"POST /api/v1/delivery/drivers/{id}/photo":                     {KeyBranchClassStatedLimit, "the fleet is dealer wide, no branch column"},
	"POST /api/v1/delivery/vehicles":                               {KeyBranchClassStatedLimit, "the fleet is dealer wide, no branch column"},
	"POST /api/v1/delivery/vehicles/{id}/photo":                    {KeyBranchClassStatedLimit, "the fleet is dealer wide, no branch column"},
	"POST /api/v1/edi/partners":                                    {KeyBranchClassStatedLimit, "trading partners and catalogs, no branch fact"},
	"POST /api/v1/edi/partners/{id}/import-catalog":                {KeyBranchClassStatedLimit, "trading partners and catalogs, no branch fact"},
	"POST /api/v1/governance/rfcs":                                 {KeyBranchClassStatedLimit, "the rfc workflow carries no branch fact"},
	"POST /api/v1/governance/rfcs/{id}/transitions":                {KeyBranchClassStatedLimit, "the rfc workflow carries no branch fact"},
	"POST /api/v1/millwork/options":                                {KeyBranchClassStatedLimit, "option catalog data, no branch fact"},
	"POST /api/v1/parsing/upload":                                  {KeyBranchClassStatedLimit, "the upload parses dealer wide product data"},
	"POST /api/v1/pricing/calculate-escalation":                    {KeyBranchClassStatedLimit, "a calculator over supplied inputs, no branch fact"},
	"POST /api/v1/pricing/categories":                              {KeyBranchClassStatedLimit, "product category reference data, no branch fact"},
	"POST /api/v1/pricing/rebates/programs":                        {KeyBranchClassStatedLimit, "programs are dealer wide and a claim uses supplied volumes"},
	"POST /api/v1/pricing/rebates/programs/{id}/claims/calculate":  {KeyBranchClassStatedLimit, "programs are dealer wide and a claim uses supplied volumes"},
	"POST /api/v1/units":                                           {KeyBranchClassStatedLimit, "the dealer wide unit catalogue"},
	"POST /api/v1/vendors":                                         {KeyBranchClassStatedLimit, "the dealer wide vendor master"},
	"POST /api/v1/vision/scan":                                     {KeyBranchClassStatedLimit, "the blueprint scan prototype carries no branch fact"},
	"PUT /api/v1/admin/modules/{id}":                               {KeyBranchClassStatedLimit, "module kill switches change dealer wide behaviour"},
	"PUT /api/v1/admin/settings/ai":                                {KeyBranchClassStatedLimit, "dealer wide configuration"},
	"PUT /api/v1/admin/settings/routing":                           {KeyBranchClassStatedLimit, "dealer wide configuration"},
	"PUT /api/v1/delivery/drivers/{id}":                            {KeyBranchClassStatedLimit, "the fleet is dealer wide, no branch column"},
	"PUT /api/v1/delivery/vehicles/{id}":                           {KeyBranchClassStatedLimit, "the fleet is dealer wide, no branch column"},
	"PUT /api/v1/edi/partners/{id}":                                {KeyBranchClassStatedLimit, "trading partners and catalogs, no branch fact"},
	"PUT /api/v1/governance/rfcs/{id}":                             {KeyBranchClassStatedLimit, "the rfc workflow carries no branch fact"},
	"PUT /api/v1/market-indices/{id}":                              {KeyBranchClassStatedLimit, "dealer wide index reference data"},
	"PUT /api/v1/matching/config":                                  {KeyBranchClassStatedLimit, "match tolerances are dealer wide configuration"},
	"PUT /api/v1/pricing/categories/{id}":                          {KeyBranchClassStatedLimit, "product category reference data, no branch fact"},
	"PUT /api/v1/units/{code}":                                     {KeyBranchClassStatedLimit, "the dealer wide unit catalogue"},
	"DELETE /api/v1/activities/{id}":                               {KeyBranchClassWalled, "the branch middleware"},
	"DELETE /api/v1/contacts/{id}":                                 {KeyBranchClassWalled, "the branch middleware"},
	"DELETE /api/v1/pos/transactions/{id}/items/{itemId}":          {KeyBranchClassWalled, "the branch middleware"},
	"DELETE /api/v1/products/{id}/pim/collateral/{collateralId}":   {KeyBranchClassWalled, "the branch middleware"},
	"DELETE /api/v1/products/{id}/pim/media/{mediaId}":             {KeyBranchClassWalled, "the branch middleware"},
	"GET /api/v1/accounts/{id}":                                    {KeyBranchClassWalled, "the branch middleware"},
	"GET /api/v1/accounts/{id}/transactions":                       {KeyBranchClassWalled, "the branch middleware"},
	"GET /api/v1/activities/{id}":                                  {KeyBranchClassWalled, "the branch middleware"},
	"GET /api/v1/ar/aging":                                         {KeyBranchClassWalled, "the branch middleware"},
	"GET /api/v1/ar/aging/summary":                                 {KeyBranchClassWalled, "the branch middleware"},
	"GET /api/v1/ar/customers/{id}/statement":                      {KeyBranchClassWalled, "the branch middleware"},
	"GET /api/v1/ar/reconciliation":                                {KeyBranchClassWalled, "the branch middleware"},
	"GET /api/v1/branches/{id}/tree":                               {KeyBranchClassWalled, "the branch middleware"},
	"GET /api/v1/charge-codes":                                     {KeyBranchClassWalled, "the branch middleware"},
	"GET /api/v1/charge-codes/{id}":                                {KeyBranchClassWalled, "the branch middleware"},
	"GET /api/v1/contacts/{id}":                                    {KeyBranchClassWalled, "the branch middleware"},
	"GET /api/v1/credit-memos":                                     {KeyBranchClassWalled, "the branch middleware"},
	"GET /api/v1/credit-memos/{id}":                                {KeyBranchClassWalled, "the branch middleware"},
	"GET /api/v1/customers":                                        {KeyBranchClassWalled, "the branch middleware"},
	"GET /api/v1/customers/{customerId}/activities":                {KeyBranchClassWalled, "the branch middleware"},
	"GET /api/v1/customers/{customerId}/contacts":                  {KeyBranchClassWalled, "the branch middleware"},
	"GET /api/v1/customers/{id}":                                   {KeyBranchClassWalled, "the branch middleware"},
	"GET /api/v1/customers/{id}/escalation-policy":                 {KeyBranchClassWalled, "the branch middleware"},
	"GET /api/v1/customers/{id}/ship-tos":                          {KeyBranchClassWalled, "the branch middleware"},
	"GET /api/v1/dashboard/inventory-alerts":                       {KeyBranchClassWalled, "the branch middleware"},
	"GET /api/v1/dashboard/order-activity":                         {KeyBranchClassWalled, "the branch middleware"},
	"GET /api/v1/dashboard/revenue-trend":                          {KeyBranchClassWalled, "the branch middleware"},
	"GET /api/v1/dashboard/summary":                                {KeyBranchClassWalled, "the branch middleware"},
	"GET /api/v1/dashboard/top-customers":                          {KeyBranchClassWalled, "the branch middleware"},
	"GET /api/v1/delivery/deliveries/{id}":                         {KeyBranchClassWalled, "the branch middleware"},
	"GET /api/v1/delivery/deliveries/{id}/pod-photos":              {KeyBranchClassWalled, "the branch middleware"},
	"GET /api/v1/delivery/routes":                                  {KeyBranchClassWalled, "the branch middleware"},
	"GET /api/v1/delivery/routes/{id}":                             {KeyBranchClassWalled, "the branch middleware"},
	"GET /api/v1/delivery/routes/{id}/deliveries":                  {KeyBranchClassWalled, "the branch middleware"},
	"GET /api/v1/documents/print/invoice/{id}":                     {KeyBranchClassWalled, "the branch middleware"},
	"GET /api/v1/documents/print/pickticket/{id}":                  {KeyBranchClassWalled, "the branch middleware"},
	"GET /api/v1/drafts/orders":                                    {KeyBranchClassWalled, "the branch middleware"},
	"GET /api/v1/drafts/orders/feed":                               {KeyBranchClassWalled, "the branch middleware"},
	"GET /api/v1/drafts/orders/{id}":                               {KeyBranchClassWalled, "the branch middleware"},
	"GET /api/v1/drafts/quotes":                                    {KeyBranchClassWalled, "the branch middleware"},
	"GET /api/v1/drafts/quotes/feed":                               {KeyBranchClassWalled, "the branch middleware"},
	"GET /api/v1/drafts/quotes/{id}":                               {KeyBranchClassWalled, "the branch middleware"},
	"GET /api/v1/inventory":                                        {KeyBranchClassWalled, "the branch middleware"},
	"GET /api/v1/invoices":                                         {KeyBranchClassWalled, "the branch middleware"},
	"GET /api/v1/invoices/{id}":                                    {KeyBranchClassWalled, "the branch middleware"},
	"GET /api/v1/invoices/{id}/payments":                           {KeyBranchClassWalled, "the branch middleware"},
	"GET /api/v1/links/customers/{id}":                             {KeyBranchClassWalled, "the branch middleware"},
	"GET /api/v1/links/drafts/orders/{id}":                         {KeyBranchClassWalled, "the branch middleware"},
	"GET /api/v1/links/drafts/quotes/{id}":                         {KeyBranchClassWalled, "the branch middleware"},
	"GET /api/v1/links/invoices/{id}":                              {KeyBranchClassWalled, "the branch middleware"},
	"GET /api/v1/links/orders/{id}":                                {KeyBranchClassWalled, "the branch middleware"},
	"GET /api/v1/links/products/{id}":                              {KeyBranchClassWalled, "the branch middleware"},
	"GET /api/v1/links/quotes/{id}":                                {KeyBranchClassWalled, "the branch middleware"},
	"GET /api/v1/locations":                                        {KeyBranchClassWalled, "the branch middleware"},
	"GET /api/v1/locations/{id}":                                   {KeyBranchClassWalled, "the branch middleware"},
	"GET /api/v1/matching/exceptions":                              {KeyBranchClassWalled, "the branch middleware"},
	"GET /api/v1/matching/results/{po_id}":                         {KeyBranchClassWalled, "the branch middleware"},
	"GET /api/v1/orders":                                           {KeyBranchClassWalled, "the branch middleware"},
	"GET /api/v1/orders/fulfillment-requests":                      {KeyBranchClassWalled, "the branch middleware"},
	"GET /api/v1/orders/{id}":                                      {KeyBranchClassWalled, "the branch middleware"},
	"GET /api/v1/orders/{id}/exposure-gate":                        {KeyBranchClassWalled, "the branch middleware"},
	"GET /api/v1/payments":                                         {KeyBranchClassWalled, "the branch middleware"},
	"GET /api/v1/payments/{id}":                                    {KeyBranchClassWalled, "the branch middleware"},
	"GET /api/v1/payment-terms":                                    {KeyBranchClassWalled, "the branch middleware"},
	"GET /api/v1/payment-terms/{id}":                               {KeyBranchClassWalled, "the branch middleware"},
	"GET /api/v1/pos/catalog":                                      {KeyBranchClassWalled, "the branch middleware"},
	"GET /api/v1/pos/products/search":                              {KeyBranchClassWalled, "the branch middleware"},
	"GET /api/v1/pos/returns":                                      {KeyBranchClassWalled, "the branch middleware"},
	"GET /api/v1/pos/returns/{id}":                                 {KeyBranchClassWalled, "the branch middleware"},
	"GET /api/v1/pos/till/current":                                 {KeyBranchClassWalled, "the branch middleware"},
	"GET /api/v1/pos/till/{id}/report":                             {KeyBranchClassWalled, "the branch middleware"},
	"GET /api/v1/pos/till/{id}/zreport":                            {KeyBranchClassWalled, "the branch middleware"},
	"GET /api/v1/pos/transactions":                                 {KeyBranchClassWalled, "the branch middleware"},
	"GET /api/v1/pos/transactions/{id}":                            {KeyBranchClassWalled, "the branch middleware"},
	"GET /api/v1/pos/zreports":                                     {KeyBranchClassWalled, "the branch middleware"},
	"GET /api/v1/products":                                         {KeyBranchClassWalled, "the branch middleware"},
	"GET /api/v1/products/{id}":                                    {KeyBranchClassWalled, "the branch middleware"},
	"GET /api/v1/products/{id}/detail":                             {KeyBranchClassWalled, "the branch middleware"},
	"GET /api/v1/products/{id}/kit-components":                     {KeyBranchClassWalled, "the branch middleware"},
	"GET /api/v1/products/{id}/pim/collateral":                     {KeyBranchClassWalled, "the branch middleware"},
	"GET /api/v1/products/{id}/pim/content":                        {KeyBranchClassWalled, "the branch middleware"},
	"GET /api/v1/products/{id}/pim/media":                          {KeyBranchClassWalled, "the branch middleware"},
	"GET /api/v1/products/{id}/units":                              {KeyBranchClassWalled, "the branch middleware"},
	"GET /api/v1/products/reorder-alerts":                          {KeyBranchClassWalled, "the branch middleware"},
	"GET /api/v1/purchase-orders":                                  {KeyBranchClassWalled, "the branch middleware"},
	"GET /api/v1/purchase-orders/{id}":                             {KeyBranchClassWalled, "the branch middleware"},
	"GET /api/v1/purchase-orders/{id}/freight":                     {KeyBranchClassWalled, "the branch middleware"},
	"GET /api/v1/purchase-orders/recommendations":                  {KeyBranchClassWalled, "the branch middleware"},
	"GET /api/v1/purchase-orders/reorder-runs":                     {KeyBranchClassWalled, "the branch middleware"},
	"GET /api/v1/purchase-orders/source-summary":                   {KeyBranchClassWalled, "the branch middleware"},
	"GET /api/v1/quotes":                                           {KeyBranchClassWalled, "the branch middleware"},
	"GET /api/v1/quotes/analytics":                                 {KeyBranchClassWalled, "the branch middleware"},
	"GET /api/v1/quotes/{id}":                                      {KeyBranchClassWalled, "the branch middleware"},
	"GET /api/v1/quotes/{id}/file":                                 {KeyBranchClassWalled, "the branch middleware"},
	"GET /api/v1/ship-tos/{id}":                                    {KeyBranchClassWalled, "the branch middleware"},
	"PATCH /api/v1/customers/{id}/salesperson":                     {KeyBranchClassWalled, "the branch middleware"},
	"PATCH /api/v1/products/{id}/dimensions":                       {KeyBranchClassWalled, "the branch middleware"},
	"PATCH /api/v1/products/{id}/lead-time":                        {KeyBranchClassWalled, "the branch middleware"},
	"PATCH /api/v1/products/{id}/margins":                          {KeyBranchClassWalled, "the branch middleware"},
	"PATCH /api/v1/products/{id}/pim/media/{mediaId}/primary":      {KeyBranchClassWalled, "the branch middleware"},
	"POST /api/v1/ar/applications/{id}/reverse":                    {KeyBranchClassWalled, "the branch middleware"},
	"POST /api/v1/charge-codes":                                    {KeyBranchClassWalled, "the branch middleware"},
	"POST /api/v1/credit-memos":                                    {KeyBranchClassWalled, "the branch middleware"},
	"POST /api/v1/credit-memos/{id}/applications":                  {KeyBranchClassWalled, "the branch middleware"},
	"POST /api/v1/credit-memos/{id}/refunds":                       {KeyBranchClassWalled, "the branch middleware"},
	"POST /api/v1/credit-memos/{id}/transitions":                   {KeyBranchClassWalled, "the branch middleware"},
	"POST /api/v1/customers":                                       {KeyBranchClassWalled, "the branch middleware"},
	"POST /api/v1/customers/{customerId}/activities":               {KeyBranchClassWalled, "the branch middleware"},
	"POST /api/v1/customers/{customerId}/contacts":                 {KeyBranchClassWalled, "the branch middleware"},
	"POST /api/v1/customers/{id}/ship-tos":                         {KeyBranchClassWalled, "the branch middleware"},
	"POST /api/v1/delivery/deliveries":                             {KeyBranchClassWalled, "the branch middleware"},
	"POST /api/v1/delivery/deliveries/{id}/adjust-qty":             {KeyBranchClassWalled, "the branch middleware"},
	"POST /api/v1/delivery/deliveries/{id}/pod-photo":              {KeyBranchClassWalled, "the branch middleware"},
	"POST /api/v1/delivery/deliveries/{id}/transitions":            {KeyBranchClassWalled, "the branch middleware"},
	"POST /api/v1/delivery/routes":                                 {KeyBranchClassWalled, "the branch middleware"},
	"POST /api/v1/delivery/routes/{id}/optimize":                   {KeyBranchClassWalled, "the branch middleware"},
	"POST /api/v1/delivery/routes/{id}/reorder":                    {KeyBranchClassWalled, "the branch middleware"},
	"POST /api/v1/delivery/routes/{id}/transitions":                {KeyBranchClassWalled, "the branch middleware"},
	"POST /api/v1/drafts/orders":                                   {KeyBranchClassWalled, "the branch middleware"},
	"POST /api/v1/drafts/orders/{id}/promote":                      {KeyBranchClassWalled, "the branch middleware"},
	"POST /api/v1/drafts/orders/{id}/transitions":                  {KeyBranchClassWalled, "the branch middleware"},
	"POST /api/v1/drafts/quotes":                                   {KeyBranchClassWalled, "the branch middleware"},
	"POST /api/v1/drafts/quotes/{id}/promote":                      {KeyBranchClassWalled, "the branch middleware"},
	"POST /api/v1/drafts/quotes/{id}/transitions":                  {KeyBranchClassWalled, "the branch middleware"},
	"POST /api/v1/inventory/adjust":                                {KeyBranchClassWalled, "the branch middleware"},
	"POST /api/v1/inventory/transfer":                              {KeyBranchClassWalled, "the branch middleware"},
	"POST /api/v1/invoices/{id}/email":                             {KeyBranchClassWalled, "the branch middleware"},
	"POST /api/v1/invoices/{id}/transitions":                       {KeyBranchClassWalled, "the branch middleware"},
	"POST /api/v1/invoices/{id}/write-offs":                        {KeyBranchClassWalled, "the branch middleware"},
	"POST /api/v1/locations":                                       {KeyBranchClassWalled, "the branch middleware"},
	"POST /api/v1/matching/run/{po_id}":                            {KeyBranchClassWalled, "the branch middleware"},
	"POST /api/v1/orders":                                          {KeyBranchClassWalled, "the branch middleware"},
	"POST /api/v1/orders/fulfillment-requests/{delivery_id}/retry": {KeyBranchClassWalled, "the branch middleware"},
	"POST /api/v1/orders/{id}/allocate":                            {KeyBranchClassWalled, "the branch middleware"},
	"POST /api/v1/orders/{id}/exposure-override":                   {KeyBranchClassWalled, "the branch middleware"},
	"POST /api/v1/orders/{id}/fulfillments":                        {KeyBranchClassWalled, "the branch middleware"},
	"POST /api/v1/orders/{id}/transitions":                         {KeyBranchClassWalled, "the branch middleware"},
	"POST /api/v1/payments":                                        {KeyBranchClassWalled, "the branch middleware"},
	"POST /api/v1/payments/card":                                   {KeyBranchClassWalled, "the branch middleware"},
	"POST /api/v1/payments/{id}/applications":                      {KeyBranchClassWalled, "the branch middleware"},
	"POST /api/v1/payments/{id}/refunds":                           {KeyBranchClassWalled, "the branch middleware"},
	"POST /api/v1/payments/{id}/transitions":                       {KeyBranchClassWalled, "the branch middleware"},
	"POST /api/v1/payments/intent":                                 {KeyBranchClassWalled, "the branch middleware"},
	"POST /api/v1/payment-terms":                                   {KeyBranchClassWalled, "the branch middleware"},
	"POST /api/v1/pos/returns":                                     {KeyBranchClassWalled, "the branch middleware"},
	"POST /api/v1/pos/sync":                                        {KeyBranchClassWalled, "the branch middleware"},
	"POST /api/v1/pos/till/{id}/close":                             {KeyBranchClassWalled, "the branch middleware"},
	"POST /api/v1/pos/till/open":                                   {KeyBranchClassWalled, "the branch middleware"},
	"POST /api/v1/pos/transactions":                                {KeyBranchClassWalled, "the branch middleware"},
	"POST /api/v1/pos/transactions/{id}/complete":                  {KeyBranchClassWalled, "the branch middleware"},
	"POST /api/v1/pos/transactions/{id}/items":                     {KeyBranchClassWalled, "the branch middleware"},
	"POST /api/v1/pos/transactions/{id}/void":                      {KeyBranchClassWalled, "the branch middleware"},
	"POST /api/v1/products":                                        {KeyBranchClassWalled, "the branch middleware"},
	"POST /api/v1/products/{id}/pim/generate/collateral":           {KeyBranchClassWalled, "the branch middleware"},
	"POST /api/v1/products/{id}/pim/generate/descriptions":         {KeyBranchClassWalled, "the branch middleware"},
	"POST /api/v1/products/{id}/pim/generate/image":                {KeyBranchClassWalled, "the branch middleware"},
	"POST /api/v1/products/{id}/pim/generate/seo":                  {KeyBranchClassWalled, "the branch middleware"},
	"POST /api/v1/purchase-orders":                                 {KeyBranchClassWalled, "the branch middleware"},
	"POST /api/v1/purchase-orders/{id}/freight":                    {KeyBranchClassWalled, "the branch middleware"},
	"POST /api/v1/purchase-orders/{id}/freight/{freightId}/apply":  {KeyBranchClassWalled, "the branch middleware"},
	"POST /api/v1/purchase-orders/{id}/receive":                    {KeyBranchClassWalled, "the branch middleware"},
	"POST /api/v1/purchase-orders/{id}/submit":                     {KeyBranchClassWalled, "the branch middleware"},
	"POST /api/v1/purchase-orders/refresh-reorder-targets":         {KeyBranchClassWalled, "the branch middleware"},
	"POST /api/v1/purchase-orders/reorder-check":                   {KeyBranchClassWalled, "the branch middleware"},
	"POST /api/v1/quotes":                                          {KeyBranchClassWalled, "the branch middleware"},
	"POST /api/v1/quotes/{id}/convert":                             {KeyBranchClassWalled, "the branch middleware"},
	"POST /api/v1/quotes/{id}/transitions":                         {KeyBranchClassWalled, "the branch middleware"},
	"PUT /api/v1/activities/{id}":                                  {KeyBranchClassWalled, "the branch middleware"},
	"PUT /api/v1/charge-codes/{id}":                                {KeyBranchClassWalled, "the branch middleware"},
	"PUT /api/v1/contacts/{id}":                                    {KeyBranchClassWalled, "the branch middleware"},
	"PUT /api/v1/credit-memos/{id}":                                {KeyBranchClassWalled, "the branch middleware"},
	"PUT /api/v1/customers/{id}":                                   {KeyBranchClassWalled, "the branch middleware"},
	"PUT /api/v1/customers/{id}/escalation-policy":                 {KeyBranchClassWalled, "the branch middleware"},
	"PUT /api/v1/drafts/orders/{id}":                               {KeyBranchClassWalled, "the branch middleware"},
	"PUT /api/v1/drafts/quotes/{id}":                               {KeyBranchClassWalled, "the branch middleware"},
	"PUT /api/v1/orders/{id}":                                      {KeyBranchClassWalled, "the branch middleware"},
	"PUT /api/v1/payment-terms/{id}":                               {KeyBranchClassWalled, "the branch middleware"},
	"PUT /api/v1/products/{id}/kit-components":                     {KeyBranchClassWalled, "the branch middleware"},
	"PUT /api/v1/products/{id}/pim/content":                        {KeyBranchClassWalled, "the branch middleware"},
	"PUT /api/v1/products/{id}/units":                              {KeyBranchClassWalled, "the branch middleware"},
	"PUT /api/v1/quotes/{id}":                                      {KeyBranchClassWalled, "the branch middleware"},
	"PUT /api/v1/quotes/{id}/file":                                 {KeyBranchClassWalled, "the branch middleware"},
	"PUT /api/v1/ship-tos/{id}":                                    {KeyBranchClassWalled, "the branch middleware"},
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
