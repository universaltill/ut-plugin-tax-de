package fiscalsign

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ErrCannotSign means the sale cannot be put on a Beleg whose two halves
// agree, or the configured treatment says not to sign it. The caller
// answers `cannot-sign` (contract 1.3.0): core completes the sale,
// journals it unsigned and alerts the operator. A TSE signature cannot be
// corrected afterwards, so a declared gap beats an irreversible false
// record.
var ErrCannotSign = errors.New("cannot sign")

// Tip recipients, as core sends them (ADR-0061 Decision 3, contract 1.9.0).
const (
	TipRecipientEmployee = "employee"
	TipRecipientBusiness = "business"
)

// BusinessTipTreatment is the merchant's setting for a tip the BUSINESS
// keeps (DSFinV-K TrinkgeldAG). It is taxable turnover, but DSFinV-K fixes
// no rate ("gemäß der Zuordnung zu den USt-Schlüsseln"), and the researched
// sources give more than one defensible method (ut-docs#833), so the
// merchant's Steuerberater picks.
type BusinessTipTreatment string

const (
	// BusinessTipProportional (default) splits the tip across the sale's
	// own VAT rates in proportion to their gross — the tip as extra
	// consideration for the same supply, the way DSFinV-K treats an
	// Aufschlag.
	BusinessTipProportional BusinessTipTreatment = "proportional"
	// BusinessTipStandardRate puts the whole tip at 19% (NORMAL).
	BusinessTipStandardRate BusinessTipTreatment = "standard_rate"
	// BusinessTipRefuse does not sign a sale carrying a business tip.
	BusinessTipRefuse BusinessTipTreatment = "refuse"
)

// ParseBusinessTipTreatment reads the `tip_business_vat_treatment`
// setting. Empty means the default; any value it does not recognise is
// read as refuse — a compliance setting is never guessed at.
func ParseBusinessTipTreatment(s string) BusinessTipTreatment {
	switch v := BusinessTipTreatment(strings.ToLower(strings.TrimSpace(s))); v {
	case "":
		return BusinessTipProportional
	case BusinessTipProportional, BusinessTipStandardRate, BusinessTipRefuse:
		return v
	default:
		return BusinessTipRefuse
	}
}

// Options carries the merchant settings that shape the signed receipt.
// The zero value is the default treatment.
type Options struct {
	BusinessTip BusinessTipTreatment
}

// Receipt is the two halves of a fiskaly standard_v1 receipt, already
// checked to be equal.
type Receipt struct {
	VAT      []VATAmount
	Payments []PaymentAmount
}

// standardRateBP is Germany's standard VAT rate (fiskaly NORMAL).
const standardRateBP = 1900

// BuildReceipt maps a `fiscal.sign.ask` request onto fiskaly's
// amounts_per_vat_rate / amounts_per_payment_type so that the two halves of
// DSFinV-K's `Beleg^<gross per VAT rate>^<per payment type>` agree
// (ut-docs#833 has the research and its sources):
//
//   - gross per rate is `net` (tax-inclusive) or `net + tax` (exclusive),
//     read from `tax_inclusive` when core sends it, else inferred from
//     which reading reconciles with `total`;
//   - a sale-level discount (DSFinV-K Rabatt, UStAE 10.3) is split across
//     the rates in proportion to their gross — floors, remainder on the
//     highest rate — exactly as the contract and core's own VAT-invoice
//     engine do;
//   - a service charge is already inside `vat_breakdown` (contract 1.5.0)
//     and is never applied again;
//   - an employee's tip (TrinkgeldAN, USt-Schlüssel 5) is not the
//     business's turnover: it goes in the 0%/NULL bucket;
//   - a tip the business keeps (TrinkgeldAG) is turnover, placed per
//     Options.BusinessTip — proportionally across the sale's taxable
//     rates only, never into NULL;
//   - a rate with no DSFinV-K bucket (anything but 19/7/10.7/5.5/0%) is
//     refused, never signed under another rate;
//   - the payment side counts each tip exactly once, whether or not core
//     already folded it into the payment's amount (both conventions exist
//     in core; `total` decides which one this request uses). Residual
//     risk, until core sends one convention (ut-docs#2571): a reader-reported tip on a tender over-paid by
//     exactly the tip total reads as "tip inside amount".
//
// Anything that does not reconcile returns ErrCannotSign.
func BuildReceipt(req Request, opt Options) (Receipt, error) {
	gross, err := discountedGrossByRate(req)
	if err != nil {
		return Receipt{}, err
	}

	var paid, tips int64
	for _, p := range req.Payments {
		if p.Amount < 0 || p.TipAmount < 0 {
			return Receipt{}, fmt.Errorf("%w: negative payment or tip", ErrCannotSign)
		}
		paid += p.Amount
		tips += p.TipAmount
	}
	var amountsIncludeTips bool
	switch {
	case paid == req.Total:
		amountsIncludeTips = false
	case paid == req.Total+tips:
		amountsIncludeTips = true
	default:
		return Receipt{}, fmt.Errorf("%w: payments %d reconcile with total %d neither with nor without tips %d", ErrCannotSign, paid, req.Total, tips)
	}

	saleGross := copyBuckets(gross)
	payBuckets := map[string]int64{}
	var employeeTips, businessTips int64
	for _, p := range req.Payments {
		collected := p.Amount
		if !amountsIncludeTips {
			collected += p.TipAmount
		}
		payBuckets[PaymentTypeBucket(p.Method)] += collected
		switch p.TipRecipient {
		case "", TipRecipientEmployee:
			employeeTips += p.TipAmount
		case TipRecipientBusiness:
			businessTips += p.TipAmount
		default:
			return Receipt{}, fmt.Errorf("%w: unknown tip_recipient %q", ErrCannotSign, p.TipRecipient)
		}
	}

	gross[0] += employeeTips
	if businessTips > 0 {
		switch opt.BusinessTip {
		case "", BusinessTipProportional:
			// Weighted by the TAXABLE rates only: a business tip is
			// turnover, so no share of it may land in the 0%/NULL bucket
			// (a fully zero-rated sale leaves nothing to weight by → refuse).
			delete(saleGross, 0)
			shares, err := apportion(businessTips, saleGross)
			if err != nil {
				return Receipt{}, err
			}
			for rate, s := range shares {
				gross[rate] += s
			}
		case BusinessTipStandardRate:
			gross[standardRateBP] += businessTips
		default:
			return Receipt{}, fmt.Errorf("%w: business tip and tip_business_vat_treatment=%q", ErrCannotSign, opt.BusinessTip)
		}
	}

	vatBuckets := map[string]int64{}
	var vatSum, paySum int64
	for rate, cents := range gross {
		if cents == 0 {
			continue
		}
		bucket, ok := VATRateBucket(rate)
		if !ok {
			return Receipt{}, fmt.Errorf("%w: VAT rate %d bp has no DSFinV-K bucket", ErrCannotSign, rate)
		}
		vatBuckets[bucket] += cents
		vatSum += cents
	}
	for _, cents := range payBuckets {
		paySum += cents
	}
	if vatSum != paySum {
		return Receipt{}, fmt.Errorf("%w: VAT side %d != payment side %d", ErrCannotSign, vatSum, paySum)
	}

	r := Receipt{}
	for rate, cents := range vatBuckets {
		r.VAT = append(r.VAT, VATAmount{VATRate: rate, Amount: MinorToDecimalString(cents)})
	}
	for typ, cents := range payBuckets {
		r.Payments = append(r.Payments, PaymentAmount{PaymentType: typ, Amount: MinorToDecimalString(cents)})
	}
	sort.Slice(r.VAT, func(i, j int) bool { return r.VAT[i].VATRate < r.VAT[j].VATRate })
	sort.Slice(r.Payments, func(i, j int) bool { return r.Payments[i].PaymentType < r.Payments[j].PaymentType })
	return r, nil
}

// discountedGrossByRate returns the gross per basis-point rate after the
// sale-level discount, and fails unless it sums to `total`.
func discountedGrossByRate(req Request) (map[int]int64, error) {
	if req.SaleDiscount < 0 {
		return nil, fmt.Errorf("%w: negative sale_discount", ErrCannotSign)
	}
	var sumNet, sumTax int64
	for _, l := range req.VATBreakdown {
		sumNet += l.Net
		sumTax += l.Tax
	}
	var inclusive bool
	switch {
	case req.TaxInclusive != nil:
		inclusive = *req.TaxInclusive
	case sumNet-req.SaleDiscount == req.Total:
		inclusive = true // also the zero-rated case, where both readings agree
	default:
		inclusive = false
	}
	gross := map[int]int64{}
	for _, l := range req.VATBreakdown {
		g := l.Net + l.Tax
		if inclusive {
			g = l.Net
		}
		gross[l.RateBP] += g
	}
	if req.SaleDiscount > 0 {
		shares, err := apportion(req.SaleDiscount, gross)
		if err != nil {
			return nil, err
		}
		for rate, s := range shares {
			gross[rate] -= s
		}
	}
	var sum int64
	for _, g := range gross {
		sum += g
	}
	if sum != req.Total {
		return nil, fmt.Errorf("%w: gross per rate sums to %d, total is %d", ErrCannotSign, sum, req.Total)
	}
	return gross, nil
}

// apportion splits amount across the rates in proportion to their gross:
// every rate but the highest gets floor(amount × gross / Σgross), the
// highest absorbs the remainder — the contract's and core's
// VATBandsForSale convention, so the shares sum to amount exactly.
func apportion(amount int64, gross map[int]int64) (map[int]int64, error) {
	rates := make([]int, 0, len(gross))
	var total int64
	for rate, g := range gross {
		if g < 0 {
			return nil, fmt.Errorf("%w: negative gross at rate %d", ErrCannotSign, rate)
		}
		if g > 0 {
			rates = append(rates, rate)
			total += g
		}
	}
	if total == 0 {
		return nil, fmt.Errorf("%w: nothing to apportion %d across", ErrCannotSign, amount)
	}
	sort.Ints(rates)
	shares := map[int]int64{}
	var given int64
	for _, rate := range rates[:len(rates)-1] {
		s := amount * gross[rate] / total
		shares[rate] = s
		given += s
	}
	shares[rates[len(rates)-1]] = amount - given
	return shares, nil
}

func copyBuckets(m map[int]int64) map[int]int64 {
	out := make(map[int]int64, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
