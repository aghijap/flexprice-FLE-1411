package service

import (
	"github.com/flexprice/flexprice/internal/api/dto"
	"github.com/flexprice/flexprice/internal/domain/invoice"
	"github.com/flexprice/flexprice/internal/types"
	"github.com/samber/lo"
	"github.com/shopspring/decimal"
)

// buildConvertedFinalizedInvoice builds a FINALIZED invoice already converted to the billing
// currency (inr), carrying the frozen fx_conversion whose source records the charge-currency (usd)
// prepaid credits. amountPaidInr/prepaidInr are billing-currency amounts; prepaidCharge is the exact
// usd credits from the source snapshot. priorRefundInr seeds a prior refund (e.g. a refund credit note).
func (s *InvoiceVoidRecalculateSuite) buildConvertedFinalizedInvoice(
	id string,
	amountPaidInr, prepaidInr, prepaidCharge, rate, priorRefundInr decimal.Decimal,
	paymentStatus types.PaymentStatus,
) *invoice.Invoice {
	now := s.testData.now
	periodStart := s.testData.subscription.CurrentPeriodStart
	periodEnd := s.testData.subscription.CurrentPeriodEnd
	bp := string(types.BILLING_PERIOD_MONTHLY)
	total := amountPaidInr.Add(prepaidInr)

	inv := &invoice.Invoice{
		ID:                         id,
		CustomerID:                 s.testData.customer.ID,
		SubscriptionID:             lo.ToPtr(s.testData.subscription.ID),
		InvoiceType:                types.InvoiceTypeSubscription,
		InvoiceStatus:              types.InvoiceStatusFinalized,
		PaymentStatus:              paymentStatus,
		Currency:                   "inr",
		Subtotal:                   total,
		Total:                      total,
		AmountDue:                  amountPaidInr,
		AmountPaid:                 amountPaidInr,
		AmountRemaining:            decimal.Zero,
		RefundedAmount:             priorRefundInr,
		TotalPrepaidCreditsApplied: prepaidInr,
		FxConversion: &types.FxConversion{
			ChargeCurrency:  "usd",
			BillingCurrency: "inr",
			Rate:            rate,
			Scope:           "tenant",
			ConvertedAt:     now,
			Source: types.FxConversionSource{
				Subtotal:                   total.Div(rate),
				TotalPrepaidCreditsApplied: prepaidCharge,
				Net:                        amountPaidInr.Div(rate),
			},
		},
		BillingPeriod: &bp,
		PeriodStart:   &periodStart,
		PeriodEnd:     &periodEnd,
		FinalizedAt:   &now,
		BaseModel:     types.GetDefaultBaseModel(s.GetContext()),
	}
	s.NoError(s.invoiceRepo.CreateWithLineItems(s.GetContext(), inv))
	return inv
}

func (s *InvoiceVoidRecalculateSuite) walletBalance(walletID string) decimal.Decimal {
	w, err := s.walletRepo.GetWalletByID(s.GetContext(), walletID)
	s.NoError(err)
	return w.Balance
}

// Voiding a converted invoice returns the full funded value to the CHARGE-currency prepaid wallet at
// the frozen rate: cash leg ÷rate, credits leg from fx_conversion.source (§6.3). refunded_amount
// stays the billing-currency field.
func (s *InvoiceVoidRecalculateSuite) TestVoidConverted_Mixed_RefundsChargeCurrencyWallet() {
	usdWallet := s.buildPrepaidWallet("wallet_usd_mixed", decimal.Zero)
	// ₹8,000 cash + ₹2,000 credits ($20 charge) at rate 100 → $80 cash + $20 credits = $100 back.
	inv := s.buildConvertedFinalizedInvoice("inv_conv_mixed",
		decimal.NewFromInt(8000), decimal.NewFromInt(2000), decimal.NewFromInt(20), decimal.NewFromInt(100), decimal.Zero,
		types.PaymentStatusSucceeded)

	_, err := s.service.VoidInvoice(s.GetContext(), inv.ID, dto.InvoiceVoidRequest{})
	s.NoError(err)

	s.True(decimal.NewFromInt(100).Equal(s.walletBalance(usdWallet.ID)),
		"usd wallet must receive $100, got %s", s.walletBalance(usdWallet.ID))
	txns := s.refundTxns(usdWallet.ID)
	s.Len(txns, 1)
	s.True(decimal.NewFromInt(100).Equal(txns[0].CreditAmount), "credit got %s", txns[0].CreditAmount)

	updated, err := s.invoiceRepo.Get(s.GetContext(), inv.ID)
	s.NoError(err)
	s.Equal(types.InvoiceStatusVoided, updated.InvoiceStatus)
	s.Equal(types.PaymentStatusRefunded, updated.PaymentStatus)
	s.True(decimal.NewFromInt(10000).Equal(updated.RefundedAmount),
		"refunded_amount stays inr funded value ₹10,000, got %s", updated.RefundedAmount)
}

// Prepaid-only void returns the exact charge-currency credits from the source snapshot.
func (s *InvoiceVoidRecalculateSuite) TestVoidConverted_PrepaidOnly_RefundsSourceCredits() {
	usdWallet := s.buildPrepaidWallet("wallet_usd_prepaid", decimal.Zero)
	inv := s.buildConvertedFinalizedInvoice("inv_conv_prepaid",
		decimal.Zero, decimal.NewFromInt(8300), decimal.NewFromInt(100), decimal.NewFromInt(83), decimal.Zero,
		types.PaymentStatusPending)

	_, err := s.service.VoidInvoice(s.GetContext(), inv.ID, dto.InvoiceVoidRequest{})
	s.NoError(err)

	s.True(decimal.NewFromInt(100).Equal(s.walletBalance(usdWallet.ID)),
		"usd wallet must receive the source credits $100, got %s", s.walletBalance(usdWallet.ID))
}

// Cash-only void divides the cash by the frozen rate into the charge-currency wallet.
func (s *InvoiceVoidRecalculateSuite) TestVoidConverted_CashOnly_DividesByFrozenRate() {
	usdWallet := s.buildPrepaidWallet("wallet_usd_cash", decimal.Zero)
	inv := s.buildConvertedFinalizedInvoice("inv_conv_cash",
		decimal.NewFromInt(8300), decimal.Zero, decimal.Zero, decimal.NewFromInt(83), decimal.Zero,
		types.PaymentStatusSucceeded)

	_, err := s.service.VoidInvoice(s.GetContext(), inv.ID, dto.InvoiceVoidRequest{})
	s.NoError(err)

	s.True(decimal.NewFromInt(100).Equal(s.walletBalance(usdWallet.ID)),
		"usd wallet must receive ₹8,300 ÷ 83 = $100, got %s", s.walletBalance(usdWallet.ID))
}

// A prior refund lowers (amount_paid − refunded_amount) before the division; the credits leg still
// returns in full from the source.
func (s *InvoiceVoidRecalculateSuite) TestVoidConverted_PartialPriorRefund_CashNetOfPrior() {
	usdWallet := s.buildPrepaidWallet("wallet_usd_partial", decimal.Zero)
	// ₹8,300 paid, ₹4,150 already refunded → remaining cash ₹4,150 ÷ 83 = $50.
	inv := s.buildConvertedFinalizedInvoice("inv_conv_partial",
		decimal.NewFromInt(8300), decimal.Zero, decimal.Zero, decimal.NewFromInt(83), decimal.NewFromInt(4150),
		types.PaymentStatusSucceeded)

	_, err := s.service.VoidInvoice(s.GetContext(), inv.ID, dto.InvoiceVoidRequest{})
	s.NoError(err)

	s.True(decimal.NewFromInt(50).Equal(s.walletBalance(usdWallet.ID)),
		"usd wallet must receive $50 (cash net of prior refund), got %s", s.walletBalance(usdWallet.ID))
}

// Voiding a converted invoice records the frozen-rate conversion on the wallet transaction (§6.3):
// the rate, the charge and billing currencies, and the billing-currency amount reversed.
func (s *InvoiceVoidRecalculateSuite) TestVoidConverted_RecordsConversionOnWalletTransaction() {
	usdWallet := s.buildPrepaidWallet("wallet_usd_meta", decimal.Zero)
	// ₹8,000 cash + ₹2,000 credits ($20) at rate 100 → $100 back; billing equivalent ₹10,000.
	inv := s.buildConvertedFinalizedInvoice("inv_conv_meta",
		decimal.NewFromInt(8000), decimal.NewFromInt(2000), decimal.NewFromInt(20), decimal.NewFromInt(100), decimal.Zero,
		types.PaymentStatusSucceeded)

	_, err := s.service.VoidInvoice(s.GetContext(), inv.ID, dto.InvoiceVoidRequest{})
	s.NoError(err)

	txns := s.refundTxns(usdWallet.ID)
	s.Len(txns, 1)
	md := txns[0].Metadata
	s.Equal("usd", md["fx_charge_currency"])
	s.Equal("inr", md["fx_billing_currency"])

	rate, err := decimal.NewFromString(md["fx_rate"])
	s.NoError(err, "fx_rate must be recorded, got %q", md["fx_rate"])
	s.True(decimal.NewFromInt(100).Equal(rate), "frozen rate got %s", md["fx_rate"])

	billing, err := decimal.NewFromString(md["fx_billing_amount"])
	s.NoError(err, "fx_billing_amount must be recorded, got %q", md["fx_billing_amount"])
	s.True(decimal.NewFromInt(10000).Equal(billing),
		"billing-currency amount reversed must be ₹10,000, got %s", md["fx_billing_amount"])
}

// The credits leg comes from fx_conversion.source exactly, not prepaid_inr ÷ rate — proving no
// rounding drift is reintroduced on the way back.
func (s *InvoiceVoidRecalculateSuite) TestVoidConverted_CreditsLegExactFromSource() {
	usdWallet := s.buildPrepaidWallet("wallet_usd_exact", decimal.Zero)
	// prepaid_inr ₹2,005 would divide to $20.05, but the source recorded exactly $20.
	inv := s.buildConvertedFinalizedInvoice("inv_conv_exact",
		decimal.Zero, decimal.NewFromInt(2005), decimal.NewFromInt(20), decimal.NewFromInt(100), decimal.Zero,
		types.PaymentStatusPending)

	_, err := s.service.VoidInvoice(s.GetContext(), inv.ID, dto.InvoiceVoidRequest{})
	s.NoError(err)

	s.True(decimal.NewFromInt(20).Equal(s.walletBalance(usdWallet.ID)),
		"credits leg must be the source's exact $20, got %s", s.walletBalance(usdWallet.ID))
}
