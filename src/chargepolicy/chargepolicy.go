// Package chargepolicy is the pure, host-independent half of this plugin's
// charge.policy.ask answer (ADR-0061 Decision 1, ut-docs#974): Germany's
// service-charge and tip policy, as core's
// universal-till internal/pages/charge_hook.go parses it.
//
// Same reason as src/fiscalsign/ and src/taxrate/ for being its own package
// with no wasip1 tag: `go test ./src/chargepolicy/...` runs on the host,
// while main.go cannot be unit-tested at all.
//
// The answer changes nothing core would not already do with no plugin
// installed (charge permitted, apportioned at the lines' own rates, tip to
// the employee) — its value is that core now holds Germany's policy as an
// ANSWER rather than a fallback, plus the DSFinV-K business case. The
// merchant's own configured service-charge rate stays authoritative for
// what is charged; core never applies ServiceChargeDefaultRateBP.
package chargepolicy

import "github.com/universaltill/ut-plugin-tax-de/src/fiscalsign"

// Answer is the charge.policy.ask wire shape. No omitempty anywhere: an
// absent service_charge_permitted reads as permitted on core's side, so
// the field must never be able to vanish from the wire. There is
// deliberately no Charges field — see DE().
type Answer struct {
	ServiceChargePermitted     bool   `json:"service_charge_permitted"`
	ServiceChargeDefaultRateBP int    `json:"service_charge_default_rate_bp"`
	ServiceChargeTaxBasisBP    int    `json:"service_charge_tax_basis_bp"`
	TipDefaultRecipient        string `json:"tip_default_recipient"`
	FiscalBusinessCase         string `json:"fiscal_business_case"`
}

// dsfinvkTrinkgeldAN is the DSFinV-K Geschäftsvorfall type (GV_TYP) for a
// tip that belongs to the employee — the same business case
// fiscalsign.BuildReceipt already signs an employee tip under (the
// 0%/NULL bucket, USt-Schlüssel 5).
const dsfinvkTrinkgeldAN = "TrinkgeldAN"

// DE is Germany's researched row from ut-docs#961:
//
//   - A service charge is lawful but rare, so it is permitted with no
//     suggested rate (0): the merchant opts in by configuring one.
//   - It is part of the consideration for the supply it accompanies, so it
//     is taxed at that supply's own §12 UStG rate(s) — basis 0 tells core
//     to apportion by net line value (pos.ApportionServiceChargeTax). A flat
//     rate would mis-tax every bill mixing 7% and 19% lines.
//   - A tip defaults to the employee (TrinkgeldAN), not operating income.
//     A tip the business keeps (TrinkgeldAG) is an explicit per-payment
//     choice core already carries (PaymentInput.TipRecipient) and that
//     fiscalsign.BuildReceipt treats per tip_business_vat_treatment — it is
//     not a market default, so nothing more is declared here.
//   - No additive statutory levies: ADR-0062's Charges list is applied by
//     core verbatim to every sale with no merchant override, and Germany
//     has none.
func DE() Answer {
	return Answer{
		ServiceChargePermitted:     true,
		ServiceChargeDefaultRateBP: 0,
		ServiceChargeTaxBasisBP:    0,
		TipDefaultRecipient:        fiscalsign.TipRecipientEmployee,
		FiscalBusinessCase:         dsfinvkTrinkgeldAN,
	}
}
