// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package portal

// The five contractor roles and the capabilities the server resolves them to.
//
// A route NEVER checks a role string. It names the capability it needs and
// calls requireCapability (handler.go), which resolves the caller's role
// through this table. A client is told the same answer by GET /me, so the
// buttons it renders can never disagree with the gate that enforces them:
// the one authority is this map, on the server.

// The contractor portal roles (the DB CHECK set of migration 130).
const (
	RoleOwner          = "owner"
	RoleEstimator      = "estimator"
	RoleProjectManager = "project_manager"
	RoleSiteSuper      = "site_super"
	RoleAPClerk        = "ap_clerk"
)

// PortalRoles lists the role vocabulary in its stable order.
func PortalRoles() []string {
	return []string{RoleOwner, RoleEstimator, RoleProjectManager, RoleSiteSuper, RoleAPClerk}
}

// ValidPortalRole reports whether s is one of the five roles.
func ValidPortalRole(s string) bool {
	switch s {
	case RoleOwner, RoleEstimator, RoleProjectManager, RoleSiteSuper, RoleAPClerk:
		return true
	}
	return false
}

// Capability names. These are the wire spellings GET /me returns and every
// route guard names; adding one is additive, changing one is a wire break.
const (
	CapManageTeam              = "manage_team"
	CapSeeMoney                = "see_money"
	CapPay                     = "pay"
	CapOrder                   = "order"
	CapRequestQuotes           = "request_quotes"
	CapApproveQuotes           = "approve_quotes"
	CapSchedule                = "schedule"
	CapEditDeliveryInstruction = "edit_delivery_instructions"
	CapQuoteCustomers          = "quote_customers"
)

// Capabilities is the server resolved answer to "what may this user do".
// Every field is a boolean the client can branch on directly; the JSON keys
// are the capability names above.
type Capabilities struct {
	ManageTeam              bool `json:"manage_team"`
	SeeMoney                bool `json:"see_money"`
	Pay                     bool `json:"pay"`
	Order                   bool `json:"order"`
	RequestQuotes           bool `json:"request_quotes"`
	ApproveQuotes           bool `json:"approve_quotes"`
	Schedule                bool `json:"schedule"`
	EditDeliveryInstruction bool `json:"edit_delivery_instructions"`
	QuoteCustomers          bool `json:"quote_customers"`
}

// CapabilitiesFor resolves a role to its capability set. An unknown role (an
// empty dev claim, a stale token from before the migration) grants nothing
// beyond signing in: reads stay, every capability gated write refuses.
func CapabilitiesFor(role string) Capabilities {
	switch role {
	case RoleOwner:
		// The company's owner: team, money, orders, quotes, schedule.
		return Capabilities{
			ManageTeam:              true,
			SeeMoney:                true,
			Pay:                     true,
			Order:                   true,
			RequestQuotes:           true,
			ApproveQuotes:           true,
			Schedule:                true,
			EditDeliveryInstruction: true,
			QuoteCustomers:          true,
		}
	case RoleEstimator:
		// Prices work and asks the dealer for quotes: no money, no team.
		return Capabilities{
			Order:                   true,
			RequestQuotes:           true,
			ApproveQuotes:           true,
			Schedule:                true,
			EditDeliveryInstruction: true,
			// Builds customer quotes with markups (which show the dealer
			// costs they are built on) without seeing account money.
			QuoteCustomers: true,
		}
	case RoleProjectManager:
		// Runs jobs end to end, so sees the money a job commits against.
		return Capabilities{
			SeeMoney:                true,
			Order:                   true,
			RequestQuotes:           true,
			ApproveQuotes:           true,
			Schedule:                true,
			EditDeliveryInstruction: true,
			QuoteCustomers:          true,
		}
	case RoleSiteSuper:
		// The site: orders materials and books the yard's time. No financials.
		return Capabilities{
			Order:                   true,
			Schedule:                true,
			EditDeliveryInstruction: true,
		}
	case RoleAPClerk:
		// The back office: sees the money and pays it. Cannot commit an order.
		return Capabilities{
			SeeMoney: true,
			Pay:      true,
		}
	default:
		return Capabilities{}
	}
}

// Has reports whether the set carries one capability.
func (c Capabilities) Has(name string) bool {
	switch name {
	case CapManageTeam:
		return c.ManageTeam
	case CapSeeMoney:
		return c.SeeMoney
	case CapPay:
		return c.Pay
	case CapOrder:
		return c.Order
	case CapRequestQuotes:
		return c.RequestQuotes
	case CapApproveQuotes:
		return c.ApproveQuotes
	case CapSchedule:
		return c.Schedule
	case CapEditDeliveryInstruction:
		return c.EditDeliveryInstruction
	case CapQuoteCustomers:
		return c.QuoteCustomers
	}
	return false
}
