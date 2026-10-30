// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

//! Exact fixed-scale arithmetic for the offline counter (ADR 0005, ADR 0006,
//! and ADR 0011 section 7): scale 4 quantities and prices, scale 6 tax rates,
//! canonical unit conversion pairs, exact stock conversion, stock unit
//! restatement, tally linear and board feet, and tender settlement.
//!
//! No floating-point type appears anywhere in this module.

use crate::offline::model::CashRoundingMode;
use serde::{Deserialize, Serialize};

pub const QUANTITY_SCALE: u32 = 4;
pub const QUANTITY_MAX: i64 = 999_999_999_999;
pub const ONE_QTY: i64 = 10_000;
pub const TAX_RATE_SCALE: u32 = 6;
const EXTEND_SCALE_DIVISOR: i128 = 1_000_000;
const TAX_SCALE_DIVISOR: i128 = 1_000_000;
const BOARD_FEET_DIVISOR: i128 = 1_200_000_000;

/// Parses a plain canonical decimal string into a scale 4 integer, matching
/// `httpx.ParseQuantity`.
pub fn parse_quantity(s: &str) -> Result<i64, String> {
    let val = parse_fixed_scale(s, QUANTITY_SCALE as usize)?;
    let body = s.strip_prefix('-').unwrap_or(s);
    let int_part = body.split('.').next().unwrap_or(body);
    if int_part.len() > 1 && int_part.starts_with('0') {
        return Err("quantity is not in canonical form".to_string());
    }
    if val == 0 && s.starts_with('-') {
        return Err("negative zero is not canonical".to_string());
    }
    if !(-QUANTITY_MAX..=QUANTITY_MAX).contains(&val) {
        return Err("quantity is beyond the NUMERIC(12,4) bound".to_string());
    }
    Ok(val)
}

/// Formats a scale 4 integer as a fixed four-digit decimal ("12.5000").
pub fn format_decimal_quantity(q: i64) -> String {
    let neg = q < 0;
    let abs = q.unsigned_abs();
    let major = abs / 10_000;
    let frac = abs % 10_000;
    if neg {
        format!("-{major}.{frac:04}")
    } else {
        format!("{major}.{frac:04}")
    }
}

/// Formats a scale 4 integer as the shortest canonical decimal string ("12.5",
/// "1000"), matching `httpx.Quantity.WireString`.
pub fn format_wire_quantity(q: i64) -> String {
    let s = format_decimal_quantity(q);
    if !s.contains('.') {
        return s;
    }
    let trimmed = s.trim_end_matches('0');
    trimmed.strip_suffix('.').unwrap_or(trimmed).to_string()
}

/// Parses a tax rate decimal string ("0.088750") into its scale 6 integer,
/// matching `salesdoc.ParseTaxRate`.
pub fn parse_tax_rate(s: &str) -> Result<i64, String> {
    let trimmed = s.trim();
    if trimmed.is_empty() {
        return Err("tax rate is empty".to_string());
    }
    parse_fixed_scale(trimmed, TAX_RATE_SCALE as usize)
}

fn parse_fixed_scale(s: &str, scale: usize) -> Result<i64, String> {
    let neg = s.starts_with('-');
    let body = s.strip_prefix('-').unwrap_or(s);
    let (int_part, frac_part) = match body.split_once('.') {
        Some((i, f)) => {
            if f.is_empty() {
                return Err("trailing decimal point is not allowed".to_string());
            }
            (i, f)
        }
        None => (body, ""),
    };
    if int_part.is_empty() {
        return Err("not a plain decimal".to_string());
    }
    let mut value: i64 = 0;
    for b in int_part.bytes() {
        if !b.is_ascii_digit() {
            return Err("not a plain decimal".to_string());
        }
        value = value
            .checked_mul(10)
            .and_then(|v| v.checked_add(i64::from(b - b'0')))
            .ok_or_else(|| "decimal overflow".to_string())?;
    }
    let used = frac_part.len().min(scale);
    for (idx, b) in frac_part.bytes().enumerate() {
        if !b.is_ascii_digit() {
            return Err("not a plain decimal".to_string());
        }
        if idx < used {
            value = value
                .checked_mul(10)
                .and_then(|v| v.checked_add(i64::from(b - b'0')))
                .ok_or_else(|| "decimal overflow".to_string())?;
        } else if b != b'0' {
            return Err("precision beyond the fixed scale".to_string());
        }
    }
    for _ in 0..(scale - used) {
        value = value
            .checked_mul(10)
            .ok_or_else(|| "decimal overflow".to_string())?;
    }
    if neg {
        value = -value;
    }
    Ok(value)
}

/// Extends a line (ADR 0001 section 7a and ADR 0005 section 2.4):
/// `qty * price_uom_qty * unit_price / (uom_qty * 1_000_000)`, rounded once to
/// cents, half away from zero.
pub fn extend(qty: i64, uom_qty: i64, price_uom_qty: i64, unit_price: i64) -> Result<i64, String> {
    if uom_qty <= 0 || price_uom_qty <= 0 {
        return Err("conversion pair sides must be positive".to_string());
    }
    let n = i128::from(qty) * i128::from(price_uom_qty) * i128::from(unit_price);
    let div = i128::from(uom_qty) * EXTEND_SCALE_DIVISOR;
    let neg = n < 0;
    let mag = n.abs();
    let rounded = (mag + (div >> 1)) / div;
    let cents = i64::try_from(rounded).map_err(|_| "line extension overflow".to_string())?;
    Ok(if neg { -cents } else { cents })
}

/// Computes document tax from taxable cents and a scale 6 rate (ADR 0005
/// section 3): `round_half_away(taxable * rate_scaled / 1_000_000)`.
pub fn tax_at(taxable_cents: i64, rate_scaled: i64) -> i64 {
    let n = i128::from(taxable_cents) * i128::from(rate_scaled);
    let neg = n < 0;
    let mag = n.abs();
    let rounded = (mag + (TAX_SCALE_DIVISOR >> 1)) / TAX_SCALE_DIVISOR;
    let cents = rounded as i64;
    if neg {
        -cents
    } else {
        cents
    }
}

/// A conversion pair of positive scale 4 quantities (ADR 0006 R1).
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct Pair {
    pub a: i64,
    pub b: i64,
}

impl Pair {
    pub const fn one() -> Self {
        Self {
            a: ONE_QTY,
            b: ONE_QTY,
        }
    }

    pub fn same_ratio(self, other: Self) -> bool {
        if self.a <= 0 || self.b <= 0 || other.a <= 0 || other.b <= 0 {
            return false;
        }
        i128::from(self.a) * i128::from(other.b) == i128::from(self.b) * i128::from(other.a)
    }
}

fn gcd_u128(mut a: u128, mut b: u128) -> u128 {
    while b != 0 {
        let t = b;
        b = a % b;
        a = t;
    }
    a
}

fn exact_at_scale4(num: u128, den: u128) -> Option<i64> {
    if den == 0 {
        return None;
    }
    let scaled = num.checked_mul(10_000)?;
    if scaled % den != 0 {
        return None;
    }
    i64::try_from(scaled / den).ok()
}

fn pair_from_scale(a: i64, b: i64) -> Result<Pair, String> {
    if a <= 0 || b <= 0 || a > QUANTITY_MAX || b > QUANTITY_MAX {
        return Err("the conversion does not fit the pair's bound".to_string());
    }
    Ok(Pair { a, b })
}

fn canonical_ratio(num: u128, den: u128) -> Result<Pair, String> {
    if num == 0 || den == 0 {
        return Err("a pair's sides are positive".to_string());
    }
    let g = gcd_u128(num, den);
    let n = num / g;
    let d = den / g;
    let r_exact = exact_at_scale4(n, d);
    let inv_exact = exact_at_scale4(d, n);
    match (r_exact, inv_exact) {
        (Some(r_scaled), Some(inv_scaled)) => {
            if n >= d {
                if let Ok(p) = pair_from_scale(r_scaled, ONE_QTY) {
                    return Ok(p);
                }
            } else if let Ok(p) = pair_from_scale(ONE_QTY, inv_scaled) {
                return Ok(p);
            }
        }
        (Some(r_scaled), None) => {
            if let Ok(p) = pair_from_scale(r_scaled, ONE_QTY) {
                return Ok(p);
            }
        }
        (None, Some(inv_scaled)) => {
            if let Ok(p) = pair_from_scale(ONE_QTY, inv_scaled) {
                return Ok(p);
            }
        }
        (None, None) => {}
    }
    for k in [10_000u128, 1_000, 100, 10, 1] {
        if let (Some(ps), Some(qs)) = (n.checked_mul(k), d.checked_mul(k)) {
            if let (Ok(pi), Ok(qi)) = (i64::try_from(ps), i64::try_from(qs)) {
                if let Ok(p) = pair_from_scale(pi, qi) {
                    return Ok(p);
                }
            }
        }
    }
    Err("the conversion does not fit the pair's bound".to_string())
}

/// Resolves the canonical conversion pair between a line's sale unit row `u`
/// and price unit row `p` in the product's unit set (ADR 0006 section 3.3).
pub fn resolve_line_pair(u: Pair, p: Pair) -> Result<Pair, String> {
    if u.a <= 0 || u.b <= 0 || p.a <= 0 || p.b <= 0 {
        return Err("a pair's sides are positive".to_string());
    }
    let num = (u.a as u128) * (p.b as u128);
    let den = (p.a as u128) * (u.b as u128);
    canonical_ratio(num, den)
}

/// Inexact stock conversion refusal (ADR 0006 R5), naming the nearest exact
/// quantities on either side.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct InexactStockError {
    pub from: String,
    pub to: String,
    pub sent: i64,
    pub below: i64,
    pub above: i64,
    pub exact_step: i64,
}

impl InexactStockError {
    pub fn message(&self) -> String {
        let below_ok = self.below != 0 && self.below >= -QUANTITY_MAX;
        let above_ok = self.above != 0 && self.above <= QUANTITY_MAX;
        match (below_ok, above_ok) {
            (true, true) => format!(
                "does not convert exactly into the stocking unit {}; the nearest quantities that do are {} and {}",
                self.to,
                format_wire_quantity(self.below),
                format_wire_quantity(self.above)
            ),
            (true, false) => format!(
                "does not convert exactly into the stocking unit {}; the nearest quantity that does is {}",
                self.to,
                format_wire_quantity(self.below)
            ),
            (false, true) => format!(
                "does not convert exactly into the stocking unit {}; the nearest quantity that does is {}",
                self.to,
                format_wire_quantity(self.above)
            ),
            (false, false) => format!(
                "does not convert exactly into the stocking unit {}; no quantity this line can hold converts exactly",
                self.to
            ),
        }
    }
}

fn floor_to_step(v: i64, step: i64) -> i64 {
    if step <= 0 {
        return v;
    }
    let mut q = v / step;
    if v % step != 0 && v < 0 {
        q -= 1;
    }
    q * step
}

fn smallest_exact_step(f: Pair, t: Pair) -> i64 {
    let num = (f.b as u128) * (t.a as u128);
    let den = (f.a as u128) * (t.b as u128);
    let g = gcd_u128(num, den);
    let step = den / g;
    match i64::try_from(step) {
        Ok(s) if s > 0 && s <= QUANTITY_MAX => s,
        _ => ONE_QTY,
    }
}

/// Converts `qty` in `sale_uom` into `stock_uom` through `unit_row` (`unit_qty`
/// of `sale_uom` = `stock_qty` of `stock_uom`), matching `units.ConvertStock`.
pub fn convert_stock(
    qty: i64,
    sale_uom: &str,
    stock_uom: &str,
    unit_qty: i64,
    stock_qty: i64,
) -> Result<i64, InexactStockError> {
    if sale_uom == stock_uom {
        return Ok(qty);
    }
    let f = Pair {
        a: unit_qty,
        b: stock_qty,
    };
    let t = Pair::one();
    let num = i128::from(qty) * i128::from(f.b) * i128::from(t.a);
    let den = i128::from(f.a) * i128::from(t.b);
    if den > 0 && num % den == 0 {
        if let Ok(out) = i64::try_from(num / den) {
            if (-QUANTITY_MAX..=QUANTITY_MAX).contains(&out) {
                return Ok(out);
            }
        }
    }
    let step = smallest_exact_step(f, t);
    let below = floor_to_step(qty, step);
    Err(InexactStockError {
        from: sale_uom.to_string(),
        to: stock_uom.to_string(),
        sent: qty,
        below,
        above: below + step,
        exact_step: step,
    })
}

/// One row of a product's unit set for stock restatement checks.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct UnitSetEntry {
    pub uom: String,
    pub unit_qty: i64,
    pub stock_qty: i64,
    pub sell: bool,
    pub price: bool,
}

/// Error returned when a line's recorded `stock_uom` cannot convert into the
/// product's current stocking unit (`stock_unit_changed`).
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct StockMoveError {
    pub field: String,
    pub code: &'static str,
    pub message: String,
}

/// Matches `salesdoc.ResolveStockMove` (ADR 0006 section 3.4): when a line's
/// recorded `stock_uom` differs from the product's current stocking unit,
/// converts `recorded_stock_qty` through the current unit set if the old unit
/// is still in the set (and the current stocking unit's row is 1 and 1);
/// otherwise refuses with `stock_unit_changed`.
pub fn resolve_stock_move(
    line_index: usize,
    recorded_stock_uom: &str,
    recorded_stock_qty: i64,
    current_stock_uom: &str,
    unit_set: &[UnitSetEntry],
) -> Result<(String, i64), StockMoveError> {
    if recorded_stock_uom.is_empty()
        || current_stock_uom.is_empty()
        || recorded_stock_uom == current_stock_uom
    {
        let uom = if current_stock_uom.is_empty() {
            recorded_stock_uom.to_string()
        } else {
            current_stock_uom.to_string()
        };
        return Ok((uom, recorded_stock_qty));
    }
    let cur_row = unit_set.iter().find(|r| r.uom == current_stock_uom);
    let old_row = unit_set.iter().find(|r| r.uom == recorded_stock_uom);
    if let (Some(cur), Some(old)) = (cur_row, old_row) {
        if cur.unit_qty == ONE_QTY && cur.stock_qty == ONE_QTY {
            if let Ok(converted) = convert_stock(
                recorded_stock_qty,
                recorded_stock_uom,
                current_stock_uom,
                old.unit_qty,
                old.stock_qty,
            ) {
                return Ok((current_stock_uom.to_string(), converted));
            }
        }
    }
    let field = format!("lines[{line_index}]");
    let message = format!(
        "{field} carries stock_uom {recorded_stock_uom}, which cannot convert into the product's current stocking unit {current_stock_uom}"
    );
    Err(StockMoveError {
        field,
        code: "stock_unit_changed",
        message,
    })
}

/// Input row for a random-length tally.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct TallyRowCalc {
    pub pieces: i64,
    pub length_ft: i64,
}

/// Completed tally with exact `linear_feet` and display-rounded `board_feet`
/// (ADR 0006 section 4.3).
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct CompletedTally {
    pub linear_feet: i64,
    pub board_feet: Option<i64>,
}

/// Completes a random-length tally, matching `units.LinearFeet`,
/// `units.BoardFeet`, and `units.DisplayBoardFeet`.
pub fn complete_tally(
    rows: &[TallyRowCalc],
    thickness_in: Option<i64>,
    width_in: Option<i64>,
) -> Result<CompletedTally, String> {
    if rows.is_empty() || rows.len() > 100 {
        return Err("tally must carry between 1 and 100 rows".to_string());
    }
    let mut total: i128 = 0;
    for (idx, r) in rows.iter().enumerate() {
        if r.pieces <= 0 {
            return Err(format!("tally row {idx}: pieces must be positive"));
        }
        if r.length_ft <= 0 {
            return Err(format!("tally row {idx}: length_ft must be positive"));
        }
        total += i128::from(r.pieces) * i128::from(r.length_ft);
    }
    if total.abs() > i128::from(QUANTITY_MAX) {
        return Err("tally linear feet exceed the quantity bound".to_string());
    }
    let linear_feet = total as i64;
    let board_feet = match (thickness_in, width_in) {
        (Some(t), Some(w)) if t > 0 && w > 0 => {
            let num = i128::from(linear_feet) * i128::from(t) * i128::from(w);
            let neg = num < 0;
            let mag = num.abs();
            let rounded = (mag + (BOARD_FEET_DIVISOR >> 1)) / BOARD_FEET_DIVISOR;
            let bf = rounded as i64;
            Some(if neg { -bf } else { bf })
        }
        _ => None,
    };
    Ok(CompletedTally {
        linear_feet,
        board_feet,
    })
}

/// One tender leg for offline settlement.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct OfflineTenderCalc {
    pub method: String,
    pub amount_cents: i64,
}

/// Outcome of settling an offline sale's tenders against its total.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct TenderSettlement {
    pub tendered_cents: i64,
    pub cash_cents: i64,
    pub account_cents: i64,
    pub change_cents: i64,
    /// The drawer's difference from the exact cash leg under the branch's
    /// cash rounding rule: positive on a round up.
    pub rounding_cents: i64,
}

/// Rounds a cash amount in cents to the nearest five cents, symmetrically,
/// as `pos.RoundCash` does (CP-18): endings 1, 2, 6 and 7 go down, 3, 4, 8
/// and 9 go up, 0 and 5 stay. Returns the rounded amount and its signed
/// difference from the input.
pub fn round_cash(cents: i64) -> (i64, i64) {
    let magnitude = cents.abs();
    let rounded = match magnitude % 5 {
        0 => magnitude,
        1 => magnitude - 1,
        2 => magnitude - 2,
        3 => magnitude + 2,
        _ => magnitude + 1,
    };
    let rounded = if cents < 0 { -rounded } else { rounded };
    (rounded, rounded - cents)
}

/// Whether one cash payment can stand a rounding difference, as the live
/// completion decides: the change comes out of the cash tenders in the order
/// they were taken, and the largest payment left must hold it on its own.
fn cash_leg_carries(tenders: &[OfflineTenderCalc], change: i64, diff: i64) -> bool {
    let mut largest = 0;
    let mut left = change;
    for t in tenders {
        if !t.method.trim().eq_ignore_ascii_case("cash") {
            continue;
        }
        let mut amount = t.amount_cents;
        if left > 0 {
            let give = amount.min(left);
            amount -= give;
            left -= give;
        }
        largest = largest.max(amount);
    }
    largest > 0 && largest >= diff
}

/// Refusal when offline tender settlement fails.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct TenderRefusal {
    pub code: &'static str,
    pub message: String,
    pub partial: TenderSettlement,
}

/// Sums an offline sale's tenders and computes change from cash, matching
/// `salesdoc.SettleOfflineTenders` with the live `pos.RoundCashFor`: under
/// the branch's cash rounding rule the cash leg rounds, the change falls out
/// of the rounded leg, and where no single cash payment can stand the
/// difference the cash is taken exactly.
pub fn settle_tenders(
    total_cents: i64,
    tenders: &[OfflineTenderCalc],
    rounding: CashRoundingMode,
) -> Result<TenderSettlement, TenderRefusal> {
    let mut out = TenderSettlement {
        tendered_cents: 0,
        cash_cents: 0,
        account_cents: 0,
        change_cents: 0,
        rounding_cents: 0,
    };
    if tenders.is_empty() {
        return Err(TenderRefusal {
            code: "insufficient_tender",
            message: "at least one tender is required".to_string(),
            partial: out,
        });
    }
    let mut account_legs = 0usize;
    for (idx, t) in tenders.iter().enumerate() {
        if t.amount_cents <= 0 {
            return Err(TenderRefusal {
                code: "invalid_tender_amount",
                message: format!(
                    "tender {idx} carries a non positive amount ({})",
                    t.amount_cents
                ),
                partial: out,
            });
        }
        out.tendered_cents += t.amount_cents;
        match t.method.trim().to_ascii_lowercase().as_str() {
            "cash" => out.cash_cents += t.amount_cents,
            "check" => {}
            "account" => {
                out.account_cents += t.amount_cents;
                account_legs += 1;
            }
            other => {
                return Err(TenderRefusal {
                    code: "tender_not_offline",
                    message: format!("tender method {other} is not available offline"),
                    partial: out,
                });
            }
        }
    }
    if account_legs > 1 {
        return Err(TenderRefusal {
            code: "one_account_leg",
            message: "a sale takes at most one account tender: put the whole account amount on one"
                .to_string(),
            partial: out,
        });
    }
    let mut change = out.tendered_cents - total_cents;
    let cash_leg_exact = out.cash_cents - change;
    if rounding == CashRoundingMode::Nearest5Cents && cash_leg_exact > 0 {
        let (rounded, diff) = round_cash(cash_leg_exact);
        if diff != 0 && cash_leg_carries(tenders, out.cash_cents - rounded, diff) {
            change = out.cash_cents - rounded;
            out.rounding_cents = diff;
            if change < 0 {
                return Err(TenderRefusal {
                    code: "insufficient_tender",
                    message: format!(
                        "the cash due is {rounded} cents once the branch rounds it to the nearest five cents, and the tenders carry {}",
                        out.cash_cents
                    ),
                    partial: out,
                });
            }
        }
    }
    if change < 0 {
        return Err(TenderRefusal {
            code: "insufficient_tender",
            message: format!(
                "the sale needs {total_cents} cents and the tenders carry {}",
                out.tendered_cents
            ),
            partial: out,
        });
    }
    if change > out.cash_cents {
        return Err(TenderRefusal {
            code: "change_from_cash",
            message: "only a cash tender can give change: the over tender exceeds the cash taken"
                .to_string(),
            partial: out,
        });
    }
    out.change_cents = change;
    Ok(out)
}
