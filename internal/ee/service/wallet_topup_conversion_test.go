package service

import (
	"github.com/flexprice/flexprice/internal/api/dto"
	"github.com/flexprice/flexprice/internal/domain/fxrate"
	"github.com/flexprice/flexprice/internal/types"
	"github.com/samber/lo"
	"github.com/shopspring/decimal"
)

// seedTenantRate creates a published tenant-scope fx rate for the pair.
func (s *WalletServiceSuite) seedTenantRate(from, to, rate string) {
	s.NoError(s.GetStores().FXRateRepo.Create(s.GetContext(), &fxrate.FXRate{
		ID:            types.GenerateUUIDWithPrefix(types.UUID_PREFIX_FX_RATE),
		Scope:         types.FXRateScopeTenant,
		ScopeID:       types.FXRateScopeIDTenant,
		FromCurrency:  from,
		ToCurrency:    to,
		Rate:          decimal.RequireFromString(rate),
		EnvironmentID: types.GetEnvironmentID(s.GetContext()),
		BaseModel:     types.GetDefaultBaseModel(s.GetContext()),
	}))
}

func (s *WalletServiceSuite) setCustomerBillingCurrency(billing string) {
	s.testData.customer.BillingCurrency = lo.ToPtr(billing)
	s.NoError(s.GetStores().CustomerRepo.Update(s.GetContext(), s.testData.customer))
}

// A cross-currency purchased top-up runs the invoice conversion inside the same transaction that
// mints the pending wallet credit, so a missing rate aborts the whole operation instead of leaving
// a stranded pending credit (§6.2). (The DB rollback itself is a Postgres guarantee — the in-memory
// test WithTx is a passthrough, so this asserts the operation errors, not the row's absence.)
func (s *WalletServiceSuite) TestTopUpWallet_CrossCurrency_MissingRate_Errors() {
	s.seedAutoComplete(false)
	s.setCustomerBillingCurrency("inr") // wallet is usd; no usd->inr rate seeded

	_, err := s.service.TopUpWallet(s.GetContext(), s.testData.wallet.ID, &dto.TopUpWalletRequest{
		CreditsToAdd:      decimal.NewFromInt(100),
		TransactionReason: types.TransactionReasonPurchasedCreditInvoiced,
		IdempotencyKey:    lo.ToPtr("topup-xcur-norate"),
	})
	s.Require().Error(err, "a cross-currency top-up with no rate must fail inside the tx")
}

// With a rate present, the top-up invoice is converted to the customer's billing currency inside the
// top-up transaction; the pending wallet credit stays in the wallet's own currency (§6.2).
func (s *WalletServiceSuite) TestTopUpWallet_CrossCurrency_ConvertsInvoiceInsideTx() {
	s.seedAutoComplete(false)
	s.setCustomerBillingCurrency("inr")
	s.seedTenantRate("usd", "inr", "83")

	resp, err := s.service.TopUpWallet(s.GetContext(), s.testData.wallet.ID, &dto.TopUpWalletRequest{
		CreditsToAdd:      decimal.NewFromInt(100),
		TransactionReason: types.TransactionReasonPurchasedCreditInvoiced,
		IdempotencyKey:    lo.ToPtr("topup-xcur-rate"),
	})
	s.Require().NoError(err)
	s.Require().NotNil(resp.InvoiceID)

	// Invoice converted to the billing currency, at the frozen rate.
	inv, err := s.GetStores().InvoiceRepo.Get(s.GetContext(), *resp.InvoiceID)
	s.Require().NoError(err)
	s.Equal("inr", inv.Currency, "top-up invoice must be issued in the billing currency")
	s.Require().NotNil(inv.FxConversion)
	s.Equal("usd", inv.FxConversion.ChargeCurrency)
	s.True(decimal.RequireFromString("8300").Equal(inv.AmountDue), "amount_due got %s", inv.AmountDue)

	// Pending wallet credit stays in the wallet's own currency — conversion never touches it.
	s.Require().NotNil(resp.WalletTransaction)
	s.Equal("usd", resp.WalletTransaction.Currency)
	s.True(decimal.NewFromInt(100).Equal(resp.WalletTransaction.CreditAmount), "credits got %s", resp.WalletTransaction.CreditAmount)
}

// A pay-first top-up leaves the invoice DRAFT (checkout finalizes it later), so the draft is not
// converted at finalize. This proves §6.2's real gap: the conversion must run inside the top-up tx
// itself. handlePurchasedCreditInvoicedTransaction is called directly to observe the draft right
// after the tx, before the (separate) checkout-session step.
func (s *WalletServiceSuite) TestTopUpWallet_PayFirstCrossCurrency_ConvertsDraftInsideTx() {
	s.setCustomerBillingCurrency("inr")
	s.seedTenantRate("usd", "inr", "83")

	ws := s.service.(*walletService)
	_, invoiceID, err := ws.handlePurchasedCreditInvoicedTransaction(
		s.GetContext(), s.testData.wallet.ID, lo.ToPtr("topup-payfirst-rate"),
		&dto.TopUpWalletRequest{
			CreditsToAdd:      decimal.NewFromInt(100),
			TransactionReason: types.TransactionReasonPurchasedCreditInvoiced,
			Checkout:          s.checkoutParamsRazorpay(),
			IdempotencyKey:    lo.ToPtr("topup-payfirst-rate"),
		})
	s.Require().NoError(err)

	inv, err := s.GetStores().InvoiceRepo.Get(s.GetContext(), invoiceID)
	s.Require().NoError(err)
	s.Equal(types.InvoiceStatusDraft, inv.InvoiceStatus, "pay-first invoice stays DRAFT until checkout completes")
	s.Equal("inr", inv.Currency, "pay-first draft must be converted inside the top-up tx (before checkout)")
	s.Require().NotNil(inv.FxConversion, "draft must carry fx_conversion after the in-tx conversion")
	s.Equal("usd", inv.FxConversion.ChargeCurrency)
}

// A pay-first cross-currency top-up with no rate fails inside the top-up tx, so in real Postgres the
// pending wallet credit rolls back instead of stranding (§6.2). Without the in-tx conversion the
// draft would be created unconverted and the failure would only surface later at checkout.
func (s *WalletServiceSuite) TestTopUpWallet_PayFirstCrossCurrency_MissingRate_Errors() {
	s.setCustomerBillingCurrency("inr") // no usd->inr rate

	ws := s.service.(*walletService)
	_, _, err := ws.handlePurchasedCreditInvoicedTransaction(
		s.GetContext(), s.testData.wallet.ID, lo.ToPtr("topup-payfirst-norate"),
		&dto.TopUpWalletRequest{
			CreditsToAdd:      decimal.NewFromInt(100),
			TransactionReason: types.TransactionReasonPurchasedCreditInvoiced,
			Checkout:          s.checkoutParamsRazorpay(),
			IdempotencyKey:    lo.ToPtr("topup-payfirst-norate"),
		})
	s.Require().Error(err, "a pay-first cross-currency top-up with no rate must fail inside the top-up tx")
}
