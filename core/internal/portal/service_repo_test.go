// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package portal

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/customer"
	"github.com/gablelbm/gable/internal/inventory"
	"github.com/gablelbm/gable/internal/order"
	"github.com/gablelbm/gable/internal/pricing"
	"github.com/gablelbm/gable/internal/product"
	"github.com/gablelbm/gable/pkg/money"
	"github.com/google/uuid"
)

// These are the tests the concrete-*Repository gap used to block. Service now
// takes the Repository interface declared in repository.go, so the business
// logic that sits ABOVE the SQL is reachable: cart arithmetic, checkout
// assembly, the invite and role rules, the order change feed, and — the part
// that matters most — the fact that every read and write is handed the customer
// id from the session claims and nothing else.
//
// Division of labour with the two DB-backed suites:
//
//   - tenancy_pg_test.go proves the SQL actually filters on customer_id.
//   - THIS file proves the service never hands the SQL a customer id that did
//     not come from the session. Both halves are needed: a correct WHERE clause
//     called with the caller's id is not a tenant boundary.
//
// The collaborating services (customer, product, pricing, inventory, order) all
// already take repository interfaces, so they are built here as REAL services
// over fake repositories rather than mocked out — the pricing waterfall and the
// order module's own validation are part of what checkout has to satisfy.
//
// Tests are CORRECTNESS unless labelled CHARACTERIZATION.

// --- portal repository fake ----------------------------------------------

// scopeCall records a (subject, customer) pair a repository method was called
// with, so a test can assert the customer scope rather than only the answer.
type scopeCall struct {
	subject  uuid.UUID
	customer uuid.UUID
}

// String keeps failure messages readable — uuid.UUID is a [16]byte array, so
// the default %v formatting prints the raw bytes.
func (s scopeCall) String() string {
	return "{subject:" + s.subject.String() + " customer:" + s.customer.String() + "}"
}

type fakePortalRepo struct {
	// data
	cart          *CartDTO
	orders        []PortalOrderDTO
	invoices      []PortalInvoiceDTO
	deliveries    []PortalDeliveryDTO
	order         *PortalOrderDTO
	orderStatus   string
	inMotion      bool
	inMotionWhy   string
	projectOwned  bool
	arBalance     float64
	arCreditLimit float64
	arPastDue     float64
	newCartID     uuid.UUID
	newOrderID    uuid.UUID

	// failures
	getCartErr    error
	createCartErr error
	addItemErr    error
	updateQtyErr  error
	removeErr     error
	clearErr      error
	listOrdersErr error
	arErr         error
	projectErr    error
	setProjErr    error
	statusErr     error
	inMotionErr   error
	inviteErr     error
	roleErr       error
	statusWErr    error

	// recordings
	getCartFor    []uuid.UUID
	createCartFor []uuid.UUID
	addedItems    []addedItem
	updatedQty    []qtyUpdate
	removedItems  []scopeCall
	clearedCarts  []uuid.UUID
	listedOrders  []uuid.UUID
	filtered      []filteredCall
	orderReads    []scopeCall
	invoiceReads  []scopeCall
	deliveryReads []scopeCall
	arReads       []uuid.UUID
	projectChecks []scopeCall
	setProject    []setProjectCall
	statusReads   []scopeCall
	invites       []PortalInvite
	roleWrites    []roleWrite
	statusWrites  []roleWrite
	userLists     []uuid.UUID
	inviteLists   []uuid.UUID
	reorders      []scopeCall
}

type addedItem struct {
	cartID, productID uuid.UUID
	quantity          float64
	unitPrice         float64
}

type qtyUpdate struct {
	itemID   uuid.UUID
	quantity float64
	customer uuid.UUID
}

type filteredCall struct {
	customer uuid.UUID
	filter   OrderListFilter
}

type setProjectCall struct {
	orderID   uuid.UUID
	customer  uuid.UUID
	projectID *uuid.UUID
}

type roleWrite struct {
	userID, customer uuid.UUID
	value            string
}

var _ Repository = (*fakePortalRepo)(nil)

func newFakePortalRepo() *fakePortalRepo {
	return &fakePortalRepo{newCartID: uuid.New(), newOrderID: uuid.New(), projectOwned: true}
}

func (f *fakePortalRepo) GetCustomerUserByEmail(context.Context, string) (*CustomerUser, error) {
	return nil, errors.New("not found")
}
func (f *fakePortalRepo) GetPortalConfig(context.Context) (*PortalConfig, error) {
	return &PortalConfig{DealerName: "Gable Lumber & Supply"}, nil
}

func (f *fakePortalRepo) GetCustomerARSummary(_ context.Context, customerID uuid.UUID) (float64, float64, float64, error) {
	f.arReads = append(f.arReads, customerID)
	if f.arErr != nil {
		return 0, 0, 0, f.arErr
	}
	return f.arBalance, f.arCreditLimit, f.arPastDue, nil
}

func (f *fakePortalRepo) ListOrdersByCustomer(_ context.Context, customerID uuid.UUID) ([]PortalOrderDTO, error) {
	f.listedOrders = append(f.listedOrders, customerID)
	if f.listOrdersErr != nil {
		return nil, f.listOrdersErr
	}
	return f.orders, nil
}

func (f *fakePortalRepo) ListOrdersByCustomerFiltered(_ context.Context, customerID uuid.UUID, filter OrderListFilter) ([]PortalOrderDTO, error) {
	f.filtered = append(f.filtered, filteredCall{customerID, filter})
	if f.listOrdersErr != nil {
		return nil, f.listOrdersErr
	}
	return f.orders, nil
}

func (f *fakePortalRepo) GetOrderByIDAndCustomer(_ context.Context, orderID, customerID uuid.UUID) (*PortalOrderDTO, error) {
	f.orderReads = append(f.orderReads, scopeCall{orderID, customerID})
	if f.order == nil {
		return nil, ErrOrderNotFound
	}
	return f.order, nil
}

func (f *fakePortalRepo) GetOrderStatusForCustomer(_ context.Context, orderID, customerID uuid.UUID) (string, error) {
	f.statusReads = append(f.statusReads, scopeCall{orderID, customerID})
	if f.statusErr != nil {
		return "", f.statusErr
	}
	return f.orderStatus, nil
}

func (f *fakePortalRepo) OrderDeliveryInMotion(context.Context, uuid.UUID) (bool, string, error) {
	if f.inMotionErr != nil {
		return false, "", f.inMotionErr
	}
	return f.inMotion, f.inMotionWhy, nil
}

func (f *fakePortalRepo) ListInvoicesByCustomer(_ context.Context, customerID uuid.UUID) ([]PortalInvoiceDTO, error) {
	f.invoiceReads = append(f.invoiceReads, scopeCall{uuid.Nil, customerID})
	return f.invoices, nil
}

func (f *fakePortalRepo) GetInvoiceByIDAndCustomer(_ context.Context, invoiceID, customerID uuid.UUID) (*PortalInvoiceDTO, error) {
	f.invoiceReads = append(f.invoiceReads, scopeCall{invoiceID, customerID})
	if len(f.invoices) == 0 {
		return nil, errors.New("invoice not found")
	}
	return &f.invoices[0], nil
}

func (f *fakePortalRepo) ListDeliveriesByCustomer(_ context.Context, customerID uuid.UUID) ([]PortalDeliveryDTO, error) {
	f.deliveryReads = append(f.deliveryReads, scopeCall{uuid.Nil, customerID})
	return f.deliveries, nil
}

func (f *fakePortalRepo) GetDeliveryByIDAndCustomer(_ context.Context, deliveryID, customerID uuid.UUID) (*PortalDeliveryDTO, error) {
	f.deliveryReads = append(f.deliveryReads, scopeCall{deliveryID, customerID})
	if len(f.deliveries) == 0 {
		return nil, ErrDeliveryNotFound
	}
	return &f.deliveries[0], nil
}

func (f *fakePortalRepo) CreateReorder(_ context.Context, customerID, sourceOrderID uuid.UUID) (uuid.UUID, error) {
	f.reorders = append(f.reorders, scopeCall{sourceOrderID, customerID})
	return f.newOrderID, nil
}

func (f *fakePortalRepo) ProjectBelongsToCustomer(_ context.Context, projectID, customerID uuid.UUID) (bool, error) {
	f.projectChecks = append(f.projectChecks, scopeCall{projectID, customerID})
	if f.projectErr != nil {
		return false, f.projectErr
	}
	return f.projectOwned, nil
}

func (f *fakePortalRepo) SetOrderProject(_ context.Context, orderID, customerID uuid.UUID, projectID *uuid.UUID) error {
	f.setProject = append(f.setProject, setProjectCall{orderID, customerID, projectID})
	return f.setProjErr
}

func (f *fakePortalRepo) ListCatalogProducts(context.Context, CatalogFilter) ([]catalogRow, error) {
	return nil, nil
}
func (f *fakePortalRepo) GetCatalogProduct(context.Context, uuid.UUID) (*catalogRow, error) {
	return nil, errors.New("not found")
}
func (f *fakePortalRepo) ListProductCategories(context.Context) ([]categoryRow, error) {
	return nil, nil
}

func (f *fakePortalRepo) GetCartByCustomer(_ context.Context, customerID uuid.UUID) (*CartDTO, error) {
	f.getCartFor = append(f.getCartFor, customerID)
	if f.getCartErr != nil {
		return nil, f.getCartErr
	}
	if f.cart == nil {
		return nil, errors.New("cart not found")
	}
	return f.cart, nil
}

func (f *fakePortalRepo) CreateCart(_ context.Context, customerID uuid.UUID) (uuid.UUID, error) {
	f.createCartFor = append(f.createCartFor, customerID)
	if f.createCartErr != nil {
		return uuid.Nil, f.createCartErr
	}
	return f.newCartID, nil
}

func (f *fakePortalRepo) AddCartItem(_ context.Context, cartID, productID uuid.UUID, quantity, unitPrice float64) error {
	f.addedItems = append(f.addedItems, addedItem{cartID, productID, quantity, unitPrice})
	return f.addItemErr
}

func (f *fakePortalRepo) UpdateCartItemQty(_ context.Context, itemID uuid.UUID, quantity float64, customerID uuid.UUID) error {
	f.updatedQty = append(f.updatedQty, qtyUpdate{itemID, quantity, customerID})
	return f.updateQtyErr
}

func (f *fakePortalRepo) RemoveCartItem(_ context.Context, itemID, customerID uuid.UUID) error {
	f.removedItems = append(f.removedItems, scopeCall{itemID, customerID})
	return f.removeErr
}

func (f *fakePortalRepo) ClearCart(_ context.Context, cartID uuid.UUID) error {
	f.clearedCarts = append(f.clearedCarts, cartID)
	return f.clearErr
}

func (f *fakePortalRepo) ListCustomerUsers(_ context.Context, customerID uuid.UUID) ([]CustomerUser, error) {
	f.userLists = append(f.userLists, customerID)
	return nil, nil
}

func (f *fakePortalRepo) UpdateUserRole(_ context.Context, userID, customerID uuid.UUID, role string) error {
	f.roleWrites = append(f.roleWrites, roleWrite{userID, customerID, role})
	return f.roleErr
}

func (f *fakePortalRepo) UpdateUserStatus(_ context.Context, userID, customerID uuid.UUID, status string) error {
	f.statusWrites = append(f.statusWrites, roleWrite{userID, customerID, status})
	return f.statusWErr
}

func (f *fakePortalRepo) CreatePortalInvite(_ context.Context, invite PortalInvite) error {
	f.invites = append(f.invites, invite)
	return f.inviteErr
}

func (f *fakePortalRepo) ListPortalInvites(_ context.Context, customerID uuid.UUID) ([]PortalInvite, error) {
	f.inviteLists = append(f.inviteLists, customerID)
	return nil, nil
}

func (f *fakePortalRepo) LookupQuoteLineProduct(context.Context, uuid.UUID) (string, string, string, error) {
	return "", "", "", errors.New("not found")
}
func (f *fakePortalRepo) CreatePortalQuote(context.Context, uuid.UUID, portalQuoteInsert, []portalQuoteLineInsert) (uuid.UUID, error) {
	return uuid.Nil, errors.New("not implemented in this fake")
}
func (f *fakePortalRepo) ListPortalQuotes(context.Context, uuid.UUID) ([]PortalQuoteDTO, error) {
	return nil, nil
}
func (f *fakePortalRepo) GetPortalQuote(context.Context, uuid.UUID, uuid.UUID) (*PortalQuoteDTO, error) {
	return nil, ErrQuoteNotFound
}
func (f *fakePortalRepo) GetDeliveryRescheduleState(context.Context, uuid.UUID, uuid.UUID) (*deliveryRescheduleState, error) {
	return nil, ErrDeliveryNotFound
}
func (f *fakePortalRepo) CreateRescheduleRequest(context.Context, uuid.UUID, uuid.UUID, *uuid.UUID, time.Time, string) (uuid.UUID, error) {
	return uuid.Nil, errors.New("not implemented in this fake")
}
func (f *fakePortalRepo) GetRescheduleRequest(context.Context, uuid.UUID, uuid.UUID) (*DeliveryRescheduleDTO, error) {
	return nil, ErrDeliveryNotFound
}
func (f *fakePortalRepo) GetLatestRescheduleRequest(context.Context, uuid.UUID, uuid.UUID) (*DeliveryRescheduleDTO, error) {
	return nil, ErrDeliveryNotFound
}

// --- collaborator repository fakes ---------------------------------------
//
// customer, product, pricing, inventory and order all already accept a
// Repository interface, so the REAL services are built over these. Only the
// methods the portal actually reaches are given behaviour; the rest satisfy the
// interface and are never called.

type fakeCustomerRepo struct {
	customers map[uuid.UUID]*customer.Customer
	err       error
	gets      []uuid.UUID
}

var _ customer.Repository = (*fakeCustomerRepo)(nil)

func (f *fakeCustomerRepo) GetCustomer(_ context.Context, id uuid.UUID) (*customer.Customer, error) {
	f.gets = append(f.gets, id)
	if f.err != nil {
		return nil, f.err
	}
	c, ok := f.customers[id]
	if !ok {
		return nil, errors.New("customer not found")
	}
	return c, nil
}
func (f *fakeCustomerRepo) CreateCustomer(context.Context, *customer.Customer) error { return nil }
func (f *fakeCustomerRepo) GetCustomerByEmail(context.Context, string) (*customer.Customer, error) {
	return nil, errors.New("not found")
}
func (f *fakeCustomerRepo) ListCustomers(context.Context) ([]customer.Customer, error) {
	return nil, nil
}
func (f *fakeCustomerRepo) ListCustomersPaginated(context.Context, int, int) ([]customer.Customer, int, error) {
	return nil, 0, nil
}
func (f *fakeCustomerRepo) ListPriceLevels(context.Context) ([]customer.PriceLevel, error) {
	return nil, nil
}
func (f *fakeCustomerRepo) GetPriceLevel(context.Context, uuid.UUID) (*customer.PriceLevel, error) {
	return nil, errors.New("not found")
}
func (f *fakeCustomerRepo) UpdateBalance(context.Context, uuid.UUID, float64) error { return nil }
func (f *fakeCustomerRepo) UpdateSalesperson(context.Context, uuid.UUID, *uuid.UUID) error {
	return nil
}
func (f *fakeCustomerRepo) GetEscalationPolicy(context.Context, uuid.UUID) (*customer.EscalationPolicy, error) {
	return nil, errors.New("not found")
}
func (f *fakeCustomerRepo) SetEscalationPolicy(context.Context, *customer.EscalationPolicy) error {
	return nil
}
func (f *fakeCustomerRepo) CreateContact(context.Context, *customer.Contact) error { return nil }
func (f *fakeCustomerRepo) GetContact(context.Context, uuid.UUID) (*customer.Contact, error) {
	return nil, errors.New("not found")
}
func (f *fakeCustomerRepo) ListContactsByCustomer(context.Context, uuid.UUID) ([]customer.Contact, error) {
	return nil, nil
}
func (f *fakeCustomerRepo) UpdateContact(context.Context, *customer.Contact) error { return nil }
func (f *fakeCustomerRepo) DeleteContact(context.Context, uuid.UUID) error         { return nil }

type fakeProductRepo struct {
	products map[uuid.UUID]*product.Product
	gets     []uuid.UUID
}

var _ product.Repository = (*fakeProductRepo)(nil)

func (f *fakeProductRepo) GetProduct(_ context.Context, id uuid.UUID) (*product.Product, error) {
	f.gets = append(f.gets, id)
	p, ok := f.products[id]
	if !ok {
		return nil, errors.New("product not found")
	}
	return p, nil
}
func (f *fakeProductRepo) CreateProduct(context.Context, *product.Product) error { return nil }
func (f *fakeProductRepo) ListProducts(context.Context) ([]product.Product, error) {
	return nil, nil
}
func (f *fakeProductRepo) ListProductsPaginated(context.Context, int, int) ([]product.Product, int, error) {
	return nil, 0, nil
}
func (f *fakeProductRepo) ListBelowReorder(context.Context) ([]product.ReorderAlert, error) {
	return nil, nil
}
func (f *fakeProductRepo) UpdateAverageCost(context.Context, uuid.UUID, float64) error { return nil }
func (f *fakeProductRepo) UpdateMarginRules(context.Context, uuid.UUID, float64, float64) error {
	return nil
}
func (f *fakeProductRepo) UpdateReorderTargets(context.Context, uuid.UUID, float64, float64) error {
	return nil
}
func (f *fakeProductRepo) UpdateVendor(context.Context, uuid.UUID, *string, *uuid.UUID) error {
	return nil
}
func (f *fakeProductRepo) UpdateDimensions(context.Context, uuid.UUID, product.Geometry) error {
	return nil
}
func (f *fakeProductRepo) UpdateLeadTime(context.Context, uuid.UUID, *int) error { return nil }

type fakePricingRepo struct {
	contracts map[uuid.UUID]float64 // productID -> contract price
	err       error
}

var _ pricing.Repository = (*fakePricingRepo)(nil)

func (f *fakePricingRepo) GetContract(_ context.Context, customerID, productID uuid.UUID) (*pricing.CustomerContract, error) {
	if f.err != nil {
		return nil, f.err
	}
	price, ok := f.contracts[productID]
	if !ok {
		return nil, nil // no contract
	}
	return &pricing.CustomerContract{CustomerID: customerID, ProductID: productID, ContractPrice: price}, nil
}
func (f *fakePricingRepo) CreateContract(context.Context, *pricing.CustomerContract) error {
	return nil
}
func (f *fakePricingRepo) GetMatchingRules(context.Context, uuid.UUID, *uuid.UUID, *uuid.UUID, float64) ([]pricing.PricingRule, error) {
	return nil, nil
}
func (f *fakePricingRepo) ListBreakQuantities(context.Context, uuid.UUID, *uuid.UUID) ([]float64, error) {
	return nil, nil
}
func (f *fakePricingRepo) ListRules(context.Context) ([]pricing.PricingRule, error) { return nil, nil }
func (f *fakePricingRepo) CreateRule(context.Context, *pricing.PricingRule) error   { return nil }

type fakeInventoryRepo struct {
	byProduct map[uuid.UUID][]inventory.Inventory
}

var _ inventory.Repository = (*fakeInventoryRepo)(nil)

func (f *fakeInventoryRepo) ListInventoryByProduct(_ context.Context, productID uuid.UUID) ([]inventory.Inventory, error) {
	return f.byProduct[productID], nil
}
func (f *fakeInventoryRepo) GetInventory(context.Context, uuid.UUID, *uuid.UUID) (*inventory.Inventory, error) {
	return nil, errors.New("not found")
}
func (f *fakeInventoryRepo) UpdateInventory(context.Context, *inventory.Inventory) error { return nil }
func (f *fakeInventoryRepo) CreateInventory(context.Context, *inventory.Inventory) error { return nil }
func (f *fakeInventoryRepo) ListInventoryByProductAndBranch(context.Context, uuid.UUID, *uuid.UUID) ([]inventory.Inventory, error) {
	return nil, nil
}
func (f *fakeInventoryRepo) LocationBranchID(context.Context, uuid.UUID) (*uuid.UUID, error) {
	return nil, nil
}
func (f *fakeInventoryRepo) AllocateStock(context.Context, uuid.UUID, float64) error   { return nil }
func (f *fakeInventoryRepo) DeallocateStock(context.Context, uuid.UUID, float64) error { return nil }
func (f *fakeInventoryRepo) FulfillStock(context.Context, uuid.UUID, float64) error    { return nil }
func (f *fakeInventoryRepo) RevertFulfillStock(context.Context, uuid.UUID, float64) error {
	return nil
}
func (f *fakeInventoryRepo) ExecuteInTx(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

type fakeOrderRepo struct {
	created  []order.Order
	orders   map[uuid.UUID]*order.Order
	statuses []order.OrderStatus
	createEr error
}

var _ order.Repository = (*fakeOrderRepo)(nil)

func (f *fakeOrderRepo) CreateOrder(_ context.Context, o *order.Order) error {
	if f.createEr != nil {
		return f.createEr
	}
	if o.ID == uuid.Nil {
		o.ID = uuid.New()
	}
	f.created = append(f.created, *o)
	if f.orders == nil {
		f.orders = map[uuid.UUID]*order.Order{}
	}
	cp := *o
	f.orders[o.ID] = &cp
	return nil
}
func (f *fakeOrderRepo) GetOrder(_ context.Context, id uuid.UUID) (*order.Order, error) {
	o, ok := f.orders[id]
	if !ok {
		return nil, errors.New("order not found")
	}
	return o, nil
}
func (f *fakeOrderRepo) ListOrders(context.Context) ([]order.Order, error) { return nil, nil }
func (f *fakeOrderRepo) ListOrdersPaginated(context.Context, int, int) ([]order.Order, int, error) {
	return nil, 0, nil
}
func (f *fakeOrderRepo) UpdateStatus(_ context.Context, id uuid.UUID, status order.OrderStatus) error {
	f.statuses = append(f.statuses, status)
	if o, ok := f.orders[id]; ok {
		o.Status = status
	}
	return nil
}

// --- service builder -----------------------------------------------------

// portalRig is a fully wired portal Service plus every fake behind it, so a
// test can arrange data and then assert on what reached each seam.
type portalRig struct {
	svc *Service

	repo      *fakePortalRepo
	customers *fakeCustomerRepo
	products  *fakeProductRepo
	prices    *fakePricingRepo
	stock     *fakeInventoryRepo
	orders    *fakeOrderRepo
}

func newPortalRig(t *testing.T) *portalRig {
	t.Helper()

	rig := &portalRig{
		repo:      newFakePortalRepo(),
		customers: &fakeCustomerRepo{customers: map[uuid.UUID]*customer.Customer{}},
		products:  &fakeProductRepo{products: map[uuid.UUID]*product.Product{}},
		prices:    &fakePricingRepo{contracts: map[uuid.UUID]float64{}},
		stock:     &fakeInventoryRepo{byProduct: map[uuid.UUID][]inventory.Inventory{}},
		orders:    &fakeOrderRepo{orders: map[uuid.UUID]*order.Order{}},
	}

	customerSvc := customer.NewService(rig.customers)
	// order.Service without a *database.DB takes its documented no-transaction
	// fallback, which is exactly the seam this rig needs.
	orderSvc := order.NewService(rig.orders, inventory.NewService(rig.stock), nil, customerSvc, nil)

	rig.svc = NewService(
		rig.repo,
		testSecret,
		testLogger(),
		pricing.NewService(rig.prices),
		customerSvc,
		inventory.NewService(rig.stock),
		orderSvc,
		product.NewService(rig.products),
	)
	return rig
}

// withCustomer registers a customer so the pricing waterfall has a tier to
// resolve against.
func (r *portalRig) withCustomer(id uuid.UUID, tier customer.CustomerTier) *customer.Customer {
	c := &customer.Customer{ID: id, Name: "Acme Framing", Tier: tier, IsActive: true}
	r.customers.customers[id] = c
	return c
}

func (r *portalRig) withProduct(id uuid.UUID, sku string, basePrice float64) {
	r.products.products[id] = &product.Product{ID: id, SKU: sku, Description: sku, BasePrice: basePrice}
}

// --- GetCart -------------------------------------------------------------

// CORRECTNESS: a customer with no cart yet gets one created for THEM, and the
// empty cart is returned with an initialised item slice (a nil Items would
// serialise as null and break a consumer that maps over it).
func TestGetCart_CreatesOneScopedToTheSessionCustomer(t *testing.T) {
	rig := newPortalRig(t)
	me := uuid.New()

	cart, err := rig.svc.GetCart(context.Background(), me)
	if err != nil {
		t.Fatalf("GetCart: %v", err)
	}

	if len(rig.repo.getCartFor) != 1 || rig.repo.getCartFor[0] != me {
		t.Errorf("looked up the cart for %v, want the session customer %s", rig.repo.getCartFor, me)
	}
	if len(rig.repo.createCartFor) != 1 || rig.repo.createCartFor[0] != me {
		t.Errorf("created a cart for %v, want the session customer %s", rig.repo.createCartFor, me)
	}
	if cart.ID != rig.repo.newCartID {
		t.Errorf("cart id = %s, want the newly created %s", cart.ID, rig.repo.newCartID)
	}
	if cart.Items == nil {
		t.Error("Items is nil; an empty cart must carry an initialised slice so it serialises as []")
	}
	if len(cart.Items) != 0 || cart.Subtotal != 0 {
		t.Errorf("a new cart is not empty: %+v", cart)
	}
}

// CORRECTNESS: an existing cart is returned untouched — no second cart is
// created for a customer who already has one.
func TestGetCart_ReturnsTheExistingCart(t *testing.T) {
	rig := newPortalRig(t)
	me := uuid.New()
	existing := uuid.New()
	rig.repo.cart = &CartDTO{ID: existing, Items: []CartItemDTO{{ID: uuid.New(), Quantity: 2, UnitPrice: 4.75, LineTotal: 9.50}}, ItemCount: 1, Subtotal: 9.50}

	cart, err := rig.svc.GetCart(context.Background(), me)
	if err != nil {
		t.Fatalf("GetCart: %v", err)
	}
	if cart.ID != existing {
		t.Errorf("cart id = %s, want the existing %s", cart.ID, existing)
	}
	if len(rig.repo.createCartFor) != 0 {
		t.Errorf("a second cart was created for a customer who already had one: %v", rig.repo.createCartFor)
	}
	if cart.Subtotal != 9.50 {
		t.Errorf("subtotal = %v, want the repository's 9.50", cart.Subtotal)
	}
}

// CHARACTERIZATION: GetCart cannot tell "no cart yet" from "the cart query
// failed" — GetCartByCustomer returns a wrapped error for both — so any
// repository failure is answered with a freshly created, EMPTY cart.
//
// The blast radius is bounded because CreateCart is an upsert
// (ON CONFLICT (customer_id) DO UPDATE ... RETURNING id, repository.go:656),
// so the customer's real cart row is not lost. What is lost is the contents:
// a transient failure while reading the item rows is reported to the contractor
// as "your cart is empty", and Checkout then refuses with "cart is empty"
// rather than surfacing the fault.
//
// backend/internal/portal/cart.go:17-31 —
//
//	cart, err := s.repo.GetCartByCustomer(ctx, customerID)
//	if err != nil { /* create a new one */ }
func TestGetCart_AnyRepositoryFailureLooksLikeAnEmptyCart(t *testing.T) {
	rig := newPortalRig(t)
	rig.repo.cart = &CartDTO{ID: uuid.New(), Items: []CartItemDTO{{Quantity: 5, UnitPrice: 4.75}}}
	rig.repo.getCartErr = errors.New("failed to list cart items: connection reset by peer")

	cart, err := rig.svc.GetCart(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("GetCart returned an error; this test documents that it does NOT: %v", err)
	}
	if len(cart.Items) != 0 {
		t.Errorf("items = %+v, want the empty cart this path currently produces", cart.Items)
	}
	if len(rig.repo.createCartFor) != 1 {
		t.Errorf("CreateCart ran %d times, want 1 — the error was treated as 'no cart'", len(rig.repo.createCartFor))
	}
}

// CORRECTNESS: if the cart cannot be created either, the failure is surfaced.
func TestGetCart_CreateFailureIsReturned(t *testing.T) {
	rig := newPortalRig(t)
	rig.repo.createCartErr = errors.New("failed to create cart")

	if _, err := rig.svc.GetCart(context.Background(), uuid.New()); err == nil {
		t.Fatal("a cart-creation failure was swallowed")
	}
}

// --- AddToCart -----------------------------------------------------------

// CORRECTNESS: quantity is validated before anything is read or written. Zero
// and negative quantities are the two that would otherwise produce a cart line
// that subtracts from the subtotal.
func TestAddToCart_RejectsNonPositiveQuantityBeforeAnyIO(t *testing.T) {
	for _, qty := range []float64{0, -1, -0.5} {
		rig := newPortalRig(t)
		_, err := rig.svc.AddToCart(context.Background(), uuid.New(),
			AddToCartRequest{ProductID: uuid.New(), Quantity: qty})
		if err == nil {
			t.Errorf("quantity %v was accepted", qty)
		}
		if len(rig.repo.getCartFor) != 0 || len(rig.repo.addedItems) != 0 {
			t.Errorf("quantity %v reached the repository: gets=%v adds=%v", qty, rig.repo.getCartFor, rig.repo.addedItems)
		}
		if len(rig.products.gets) != 0 {
			t.Errorf("quantity %v reached the product service", qty)
		}
	}
}

// CORRECTNESS: the line is priced with THIS customer's price, not the shelf
// price. The contract price is the strongest rung of the waterfall, so a cart
// line that carries base_price instead means the contractor is quoted — and
// then charged, since checkout copies the cart's unit price onto the order —
// the wrong number.
func TestAddToCart_WritesTheCustomerPriceNotTheBasePrice(t *testing.T) {
	rig := newPortalRig(t)
	me, productID := uuid.New(), uuid.New()
	rig.withCustomer(me, customer.TierRetail)
	rig.withProduct(productID, "2X4-8", 4.75)
	rig.prices.contracts[productID] = 3.95 // negotiated

	if _, err := rig.svc.AddToCart(context.Background(), me,
		AddToCartRequest{ProductID: productID, Quantity: 12}); err != nil {
		t.Fatalf("AddToCart: %v", err)
	}

	if len(rig.repo.addedItems) != 1 {
		t.Fatalf("AddCartItem called %d times, want 1", len(rig.repo.addedItems))
	}
	got := rig.repo.addedItems[0]
	if got.unitPrice != 3.95 {
		t.Errorf("unit price = %v, want the contract price 3.95 (base is 4.75)", got.unitPrice)
	}
	if got.quantity != 12 {
		t.Errorf("quantity = %v, want 12", got.quantity)
	}
	if got.productID != productID {
		t.Errorf("product = %s, want %s", got.productID, productID)
	}
	if got.cartID != rig.repo.newCartID {
		t.Errorf("wrote into cart %s, want the session customer's cart %s", got.cartID, rig.repo.newCartID)
	}
}

// CORRECTNESS: with no contract and no rules, the tier discount applies. Gold
// is 15% off, so 4.75 becomes 4.0375 — the portal keeps float64 dollars on the
// wire, and this is the number checkout will later round into cents.
func TestAddToCart_AppliesTheTierDiscount(t *testing.T) {
	rig := newPortalRig(t)
	me, productID := uuid.New(), uuid.New()
	rig.withCustomer(me, customer.TierGold)
	rig.withProduct(productID, "2X4-8", 4.75)

	if _, err := rig.svc.AddToCart(context.Background(), me,
		AddToCartRequest{ProductID: productID, Quantity: 1}); err != nil {
		t.Fatalf("AddToCart: %v", err)
	}

	want := 4.75 * 0.85
	if got := rig.repo.addedItems[0].unitPrice; got != want {
		t.Errorf("unit price = %v, want the Gold-tier %v", got, want)
	}
}

// CORRECTNESS: when the customer cannot be resolved the line falls back to the
// product's base price rather than to zero. A zero-priced cart line becomes a
// zero-priced order line at checkout, which is a free order.
func TestAddToCart_UnknownCustomerFallsBackToBasePriceNotZero(t *testing.T) {
	rig := newPortalRig(t)
	productID := uuid.New()
	rig.withProduct(productID, "2X4-8", 4.75)
	// deliberately no customer registered

	if _, err := rig.svc.AddToCart(context.Background(), uuid.New(),
		AddToCartRequest{ProductID: productID, Quantity: 1}); err != nil {
		t.Fatalf("AddToCart: %v", err)
	}
	if got := rig.repo.addedItems[0].unitPrice; got != 4.75 {
		t.Errorf("unit price = %v, want the base price 4.75", got)
	}
}

// CORRECTNESS: a pricing failure also falls back to base price rather than
// zero, and does not fail the add.
func TestAddToCart_PricingFailureFallsBackToBasePrice(t *testing.T) {
	rig := newPortalRig(t)
	me, productID := uuid.New(), uuid.New()
	rig.withCustomer(me, customer.TierGold)
	rig.withProduct(productID, "2X4-8", 4.75)
	rig.prices.err = errors.New("pricing rules table is unavailable")

	if _, err := rig.svc.AddToCart(context.Background(), me,
		AddToCartRequest{ProductID: productID, Quantity: 1}); err != nil {
		t.Fatalf("AddToCart: %v", err)
	}
	if got := rig.repo.addedItems[0].unitPrice; got != 4.75 {
		t.Errorf("unit price = %v, want the base price 4.75 after a pricing failure", got)
	}
}

// CORRECTNESS: an unknown product is refused, and nothing is written.
func TestAddToCart_UnknownProductIsRefusedWithoutAWrite(t *testing.T) {
	rig := newPortalRig(t)
	me := uuid.New()
	rig.withCustomer(me, customer.TierRetail)

	_, err := rig.svc.AddToCart(context.Background(), me,
		AddToCartRequest{ProductID: uuid.New(), Quantity: 1})
	if err == nil {
		t.Fatal("an unknown product was accepted")
	}
	if len(rig.repo.addedItems) != 0 {
		t.Errorf("a line was written for an unknown product: %+v", rig.repo.addedItems)
	}
}

// --- UpdateCartItem / RemoveCartItem -------------------------------------

// CORRECTNESS (security): the item id is caller-supplied but the customer id is
// not — both are handed to the repository so its WHERE clause can refuse an
// item belonging to another contractor's cart. If the service ever stopped
// passing the session customer, the SQL scope would have nothing to filter on.
func TestUpdateCartItem_PassesTheSessionCustomerAsTheScope(t *testing.T) {
	rig := newPortalRig(t)
	me, itemID := uuid.New(), uuid.New()
	rig.repo.cart = &CartDTO{ID: uuid.New(), Items: []CartItemDTO{}}

	if _, err := rig.svc.UpdateCartItem(context.Background(), me, itemID,
		UpdateCartItemRequest{Quantity: 7}); err != nil {
		t.Fatalf("UpdateCartItem: %v", err)
	}

	if len(rig.repo.updatedQty) != 1 {
		t.Fatalf("UpdateCartItemQty called %d times, want 1", len(rig.repo.updatedQty))
	}
	got := rig.repo.updatedQty[0]
	if got.itemID != itemID {
		t.Errorf("item = %s, want the requested %s", got.itemID, itemID)
	}
	if got.customer != me {
		t.Errorf("scope = %s, want the session customer %s", got.customer, me)
	}
	if got.quantity != 7 {
		t.Errorf("quantity = %v, want 7", got.quantity)
	}
}

// CORRECTNESS: a non-positive quantity is refused before the write. Updating to
// zero is a delete dressed as an update and must go through RemoveCartItem.
func TestUpdateCartItem_RejectsNonPositiveQuantityBeforeTheWrite(t *testing.T) {
	for _, qty := range []float64{0, -1} {
		rig := newPortalRig(t)
		_, err := rig.svc.UpdateCartItem(context.Background(), uuid.New(), uuid.New(),
			UpdateCartItemRequest{Quantity: qty})
		if err == nil {
			t.Errorf("quantity %v was accepted", qty)
		}
		if len(rig.repo.updatedQty) != 0 {
			t.Errorf("quantity %v reached the repository: %+v", qty, rig.repo.updatedQty)
		}
	}
}

// CORRECTNESS (security): removal is scoped the same way.
func TestRemoveCartItem_PassesTheSessionCustomerAsTheScope(t *testing.T) {
	rig := newPortalRig(t)
	me, itemID := uuid.New(), uuid.New()
	rig.repo.cart = &CartDTO{ID: uuid.New(), Items: []CartItemDTO{}}

	if _, err := rig.svc.RemoveCartItem(context.Background(), me, itemID); err != nil {
		t.Fatalf("RemoveCartItem: %v", err)
	}
	if len(rig.repo.removedItems) != 1 {
		t.Fatalf("RemoveCartItem called %d times, want 1", len(rig.repo.removedItems))
	}
	if rig.repo.removedItems[0] != (scopeCall{itemID, me}) {
		t.Errorf("removed %+v, want {%s %s}", rig.repo.removedItems[0], itemID, me)
	}
}

// CORRECTNESS: a failed removal is surfaced rather than answered with a cart
// that still contains the item and a 200.
func TestRemoveCartItem_FailureIsReturned(t *testing.T) {
	rig := newPortalRig(t)
	rig.repo.removeErr = errors.New("failed to remove cart item")

	if _, err := rig.svc.RemoveCartItem(context.Background(), uuid.New(), uuid.New()); err == nil {
		t.Fatal("a failed removal was reported as success")
	}
}

// --- Checkout ------------------------------------------------------------

// cartWith builds a cart of (quantity, unitPrice) lines for the checkout tests.
func cartWith(lines ...[2]float64) *CartDTO {
	cart := &CartDTO{ID: uuid.New(), Items: make([]CartItemDTO, 0, len(lines))}
	for _, l := range lines {
		cart.Items = append(cart.Items, CartItemDTO{
			ID: uuid.New(), ProductID: uuid.New(),
			Quantity: l[0], UnitPrice: l[1], LineTotal: l[0] * l[1],
		})
		cart.Subtotal += l[0] * l[1]
	}
	cart.ItemCount = len(cart.Items)
	return cart
}

// CORRECTNESS: checkout is the dollars-to-cents boundary. Every cart line's
// float64 dollar unit price becomes an int64 cent price on the order line,
// rounded half away from zero — the same rule pkg/money.DollarsToCents applies
// everywhere else in the ledger. Truncation here loses a cent per line on the
// binary-float cases (8.20*100 is 819.99999999999989 in float64).
func TestCheckout_ConvertsEachCartLineToCents(t *testing.T) {
	rig := newPortalRig(t)
	me := uuid.New()
	rig.withCustomer(me, customer.TierRetail)
	rig.repo.cart = cartWith(
		[2]float64{10, 8.20},   // the classic 819.99999999999989 case
		[2]float64{3, 0.29},    // 28.999999999999996
		[2]float64{1, 1.15},    // 114.99999999999999
		[2]float64{2, 1234.56}, //
		[2]float64{5, 4.75},    //
	)

	resp, err := rig.svc.Checkout(context.Background(), me, CheckoutRequest{})
	if err != nil {
		t.Fatalf("Checkout: %v", err)
	}
	if resp.OrderID == uuid.Nil {
		t.Fatal("checkout returned no order id")
	}

	if len(rig.orders.created) != 1 {
		t.Fatalf("CreateOrder ran %d times, want 1", len(rig.orders.created))
	}
	got := rig.orders.created[0]
	if len(got.Lines) != len(rig.repo.cart.Items) {
		t.Fatalf("order has %d lines, want %d", len(got.Lines), len(rig.repo.cart.Items))
	}

	for i, item := range rig.repo.cart.Items {
		want := money.DollarsToCents(item.UnitPrice)
		if got.Lines[i].PriceEach != want {
			t.Errorf("line %d price_each = %d cents, want %d (from $%v)",
				i, got.Lines[i].PriceEach, want, item.UnitPrice)
		}
		if got.Lines[i].Quantity != item.Quantity {
			t.Errorf("line %d quantity = %v, want %v", i, got.Lines[i].Quantity, item.Quantity)
		}
		if got.Lines[i].ProductID != item.ProductID {
			t.Errorf("line %d product = %s, want %s", i, got.Lines[i].ProductID, item.ProductID)
		}
	}

	// And the order total the ERP computed from those cents matches the cart.
	var wantTotal int64
	for _, item := range rig.repo.cart.Items {
		wantTotal += money.RoundToCents(item.Quantity * float64(money.DollarsToCents(item.UnitPrice)))
	}
	if got.TotalAmount != wantTotal {
		t.Errorf("order total = %d cents, want %d", got.TotalAmount, wantTotal)
	}
}

// CORRECTNESS: the order is filed against the session's customer, and the cart
// is cleared afterwards so a refresh does not place it twice.
func TestCheckout_FilesAgainstTheSessionCustomerAndClearsTheCart(t *testing.T) {
	rig := newPortalRig(t)
	me := uuid.New()
	rig.withCustomer(me, customer.TierRetail)
	rig.repo.cart = cartWith([2]float64{2, 4.75})
	cartID := rig.repo.cart.ID

	if _, err := rig.svc.Checkout(context.Background(), me, CheckoutRequest{}); err != nil {
		t.Fatalf("Checkout: %v", err)
	}

	if rig.orders.created[0].CustomerID != me {
		t.Errorf("order filed for %s, want the session customer %s", rig.orders.created[0].CustomerID, me)
	}
	if len(rig.repo.clearedCarts) != 1 || rig.repo.clearedCarts[0] != cartID {
		t.Errorf("cleared %v, want the checked-out cart %s", rig.repo.clearedCarts, cartID)
	}
}

// CORRECTNESS: an empty cart is refused before an order is created. An order
// with no lines is rejected by the ERP anyway, but the portal must say "your
// cart is empty" rather than surface an ERP validation error.
func TestCheckout_EmptyCartIsRefusedBeforeTheOrder(t *testing.T) {
	rig := newPortalRig(t)
	rig.repo.cart = &CartDTO{ID: uuid.New(), Items: []CartItemDTO{}}

	_, err := rig.svc.Checkout(context.Background(), uuid.New(), CheckoutRequest{})
	if err == nil {
		t.Fatal("an empty cart was checked out")
	}
	if len(rig.orders.created) != 0 {
		t.Errorf("an order was created from an empty cart: %+v", rig.orders.created)
	}
	if len(rig.repo.clearedCarts) != 0 {
		t.Errorf("the cart was cleared on a refused checkout: %v", rig.repo.clearedCarts)
	}
}

// CORRECTNESS (security): project_id arrives in the request body and is
// therefore caller-controlled. It must be verified against the session's
// customer BEFORE the order is written — verifying afterwards would leave a
// real order filed against another contractor's job, and the order DTO reads
// that job's name back.
func TestCheckout_ForeignProjectIsRefusedBeforeTheOrderExists(t *testing.T) {
	rig := newPortalRig(t)
	me, theirProject := uuid.New(), uuid.New()
	rig.withCustomer(me, customer.TierRetail)
	rig.repo.cart = cartWith([2]float64{2, 4.75})
	rig.repo.projectOwned = false

	_, err := rig.svc.Checkout(context.Background(), me, CheckoutRequest{ProjectID: &theirProject})
	if !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("error = %v, want ErrProjectNotFound", err)
	}
	if len(rig.repo.projectChecks) != 1 || rig.repo.projectChecks[0] != (scopeCall{theirProject, me}) {
		t.Errorf("ownership checked as %+v, want {%s %s}", rig.repo.projectChecks, theirProject, me)
	}
	if len(rig.orders.created) != 0 {
		t.Errorf("an order was created against a foreign project: %+v", rig.orders.created)
	}
	if len(rig.repo.setProject) != 0 {
		t.Errorf("the project was attached anyway: %+v", rig.repo.setProject)
	}
	if len(rig.repo.clearedCarts) != 0 {
		t.Errorf("the cart was cleared on a refused checkout: %v", rig.repo.clearedCarts)
	}
}

// CORRECTNESS: an owned project is attached to the new order, scoped to the
// session customer.
func TestCheckout_OwnedProjectIsAttachedToTheNewOrder(t *testing.T) {
	rig := newPortalRig(t)
	me, myProject := uuid.New(), uuid.New()
	rig.withCustomer(me, customer.TierRetail)
	rig.repo.cart = cartWith([2]float64{2, 4.75})

	resp, err := rig.svc.Checkout(context.Background(), me, CheckoutRequest{ProjectID: &myProject})
	if err != nil {
		t.Fatalf("Checkout: %v", err)
	}
	if len(rig.repo.setProject) != 1 {
		t.Fatalf("SetOrderProject ran %d times, want 1", len(rig.repo.setProject))
	}
	got := rig.repo.setProject[0]
	if got.orderID != resp.OrderID {
		t.Errorf("attached order %s, want the new order %s", got.orderID, resp.OrderID)
	}
	if got.customer != me {
		t.Errorf("attach scope = %s, want the session customer %s", got.customer, me)
	}
	if got.projectID == nil || *got.projectID != myProject {
		t.Errorf("attached project = %v, want %s", got.projectID, myProject)
	}
}

// CORRECTNESS: failing to attach the project does NOT fail the checkout. The
// order is real and the customer's stock is committed; losing the board
// grouping is recoverable via PUT /orders/{id}/project, losing the order is not.
func TestCheckout_ProjectAttachFailureIsNotFatal(t *testing.T) {
	rig := newPortalRig(t)
	me, myProject := uuid.New(), uuid.New()
	rig.withCustomer(me, customer.TierRetail)
	rig.repo.cart = cartWith([2]float64{2, 4.75})
	rig.repo.setProjErr = errors.New("failed to set order project")

	resp, err := rig.svc.Checkout(context.Background(), me, CheckoutRequest{ProjectID: &myProject})
	if err != nil {
		t.Fatalf("a failed project attach aborted the checkout: %v", err)
	}
	if resp.OrderID == uuid.Nil {
		t.Error("no order id was returned")
	}
	if len(rig.orders.created) != 1 {
		t.Errorf("the order was not created: %+v", rig.orders.created)
	}
}

// CORRECTNESS: failing to clear the cart does not fail the checkout either —
// the order exists, and reporting a failure would invite the contractor to
// place it a second time.
func TestCheckout_ClearFailureIsNotFatal(t *testing.T) {
	rig := newPortalRig(t)
	me := uuid.New()
	rig.withCustomer(me, customer.TierRetail)
	rig.repo.cart = cartWith([2]float64{2, 4.75})
	rig.repo.clearErr = errors.New("failed to clear cart")

	resp, err := rig.svc.Checkout(context.Background(), me, CheckoutRequest{})
	if err != nil {
		t.Fatalf("a failed cart clear aborted the checkout: %v", err)
	}
	if resp.OrderID == uuid.Nil {
		t.Error("no order id was returned")
	}
}

// CORRECTNESS: if the ERP refuses the order, checkout fails and the cart is
// NOT cleared — the contractor's basket must survive a failed submission.
func TestCheckout_OrderFailureLeavesTheCartIntact(t *testing.T) {
	rig := newPortalRig(t)
	me := uuid.New()
	rig.withCustomer(me, customer.TierRetail)
	rig.repo.cart = cartWith([2]float64{2, 4.75})
	rig.orders.createEr = errors.New("failed to create order")

	if _, err := rig.svc.Checkout(context.Background(), me, CheckoutRequest{}); err == nil {
		t.Fatal("a failed order was reported as a successful checkout")
	}
	if len(rig.repo.clearedCarts) != 0 {
		t.Errorf("the cart was cleared despite the order failing: %v", rig.repo.clearedCarts)
	}
}

// CHARACTERIZATION: CheckoutRequest carries DeliveryMethod, DeliveryAddress,
// PaymentMethod and Notes, and Checkout persists none of them — only
// DeliveryMethod is logged (cart.go:156-161). A contractor who picks PICKUP and
// types a site address gets an order that records neither. This is pinned so
// that wiring them through is a deliberate change with a failing test, not a
// silent one.
func TestCheckout_DeliveryAndPaymentFieldsAreNotPersisted(t *testing.T) {
	rig := newPortalRig(t)
	me := uuid.New()
	rig.withCustomer(me, customer.TierRetail)
	rig.repo.cart = cartWith([2]float64{2, 4.75})

	if _, err := rig.svc.Checkout(context.Background(), me, CheckoutRequest{
		DeliveryMethod:  "PICKUP",
		DeliveryAddress: "1200 Maple St",
		PaymentMethod:   "CARD",
		Notes:           "call on arrival",
	}); err != nil {
		t.Fatalf("Checkout: %v", err)
	}

	o := rig.orders.created[0]
	if o.QuoteID != nil {
		t.Errorf("quote_id = %v, want nil for a cart checkout", o.QuoteID)
	}
	// The order module has no field for any of the four, so the only assertion
	// available is that the order was built from the cart alone.
	if len(o.Lines) != 1 || o.Lines[0].IsSpecialOrder {
		t.Errorf("order lines = %+v, want one plain line built from the cart", o.Lines)
	}
}

// --- dashboard -----------------------------------------------------------

// CORRECTNESS: the dashboard shows at most five recent orders, and they are the
// five the repository returned first (its query is created_at DESC), so the
// contractor sees the newest rather than an arbitrary five.
func TestGetDashboard_ShowsTheFiveNewestOrders(t *testing.T) {
	rig := newPortalRig(t)
	me := uuid.New()
	rig.repo.arBalance, rig.repo.arCreditLimit, rig.repo.arPastDue = 12500.50, 50000, 250.25

	base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 9; i++ {
		rig.repo.orders = append(rig.repo.orders, PortalOrderDTO{
			ID: uuid.New(), Status: "CONFIRMED",
			CreatedAt: base.Add(time.Duration(-i) * time.Hour),
		})
	}

	dash, err := rig.svc.GetDashboard(context.Background(), me)
	if err != nil {
		t.Fatalf("GetDashboard: %v", err)
	}

	if len(rig.repo.arReads) != 1 || rig.repo.arReads[0] != me {
		t.Errorf("AR summary read for %v, want the session customer %s", rig.repo.arReads, me)
	}
	if len(rig.repo.listedOrders) != 1 || rig.repo.listedOrders[0] != me {
		t.Errorf("orders listed for %v, want the session customer %s", rig.repo.listedOrders, me)
	}

	if len(dash.RecentOrders) != 5 {
		t.Fatalf("recent orders = %d, want 5", len(dash.RecentOrders))
	}
	for i := range dash.RecentOrders {
		if dash.RecentOrders[i].ID != rig.repo.orders[i].ID {
			t.Errorf("recent order %d = %s, want the repository's %s (the newest five)",
				i, dash.RecentOrders[i].ID, rig.repo.orders[i].ID)
		}
	}
	if dash.BalanceDue != 12500.50 || dash.CreditLimit != 50000 || dash.PastDue != 250.25 {
		t.Errorf("AR = %+v, want the repository's summary", dash)
	}
}

// CORRECTNESS: fewer than five orders are all shown, not padded or truncated.
func TestGetDashboard_ShowsEverythingWhenThereAreFewerThanFive(t *testing.T) {
	rig := newPortalRig(t)
	for i := 0; i < 3; i++ {
		rig.repo.orders = append(rig.repo.orders, PortalOrderDTO{ID: uuid.New()})
	}

	dash, err := rig.svc.GetDashboard(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("GetDashboard: %v", err)
	}
	if len(dash.RecentOrders) != 3 {
		t.Errorf("recent orders = %d, want all 3", len(dash.RecentOrders))
	}
}

// CORRECTNESS: an AR failure fails the dashboard rather than rendering a $0
// balance. A contractor shown a zero balance they do not have will not pay it.
func TestGetDashboard_ARFailureIsFatal(t *testing.T) {
	rig := newPortalRig(t)
	rig.repo.arErr = errors.New("failed to load AR summary")

	dash, err := rig.svc.GetDashboard(context.Background(), uuid.New())
	if err == nil {
		t.Fatalf("an AR failure rendered as a dashboard: %+v", dash)
	}
	if dash != nil {
		t.Errorf("returned %+v alongside the error, want nil", dash)
	}
	if len(rig.repo.listedOrders) != 0 {
		t.Errorf("the order list was queried after the AR read failed: %v", rig.repo.listedOrders)
	}
}

// --- invites, roles and status -------------------------------------------

// CORRECTNESS: an invite is written against the session's customer with a
// 7-day expiry and a freshly minted token. The token is the bearer credential
// in the invite link, so it must be a new random value, never the email or the
// invite id.
func TestInviteUser_WritesTheSessionCustomerAndAFreshToken(t *testing.T) {
	rig := newPortalRig(t)
	me := uuid.New()

	before := time.Now()
	invite, err := rig.svc.InviteUser(context.Background(), me,
		InviteUserRequest{Email: "buyer@acme.example", Role: "Buyer"})
	if err != nil {
		t.Fatalf("InviteUser: %v", err)
	}

	if len(rig.repo.invites) != 1 {
		t.Fatalf("CreatePortalInvite ran %d times, want 1", len(rig.repo.invites))
	}
	stored := rig.repo.invites[0]

	if stored.CustomerID != me {
		t.Errorf("invite customer = %s, want the session customer %s", stored.CustomerID, me)
	}
	if stored.Email != "buyer@acme.example" || stored.Role != "Buyer" {
		t.Errorf("invite = %+v, want the submitted email and role", stored)
	}
	if stored.Token == "" || stored.Token == stored.Email || stored.Token == stored.ID.String() {
		t.Errorf("token = %q, want a fresh random value distinct from the email and the invite id", stored.Token)
	}
	if _, parseErr := uuid.Parse(stored.Token); parseErr != nil {
		t.Errorf("token %q is not a UUID: %v", stored.Token, parseErr)
	}

	wantExpiry := before.Add(7 * 24 * time.Hour)
	if stored.ExpiresAt.Before(wantExpiry.Add(-time.Minute)) || stored.ExpiresAt.After(wantExpiry.Add(time.Minute)) {
		t.Errorf("expires_at = %s, want roughly %s (7 days out)", stored.ExpiresAt, wantExpiry)
	}
	if invite.Token != stored.Token || invite.ID != stored.ID {
		t.Errorf("the returned invite %+v is not the one persisted %+v", *invite, stored)
	}
}

// CORRECTNESS: every invite gets its own token. A token derived from anything
// stable would let one invite's link accept another's.
func TestInviteUser_TokensAreUniquePerInvite(t *testing.T) {
	rig := newPortalRig(t)
	me := uuid.New()

	seen := map[string]bool{}
	for i := 0; i < 5; i++ {
		inv, err := rig.svc.InviteUser(context.Background(), me,
			InviteUserRequest{Email: "buyer@acme.example", Role: "Buyer"})
		if err != nil {
			t.Fatalf("InviteUser: %v", err)
		}
		if seen[inv.Token] {
			t.Fatalf("token %q was reused across invites", inv.Token)
		}
		seen[inv.Token] = true
	}
}

// CORRECTNESS: the role whitelist is enforced before persistence, matched
// exactly. "admin" is not "Admin": the role string is compared literally by the
// portal middleware.
func TestInviteUser_InvalidRoleNeverReachesPersistence(t *testing.T) {
	for _, role := range []string{"", "admin", "ADMIN", "Owner", "buyer", "View Only", "Admin ", "Superuser"} {
		rig := newPortalRig(t)
		_, err := rig.svc.InviteUser(context.Background(), uuid.New(),
			InviteUserRequest{Email: "x@acme.example", Role: role})
		if err == nil {
			t.Errorf("role %q was accepted", role)
		}
		if len(rig.repo.invites) != 0 {
			t.Errorf("role %q reached persistence: %+v", role, rig.repo.invites)
		}
	}
}

// CORRECTNESS: an invite that fails to persist is not reported as sent.
func TestInviteUser_PersistenceFailureIsReturned(t *testing.T) {
	rig := newPortalRig(t)
	rig.repo.inviteErr = errors.New("failed to create invite")

	inv, err := rig.svc.InviteUser(context.Background(), uuid.New(),
		InviteUserRequest{Email: "x@acme.example", Role: "Buyer"})
	if err == nil {
		t.Fatalf("a failed invite was reported as sent: %+v", inv)
	}
	if inv != nil {
		t.Errorf("returned %+v alongside the error, want nil", inv)
	}
}

// CORRECTNESS (security): a role change is scoped to the session's customer, so
// an Admin at one contractor cannot promote a user at another. The target user
// id is caller-supplied; the customer id is not.
func TestUpdateUserRole_ScopesTheWriteToTheSessionCustomer(t *testing.T) {
	rig := newPortalRig(t)
	me, target := uuid.New(), uuid.New()

	if err := rig.svc.UpdateUserRole(context.Background(), me, target, "Admin"); err != nil {
		t.Fatalf("UpdateUserRole: %v", err)
	}
	if len(rig.repo.roleWrites) != 1 {
		t.Fatalf("UpdateUserRole ran %d times, want 1", len(rig.repo.roleWrites))
	}
	got := rig.repo.roleWrites[0]
	if got.userID != target {
		t.Errorf("target = %s, want %s", got.userID, target)
	}
	if got.customer != me {
		t.Errorf("scope = %s, want the session customer %s", got.customer, me)
	}
	if got.value != "Admin" {
		t.Errorf("role = %q, want Admin", got.value)
	}
}

// CORRECTNESS: the same whitelist applies to a role change as to an invite; an
// unknown role must not reach the write.
func TestUpdateUserRole_InvalidRoleNeverReachesTheWrite(t *testing.T) {
	for _, role := range []string{"", "admin", "Owner", "Buyer ", "View Only"} {
		rig := newPortalRig(t)
		if err := rig.svc.UpdateUserRole(context.Background(), uuid.New(), uuid.New(), role); err == nil {
			t.Errorf("role %q was accepted", role)
		}
		if len(rig.repo.roleWrites) != 0 {
			t.Errorf("role %q reached the write: %+v", role, rig.repo.roleWrites)
		}
	}
}

// CORRECTNESS: all three valid roles are accepted and written verbatim.
func TestUpdateUserRole_AcceptsExactlyThreeRoles(t *testing.T) {
	for _, role := range []string{"Admin", "Buyer", "View-Only"} {
		rig := newPortalRig(t)
		if err := rig.svc.UpdateUserRole(context.Background(), uuid.New(), uuid.New(), role); err != nil {
			t.Fatalf("role %q was rejected: %v", role, err)
		}
		if len(rig.repo.roleWrites) != 1 || rig.repo.roleWrites[0].value != role {
			t.Errorf("role %q was not written verbatim: %+v", role, rig.repo.roleWrites)
		}
	}
}

// CORRECTNESS (security): deactivating a user is scoped the same way, and the
// status vocabulary is closed. Deactivation is the portal's only revocation
// mechanism, so an unrecognised status silently leaving a user Active would be
// a failure to revoke.
func TestUpdateUserStatus_ScopeAndWhitelist(t *testing.T) {
	rig := newPortalRig(t)
	me, target := uuid.New(), uuid.New()

	if err := rig.svc.UpdateUserStatus(context.Background(), me, target, "Inactive"); err != nil {
		t.Fatalf("UpdateUserStatus: %v", err)
	}
	if len(rig.repo.statusWrites) != 1 {
		t.Fatalf("UpdateUserStatus ran %d times, want 1", len(rig.repo.statusWrites))
	}
	got := rig.repo.statusWrites[0]
	if got.userID != target || got.customer != me || got.value != "Inactive" {
		t.Errorf("write = %+v, want {%s %s Inactive}", got, target, me)
	}

	for _, status := range []string{"", "active", "INACTIVE", "Disabled", "Suspended", "Active "} {
		r := newPortalRig(t)
		if err := r.svc.UpdateUserStatus(context.Background(), uuid.New(), uuid.New(), status); err == nil {
			t.Errorf("status %q was accepted", status)
		}
		if len(r.repo.statusWrites) != 0 {
			t.Errorf("status %q reached the write: %+v", status, r.repo.statusWrites)
		}
	}
}

// --- order change feed ---------------------------------------------------

// CORRECTNESS: the cursor returned to the caller is the NEWEST updated_at in
// the page, not the first row's — the page is ordered by created_at, so the
// first row is not necessarily the most recently changed. A first-row cursor
// would go backwards and re-deliver rows forever.
func TestListOrdersFiltered_CursorIsTheNewestUpdatedAtInThePage(t *testing.T) {
	rig := newPortalRig(t)
	me := uuid.New()

	oldest := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	newest := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	middle := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)

	// created_at DESC order, but updated_at deliberately out of order.
	rig.repo.orders = []PortalOrderDTO{
		{ID: uuid.New(), CreatedAt: newest, UpdatedAt: middle},
		{ID: uuid.New(), CreatedAt: middle, UpdatedAt: newest},
		{ID: uuid.New(), CreatedAt: oldest, UpdatedAt: oldest},
	}

	res, err := rig.svc.ListOrdersFiltered(context.Background(), me, OrderListFilter{})
	if err != nil {
		t.Fatalf("ListOrdersFiltered: %v", err)
	}
	if res.LatestUpdatedAt == nil {
		t.Fatal("no cursor was returned")
	}
	if !res.LatestUpdatedAt.Equal(newest) {
		t.Errorf("cursor = %s, want the newest updated_at in the page %s", res.LatestUpdatedAt, newest)
	}
	if len(rig.repo.filtered) != 1 || rig.repo.filtered[0].customer != me {
		t.Errorf("queried %+v, want the session customer %s", rig.repo.filtered, me)
	}
}

// CORRECTNESS: an empty page carries no cursor, so a consumer does not advance
// past rows it has not seen.
func TestListOrdersFiltered_EmptyPageHasNoCursor(t *testing.T) {
	rig := newPortalRig(t)

	res, err := rig.svc.ListOrdersFiltered(context.Background(), uuid.New(), OrderListFilter{})
	if err != nil {
		t.Fatalf("ListOrdersFiltered: %v", err)
	}
	if res.LatestUpdatedAt != nil {
		t.Errorf("cursor = %s for an empty page, want nil", res.LatestUpdatedAt)
	}
	if res.ETag == "" {
		t.Error("an empty page still needs an ETag so a 304 can be served")
	}
}

// CORRECTNESS (security): a project filter is caller-supplied and is verified
// against the session's customer before it is used. Without the check, filtering
// by another contractor's project id would leak whether it exists.
func TestListOrdersFiltered_ForeignProjectFilterIsRefusedBeforeTheQuery(t *testing.T) {
	rig := newPortalRig(t)
	me, theirProject := uuid.New(), uuid.New()
	rig.repo.projectOwned = false

	_, err := rig.svc.ListOrdersFiltered(context.Background(), me, OrderListFilter{ProjectID: &theirProject})
	if !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("error = %v, want ErrProjectNotFound", err)
	}
	if len(rig.repo.filtered) != 0 {
		t.Errorf("the order query ran anyway: %+v", rig.repo.filtered)
	}
}

// CORRECTNESS: the filter is passed through to the query unchanged — dropping
// `since` would turn a delta poll into a full re-fetch, and dropping the
// project would return the customer's whole order history under a project
// heading.
func TestListOrdersFiltered_PassesTheFilterThrough(t *testing.T) {
	rig := newPortalRig(t)
	me, myProject := uuid.New(), uuid.New()
	since := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)

	if _, err := rig.svc.ListOrdersFiltered(context.Background(), me,
		OrderListFilter{ProjectID: &myProject, Since: &since}); err != nil {
		t.Fatalf("ListOrdersFiltered: %v", err)
	}
	if len(rig.repo.filtered) != 1 {
		t.Fatalf("the query ran %d times, want 1", len(rig.repo.filtered))
	}
	got := rig.repo.filtered[0].filter
	if got.ProjectID == nil || *got.ProjectID != myProject {
		t.Errorf("project filter = %v, want %s", got.ProjectID, myProject)
	}
	if got.Since == nil || !got.Since.Equal(since) {
		t.Errorf("since filter = %v, want %s", got.Since, since)
	}
}

// CORRECTNESS: the ETag is a function of the customer, the filter and the data.
// Two customers with identically shaped pages must not share a validator, or a
// shared cache in front of the API could serve one contractor the other's 304.
func TestListOrdersFiltered_ETagSeparatesCustomersAndFilters(t *testing.T) {
	page := []PortalOrderDTO{{ID: uuid.New(), UpdatedAt: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)}}

	etagFor := func(customerID uuid.UUID, filter OrderListFilter) string {
		rig := newPortalRig(t)
		rig.repo.orders = page
		res, err := rig.svc.ListOrdersFiltered(context.Background(), customerID, filter)
		if err != nil {
			t.Fatalf("ListOrdersFiltered: %v", err)
		}
		return res.ETag
	}

	a, b := uuid.New(), uuid.New()
	project := uuid.New()

	if etagFor(a, OrderListFilter{}) == etagFor(b, OrderListFilter{}) {
		t.Error("two customers with identical pages share an ETag")
	}
	if etagFor(a, OrderListFilter{}) == etagFor(a, OrderListFilter{ProjectID: &project}) {
		t.Error("the project filter does not change the ETag")
	}
	if etagFor(a, OrderListFilter{}) != etagFor(a, OrderListFilter{}) {
		t.Error("the ETag is not stable for identical inputs")
	}
}

// --- SetOrderProject -----------------------------------------------------

// CORRECTNESS (security): attaching an order to a project verifies the project
// first and only then issues the write, both scoped to the session customer.
func TestSetOrderProject_VerifiesOwnershipBeforeWriting(t *testing.T) {
	rig := newPortalRig(t)
	me, orderID, projectID := uuid.New(), uuid.New(), uuid.New()
	rig.repo.order = &PortalOrderDTO{ID: orderID, ProjectID: &projectID}

	if _, err := rig.svc.SetOrderProject(context.Background(), orderID, me, &projectID); err != nil {
		t.Fatalf("SetOrderProject: %v", err)
	}
	if len(rig.repo.projectChecks) != 1 || rig.repo.projectChecks[0] != (scopeCall{projectID, me}) {
		t.Errorf("ownership checked as %+v, want {%s %s}", rig.repo.projectChecks, projectID, me)
	}
	if len(rig.repo.setProject) != 1 || rig.repo.setProject[0].customer != me {
		t.Errorf("write = %+v, want it scoped to %s", rig.repo.setProject, me)
	}
	if len(rig.repo.orderReads) != 1 || rig.repo.orderReads[0] != (scopeCall{orderID, me}) {
		t.Errorf("the order was re-read as %+v, want {%s %s}", rig.repo.orderReads, orderID, me)
	}
}

// CORRECTNESS (security): a foreign project is refused and nothing is written.
func TestSetOrderProject_ForeignProjectIsRefusedWithoutAWrite(t *testing.T) {
	rig := newPortalRig(t)
	me, orderID, theirProject := uuid.New(), uuid.New(), uuid.New()
	rig.repo.projectOwned = false

	_, err := rig.svc.SetOrderProject(context.Background(), orderID, me, &theirProject)
	if !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("error = %v, want ErrProjectNotFound", err)
	}
	if len(rig.repo.setProject) != 0 {
		t.Errorf("the write ran anyway: %+v", rig.repo.setProject)
	}
}

// CORRECTNESS: detaching (a nil project) skips the ownership check — there is
// no project to own — but still writes scoped to the session customer.
func TestSetOrderProject_DetachSkipsTheOwnershipCheck(t *testing.T) {
	rig := newPortalRig(t)
	me, orderID := uuid.New(), uuid.New()
	rig.repo.order = &PortalOrderDTO{ID: orderID}

	if _, err := rig.svc.SetOrderProject(context.Background(), orderID, me, nil); err != nil {
		t.Fatalf("SetOrderProject(nil): %v", err)
	}
	if len(rig.repo.projectChecks) != 0 {
		t.Errorf("ownership was checked for a detach: %+v", rig.repo.projectChecks)
	}
	if len(rig.repo.setProject) != 1 {
		t.Fatalf("the detach did not write: %+v", rig.repo.setProject)
	}
	if rig.repo.setProject[0].projectID != nil {
		t.Errorf("wrote project %v, want nil for a detach", rig.repo.setProject[0].projectID)
	}
	if rig.repo.setProject[0].customer != me {
		t.Errorf("detach scope = %s, want %s", rig.repo.setProject[0].customer, me)
	}
}

// --- CancelOrder ---------------------------------------------------------

// CORRECTNESS (security): the ownership gate is the scoped status read, and it
// runs first. Another contractor's order must be "not found", not "forbidden",
// so the endpoint is not an oracle for which order ids exist — and the ERP
// cancel must never be reached.
func TestCancelOrder_ForeignOrderIsNotFoundAndNeverReachesTheERP(t *testing.T) {
	rig := newPortalRig(t)
	me, orderID := uuid.New(), uuid.New()
	rig.repo.statusErr = ErrOrderNotFound

	_, err := rig.svc.CancelOrder(context.Background(), orderID, me, "changed my mind")
	if !errors.Is(err, ErrOrderNotFound) {
		t.Fatalf("error = %v, want ErrOrderNotFound", err)
	}
	if len(rig.repo.statusReads) != 1 || rig.repo.statusReads[0] != (scopeCall{orderID, me}) {
		t.Errorf("status read as %+v, want {%s %s}", rig.repo.statusReads, orderID, me)
	}
	if len(rig.orders.statuses) != 0 {
		t.Errorf("the ERP cancel ran for a foreign order: %v", rig.orders.statuses)
	}
}

// CORRECTNESS: goods already in motion are refused with ErrCancelRefused and an
// actionable reason, and the ERP cancel is not attempted. order.CancelOrder
// would happily cancel a CONFIRMED order; it is the portal that must not let a
// contractor cancel a load that is on a truck.
func TestCancelOrder_GoodsInMotionAreRefusedBeforeTheERP(t *testing.T) {
	rig := newPortalRig(t)
	me, orderID := uuid.New(), uuid.New()
	rig.repo.orderStatus = "CONFIRMED"
	rig.repo.inMotion = true
	rig.repo.inMotionWhy = "this order is on a route that is IN_TRANSIT"

	_, err := rig.svc.CancelOrder(context.Background(), orderID, me, "")
	if !errors.Is(err, ErrCancelRefused) {
		t.Fatalf("error = %v, want ErrCancelRefused", err)
	}
	if !strings.Contains(err.Error(), "IN_TRANSIT") {
		t.Errorf("error %q does not say why, so the contractor cannot act on it", err)
	}
	if !strings.Contains(err.Error(), "call the dealer") {
		t.Errorf("error %q does not say what to do next", err)
	}
	if len(rig.orders.statuses) != 0 {
		t.Errorf("the ERP cancel ran for an in-motion order: %v", rig.orders.statuses)
	}
}

// CORRECTNESS: a cancellable order reaches the ERP, comes back CANCELLED, and
// the response reports the status it moved FROM so the UI can explain what
// happened.
func TestCancelOrder_CancellableOrderReachesTheERP(t *testing.T) {
	rig := newPortalRig(t)
	me, orderID := uuid.New(), uuid.New()
	rig.repo.orderStatus = "DRAFT"
	rig.orders.orders[orderID] = &order.Order{ID: orderID, CustomerID: me, Status: order.StatusDraft}

	resp, err := rig.svc.CancelOrder(context.Background(), orderID, me, "changed my mind")
	if err != nil {
		t.Fatalf("CancelOrder: %v", err)
	}
	if resp.OrderID != orderID {
		t.Errorf("order id = %s, want %s", resp.OrderID, orderID)
	}
	if resp.PreviousStatus != "DRAFT" {
		t.Errorf("previous_status = %q, want the DRAFT it was read as", resp.PreviousStatus)
	}
	if resp.Status != string(order.StatusCancelled) {
		t.Errorf("status = %q, want %q", resp.Status, order.StatusCancelled)
	}
	if len(rig.orders.statuses) != 1 || rig.orders.statuses[0] != order.StatusCancelled {
		t.Errorf("ERP status writes = %v, want a single CANCELLED", rig.orders.statuses)
	}
}

// CORRECTNESS: the ERP state machine still owns the rest. A FULFILLED order is
// refused by order.CancelOrder even though the portal's two gates passed.
func TestCancelOrder_ERPStateMachineStillApplies(t *testing.T) {
	rig := newPortalRig(t)
	me, orderID := uuid.New(), uuid.New()
	rig.repo.orderStatus = "FULFILLED"
	rig.orders.orders[orderID] = &order.Order{ID: orderID, CustomerID: me, Status: order.StatusFulfilled}

	if _, err := rig.svc.CancelOrder(context.Background(), orderID, me, ""); err == nil {
		t.Fatal("a FULFILLED order was cancelled from the portal")
	}
	if len(rig.orders.statuses) != 0 {
		t.Errorf("the status was written anyway: %v", rig.orders.statuses)
	}
}

// --- per-customer scoping, as one table ----------------------------------

// CORRECTNESS (security): the portal's tenant boundary is "the customer id
// comes from the session claims and is passed to every scoped query
// unchanged". tenancy_pg_test.go proves the SQL honours it; this proves the
// service supplies it. Every read below is called with a session customer and a
// caller-supplied subject id, and must hand BOTH to the repository.
func TestEveryScopedRead_ForwardsTheSessionCustomer(t *testing.T) {
	me := uuid.New()
	subject := uuid.New()

	cases := []struct {
		name string
		call func(*portalRig) error
		got  func(*fakePortalRepo) []scopeCall
		want scopeCall
	}{
		{
			name: "GetOrder",
			call: func(r *portalRig) error {
				_, err := r.svc.GetOrder(context.Background(), subject, me)
				return err
			},
			got:  func(f *fakePortalRepo) []scopeCall { return f.orderReads },
			want: scopeCall{subject, me},
		},
		{
			name: "GetInvoice",
			call: func(r *portalRig) error {
				_, err := r.svc.GetInvoice(context.Background(), subject, me)
				return err
			},
			got:  func(f *fakePortalRepo) []scopeCall { return f.invoiceReads },
			want: scopeCall{subject, me},
		},
		{
			name: "GetDelivery",
			call: func(r *portalRig) error {
				_, err := r.svc.GetDelivery(context.Background(), subject, me)
				return err
			},
			got:  func(f *fakePortalRepo) []scopeCall { return f.deliveryReads },
			want: scopeCall{subject, me},
		},
		{
			name: "CreateReorder",
			call: func(r *portalRig) error {
				_, err := r.svc.CreateReorder(context.Background(), me, ReorderRequest{OrderID: subject})
				return err
			},
			got:  func(f *fakePortalRepo) []scopeCall { return f.reorders },
			want: scopeCall{subject, me},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rig := newPortalRig(t)
			rig.repo.order = &PortalOrderDTO{ID: subject}
			rig.repo.invoices = []PortalInvoiceDTO{{ID: subject}}
			rig.repo.deliveries = []PortalDeliveryDTO{{ID: subject}}

			_ = tc.call(rig) // the answer does not matter, the scope does

			calls := tc.got(rig.repo)
			if len(calls) != 1 {
				t.Fatalf("%s issued %d scoped reads, want 1: %+v", tc.name, len(calls), calls)
			}
			if calls[0] != tc.want {
				t.Errorf("%s queried %+v, want {subject %s, customer %s}", tc.name, calls[0], tc.want.subject, tc.want.customer)
			}
		})
	}
}

// CORRECTNESS (security): the list reads carry the session customer too.
func TestEveryScopedList_ForwardsTheSessionCustomer(t *testing.T) {
	me := uuid.New()

	rig := newPortalRig(t)
	ctx := context.Background()

	if _, err := rig.svc.ListOrders(ctx, me); err != nil {
		t.Fatalf("ListOrders: %v", err)
	}
	if _, err := rig.svc.ListInvoices(ctx, me); err != nil {
		t.Fatalf("ListInvoices: %v", err)
	}
	if _, err := rig.svc.ListDeliveries(ctx, me); err != nil {
		t.Fatalf("ListDeliveries: %v", err)
	}
	if _, err := rig.svc.ListCustomerUsers(ctx, me); err != nil {
		t.Fatalf("ListCustomerUsers: %v", err)
	}
	if _, err := rig.svc.ListPortalInvites(ctx, me); err != nil {
		t.Fatalf("ListPortalInvites: %v", err)
	}

	for name, got := range map[string][]uuid.UUID{
		"ListOrders":        rig.repo.listedOrders,
		"ListCustomerUsers": rig.repo.userLists,
		"ListPortalInvites": rig.repo.inviteLists,
	} {
		if len(got) != 1 || got[0] != me {
			t.Errorf("%s queried %v, want the session customer %s", name, got, me)
		}
	}
	if len(rig.repo.invoiceReads) != 1 || rig.repo.invoiceReads[0].customer != me {
		t.Errorf("ListInvoices queried %+v, want the session customer %s", rig.repo.invoiceReads, me)
	}
	if len(rig.repo.deliveryReads) != 1 || rig.repo.deliveryReads[0].customer != me {
		t.Errorf("ListDeliveries queried %+v, want the session customer %s", rig.repo.deliveryReads, me)
	}
}

// CORRECTNESS: the seam is a consumer-defined interface and the Postgres
// implementation satisfies it. If someone re-couples NewService to the concrete
// type, this stops compiling and every test above goes with it.
func TestPortalSeam_ServiceTakesAnInterface(t *testing.T) {
	var _ Repository = (*PostgresRepository)(nil)
	if NewService(newFakePortalRepo(), testSecret, testLogger(), nil, nil, nil, nil, nil) == nil {
		t.Fatal("NewService returned nil for a fake repository")
	}
}
