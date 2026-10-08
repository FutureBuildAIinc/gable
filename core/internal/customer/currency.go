// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package customer

import "strings"

// supportedCurrencies are the ISO 4217 codes whose minor unit is 0 or 2
// places: the only ones a document can carry, because money is an integer of
// the minor unit at a scale of two (ADR 0001 section 7). Codes with three
// places (the dinar family) and the fund codes are out.
var supportedCurrencies = func() map[string]bool {
	m := map[string]bool{}
	for _, c := range strings.Fields(`
		AED AFN ALL AMD ANG AOA ARS AUD AWG AZN BAM BBD BDT BGN BMD BND BOB BRL BSD BTN BWP BYN BZD
		CAD CDF CHF CNY COP CRC CUP CVE CZK DKK DOP DZD EGP ERN ETB EUR FJD FKP GBP GEL GHS GIP GMD
		GTQ GYD HKD HNL HTG HUF IDR ILS INR IRR JMD KES KGS KHR KYD KZT LAK LBP LKR LRD LSL MAD MDL
		MKD MMK MNT MOP MRU MUR MVR MWK MXN MYR MZN NAD NGN NIO NOK NPR NZD PAB PEN PGK PHP PKR PLN
		QAR RON RSD RUB SAR SBD SCR SDG SEK SGD SHP SLE SOS SRD SSP STN SVC SYP SZL THB TJS TMT TOP
		TRY TTD TWD TZS UAH USD UYU UZS VES WST XCD YER ZAR ZMW ZWG
		BIF CLP DJF GNF ISK JPY KMF KRW PYG RWF UGX UYI VND VUV XAF XOF XPF`) {
		m[c] = true
	}
	return m
}()

// SupportedCurrency reports whether code is an ISO 4217 code with a minor
// unit of 0 or 2 places.
func SupportedCurrency(code string) bool { return supportedCurrencies[code] }
