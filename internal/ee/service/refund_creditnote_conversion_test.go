package service

import (
	"github.com/flexprice/flexprice/internal/domain/creditnote"
	"github.com/flexprice/flexprice/internal/domain/invoice"
	"github.com/flexprice/flexprice/internal/domain/wallet"
	"github.com/flexprice/flexprice/internal/types"
	"github.com/samber/lo"
	"github.com/shopspring/decimal"
)

// convertedInvoice builds a FINALIZED invoice converted from usd to inr at the given frozen rate.
func (s *RefundServiceSuite) convertedInvoice(rate decimal.Decimal) *invoice.Invoice {
	inv := &invoice.Invoice{
		ID:            "inv_conv_cn",
		CustomerID:    s.testData.customer.ID,
		InvoiceType:   types.InvoiceTypeOneOff,
		InvoiceStatus: types.InvoiceStatusFinalized,
		PaymentStatus: types.PaymentStatusSucceeded,
		Currency:      "inr",
		AmountDue:     decimal.NewFromInt(8300),
		AmountPaid:    decimal.NewFromInt(8300),
		Total:         decimal.NewFromInt(8300),
		InvoiceNumber: lo.ToPtr("INV-CONV-CN"),
		FxConversion: &types.FxConversion{
			ChargeCurrency:  "usd",
			BillingCurrency: "inr",
			Rate:            rate,
			Scope:           "tenant",
			ConvertedAt:     s.testData.now,
			Source:          types.FxConversionSource{Net: decimal.NewFromInt(100)},
		},
		EnvironmentID: "env_test",
		BaseModel:     types.GetDefaultBaseModel(s.GetContext()),
	}
	s.NoError(s.GetStores().InvoiceRepo.CreateWithLineItems(s.GetContext(), inv))
	return inv
}

func (s *RefundServiceSuite) creditNoteFor(inv *invoice.Invoice, amount decimal.Decimal) *creditnote.CreditNote {
	cn := s.creditNote(amount)
	cn.InvoiceID = inv.ID
	cn.Currency = inv.Currency
	return cn
}

// A PREPAID_WALLET refund on a converted invoice targets the charge-currency wallet at the frozen
// rate: ₹8,300 ÷ 83 = $100 (§7).
func (s *RefundServiceSuite) TestPrepareRefundsForCreditNote_ConvertedPrepaidWallet_ChargeCurrency() {
	inv := s.convertedInvoice(decimal.NewFromInt(83))

	rows, err := s.service.PrepareRefundsForCreditNote(s.GetContext(),
		s.creditNoteFor(inv, decimal.NewFromInt(8300)), inv, lo.ToPtr(types.RefundTargetPrepaidWallet))
	s.NoError(err)
	s.Len(rows, 1)
	s.Equal(types.RefundDestinationWallet, rows[0].RefundDestination)
	s.Equal("usd", rows[0].Currency, "refund must be in the charge currency")
	s.True(decimal.NewFromInt(100).Equal(rows[0].Amount), "amount got %s", rows[0].Amount)
	s.Equal("cn_refund_1", lo.FromPtr(rows[0].CreditNoteID))
}

// An unset target on a converted invoice keeps the money with the customer, in the charge-currency wallet.
func (s *RefundServiceSuite) TestPrepareRefundsForCreditNote_ConvertedUnsetTarget_ChargeCurrency() {
	inv := s.convertedInvoice(decimal.NewFromInt(83))

	rows, err := s.service.PrepareRefundsForCreditNote(s.GetContext(),
		s.creditNoteFor(inv, decimal.NewFromInt(8300)), inv, nil)
	s.NoError(err)
	s.Len(rows, 1)
	s.Equal(types.RefundDestinationWallet, rows[0].RefundDestination)
	s.Equal("usd", rows[0].Currency)
	s.True(decimal.NewFromInt(100).Equal(rows[0].Amount), "amount got %s", rows[0].Amount)
}

// BACK_TO_SOURCE on a converted invoice is never rate-converted: it refunds the billing-currency
// amount (gateway, or an inr wallet on fallback) (§7).
func (s *RefundServiceSuite) TestPrepareRefundsForCreditNote_ConvertedBackToSource_StaysBillingCurrency() {
	inv := s.convertedInvoice(decimal.NewFromInt(83))

	rows, err := s.service.PrepareRefundsForCreditNote(s.GetContext(),
		s.creditNoteFor(inv, decimal.NewFromInt(8300)), inv, backToSource())
	s.NoError(err)
	s.Len(rows, 1)
	s.Equal("inr", rows[0].Currency, "back-to-source is never rate-converted")
	s.True(decimal.NewFromInt(8300).Equal(rows[0].Amount), "amount got %s", rows[0].Amount)
}

// End-to-end: dispatching the converted PREPAID_WALLET refund credits the customer's charge-currency
// prepaid wallet with the reversed amount.
func (s *RefundServiceSuite) TestPrepareRefundsForCreditNote_ConvertedPrepaidWallet_SettlesToChargeWallet() {
	inv := s.convertedInvoice(decimal.NewFromInt(83))
	usd := &wallet.Wallet{
		ID:                  "wallet_usd_cn",
		CustomerID:          s.testData.customer.ID,
		Currency:            "usd",
		Balance:             decimal.Zero,
		CreditBalance:       decimal.Zero,
		WalletStatus:        types.WalletStatusActive,
		ConversionRate:      decimal.NewFromInt(1),
		TopupConversionRate: decimal.NewFromInt(1),
		WalletType:          types.WalletTypePrePaid,
		BaseModel:           types.GetDefaultBaseModel(s.GetContext()),
	}
	s.NoError(s.GetStores().WalletRepo.CreateWallet(s.GetContext(), usd))

	rows, err := s.service.PrepareRefundsForCreditNote(s.GetContext(),
		s.creditNoteFor(inv, decimal.NewFromInt(8300)), inv, lo.ToPtr(types.RefundTargetPrepaidWallet))
	s.Require().NoError(err)
	s.Require().Len(rows, 1)
	s.NoError(s.service.Dispatch(s.GetContext(), rows[0].ID))

	got, err := s.GetStores().WalletRepo.GetWalletByID(s.GetContext(), usd.ID)
	s.NoError(err)
	s.True(decimal.NewFromInt(100).Equal(got.Balance), "usd wallet balance got %s", got.Balance)
}
