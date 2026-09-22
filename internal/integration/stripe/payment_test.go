package stripe

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/flexprice/flexprice/internal/api/dto"
	"github.com/flexprice/flexprice/internal/domain/entityintegrationmapping"
	"github.com/flexprice/flexprice/internal/domain/invoice"
	"github.com/flexprice/flexprice/internal/interfaces"
	"github.com/flexprice/flexprice/internal/logger"
	"github.com/flexprice/flexprice/internal/types"
	"github.com/samber/lo"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	"github.com/stripe/stripe-go/v82"
)

func TestBuildSyncedLineItems_HappyPath(t *testing.T) {
	mappingRepo := &syncTestMappingRepo{
		mappings: []*entityintegrationmapping.EntityIntegrationMapping{
			{EntityID: "price_1", ProviderEntityID: "prod_1"},
			{EntityID: "price_2", ProviderEntityID: "prod_2"},
		},
	}
	s := &PaymentService{
		logger:       logger.NewNoopLogger(),
		priceSyncSvc: NewStripePriceSyncService(nil, mappingRepo, nil, logger.NewNoopLogger()),
	}
	invoiceResp := &dto.InvoiceResponse{
		LineItems: []*dto.InvoiceLineItemResponse{
			{InvoiceLineItem: invoice.InvoiceLineItem{ID: "li_1", PriceID: lo.ToPtr("price_1"), DisplayName: lo.ToPtr("Seat fee"), Amount: decimal.NewFromInt(50)}},
			{InvoiceLineItem: invoice.InvoiceLineItem{ID: "li_2", PriceID: lo.ToPtr("price_2"), DisplayName: lo.ToPtr("API calls"), Amount: decimal.NewFromInt(30)}},
		},
	}

	lineItems, err := s.buildSyncedLineItems(testContext(), invoiceResp, "usd")

	require.NoError(t, err)
	require.Len(t, lineItems, 2)
	require.Equal(t, "prod_1", *lineItems[0].PriceData.Product)
	require.EqualValues(t, 5000, *lineItems[0].PriceData.UnitAmount)
	require.Equal(t, "prod_2", *lineItems[1].PriceData.Product)
	require.EqualValues(t, 3000, *lineItems[1].PriceData.UnitAmount)
}

func TestBuildSyncedLineItems_MissingPriceIDFallsBackForThatItem(t *testing.T) {
	mappingRepo := &syncTestMappingRepo{
		mappings: []*entityintegrationmapping.EntityIntegrationMapping{
			{EntityID: "price_1", ProviderEntityID: "prod_1"},
		},
	}
	s := &PaymentService{
		logger:       logger.NewNoopLogger(),
		priceSyncSvc: NewStripePriceSyncService(nil, mappingRepo, nil, logger.NewNoopLogger()),
	}
	invoiceResp := &dto.InvoiceResponse{
		LineItems: []*dto.InvoiceLineItemResponse{
			{InvoiceLineItem: invoice.InvoiceLineItem{ID: "li_1", PriceID: lo.ToPtr("price_1"), DisplayName: lo.ToPtr("Seat fee"), Amount: decimal.NewFromInt(50)}},
			{InvoiceLineItem: invoice.InvoiceLineItem{ID: "li_2", PriceID: nil, DisplayName: lo.ToPtr("Manual charge"), Amount: decimal.NewFromInt(20)}},
		},
	}

	lineItems, err := s.buildSyncedLineItems(testContext(), invoiceResp, "usd")

	require.NoError(t, err)
	require.Len(t, lineItems, 2)
	require.Equal(t, "prod_1", *lineItems[0].PriceData.Product)
	require.Nil(t, lineItems[1].PriceData.Product)
	require.Equal(t, "Manual charge", *lineItems[1].PriceData.ProductData.Name)
	require.EqualValues(t, 2000, *lineItems[1].PriceData.UnitAmount)
}

func TestBuildSyncedLineItems_AllMissingPriceIDReturnsNilForFullFallback(t *testing.T) {
	s := &PaymentService{
		logger:       logger.NewNoopLogger(),
		priceSyncSvc: NewStripePriceSyncService(nil, &syncTestMappingRepo{}, nil, logger.NewNoopLogger()),
	}
	invoiceResp := &dto.InvoiceResponse{
		LineItems: []*dto.InvoiceLineItemResponse{
			{InvoiceLineItem: invoice.InvoiceLineItem{ID: "li_1", PriceID: nil, DisplayName: lo.ToPtr("Manual charge"), Amount: decimal.NewFromInt(100)}},
		},
	}

	lineItems, err := s.buildSyncedLineItems(testContext(), invoiceResp, "usd")

	require.NoError(t, err)
	require.Nil(t, lineItems, "no Price-backed items to sync, so the caller falls back to the ad-hoc lump-sum item")
}

func TestBuildSyncedLineItems_DuplicatePriceIDAcrossLineItems(t *testing.T) {
	mappingRepo := &syncTestMappingRepo{
		mappings: []*entityintegrationmapping.EntityIntegrationMapping{
			{EntityID: "price_1", ProviderEntityID: "prod_1"},
		},
	}
	s := &PaymentService{
		logger:       logger.NewNoopLogger(),
		priceSyncSvc: NewStripePriceSyncService(nil, mappingRepo, nil, logger.NewNoopLogger()),
	}
	invoiceResp := &dto.InvoiceResponse{
		LineItems: []*dto.InvoiceLineItemResponse{
			{InvoiceLineItem: invoice.InvoiceLineItem{ID: "li_1", PriceID: lo.ToPtr("price_1"), DisplayName: lo.ToPtr("Base fee"), Amount: decimal.NewFromInt(50)}},
			{InvoiceLineItem: invoice.InvoiceLineItem{ID: "li_2", PriceID: lo.ToPtr("price_1"), DisplayName: lo.ToPtr("Overage"), Amount: decimal.NewFromInt(50)}},
		},
	}

	lineItems, err := s.buildSyncedLineItems(testContext(), invoiceResp, "usd")

	require.NoError(t, err)
	require.Len(t, lineItems, 2, "both line items still get their own Checkout line item")
	require.Equal(t, "prod_1", *lineItems[0].PriceData.Product)
	require.Equal(t, "prod_1", *lineItems[1].PriceData.Product)
}

func TestBuildSyncedLineItems_SyncFailurePropagates(t *testing.T) {
	mappingRepo := &syncTestMappingRepo{listErr: errors.New("db unavailable")}
	s := &PaymentService{
		logger:       logger.NewNoopLogger(),
		priceSyncSvc: NewStripePriceSyncService(nil, mappingRepo, nil, logger.NewNoopLogger()),
	}
	invoiceResp := &dto.InvoiceResponse{
		LineItems: []*dto.InvoiceLineItemResponse{
			{InvoiceLineItem: invoice.InvoiceLineItem{ID: "li_1", PriceID: lo.ToPtr("price_1"), DisplayName: lo.ToPtr("Seat fee"), Amount: decimal.NewFromInt(100)}},
		},
	}

	_, err := s.buildSyncedLineItems(testContext(), invoiceResp, "usd")

	require.Error(t, err)
}

func TestBuildSyncedLineItems_ZeroAmountLineItemsSkipped(t *testing.T) {
	mappingRepo := &syncTestMappingRepo{
		mappings: []*entityintegrationmapping.EntityIntegrationMapping{
			{EntityID: "price_1", ProviderEntityID: "prod_1"},
		},
	}
	s := &PaymentService{
		logger:       logger.NewNoopLogger(),
		priceSyncSvc: NewStripePriceSyncService(nil, mappingRepo, nil, logger.NewNoopLogger()),
	}
	invoiceResp := &dto.InvoiceResponse{
		LineItems: []*dto.InvoiceLineItemResponse{
			{InvoiceLineItem: invoice.InvoiceLineItem{ID: "li_1", PriceID: lo.ToPtr("price_1"), DisplayName: lo.ToPtr("Seat fee"), Amount: decimal.NewFromInt(100)}},
			{InvoiceLineItem: invoice.InvoiceLineItem{ID: "li_2", PriceID: nil, DisplayName: lo.ToPtr("Zeroed credit line"), Amount: decimal.Zero}},
		},
	}

	lineItems, err := s.buildSyncedLineItems(testContext(), invoiceResp, "usd")

	require.NoError(t, err)
	require.Len(t, lineItems, 1)
}

func TestComputeCheckoutDiscount_NoDiscountWhenEqual(t *testing.T) {
	session := &stripe.CheckoutSession{AmountTotal: 10000}

	discount, metadataJSON, err := computeCheckoutDiscount(session, decimal.NewFromInt(100))

	require.NoError(t, err)
	require.True(t, decimal.Zero.Equal(discount))
	require.Empty(t, metadataJSON)
}

func TestComputeCheckoutDiscount_ZeroDecimalCurrencyNoFalseDiscount(t *testing.T) {
	// JPY has zero decimal places: AmountTotal is already the yen amount, not cents.
	// Dividing by 100 (the USD-shaped conversion) would wrongly read 10,000 JPY as a
	// captured amount of 100 and report a false 9,900 discount.
	session := &stripe.CheckoutSession{AmountTotal: 10000, Currency: stripe.CurrencyJPY}

	discount, _, err := computeCheckoutDiscount(session, decimal.NewFromInt(10000))

	require.NoError(t, err)
	require.True(t, decimal.Zero.Equal(discount))
}

func TestComputeCheckoutDiscount_ZeroDecimalCurrencyRealDiscount(t *testing.T) {
	session := &stripe.CheckoutSession{AmountTotal: 8000, Currency: stripe.CurrencyJPY}

	discount, _, err := computeCheckoutDiscount(session, decimal.NewFromInt(10000))

	require.NoError(t, err)
	require.True(t, decimal.NewFromInt(2000).Equal(discount))
}

func TestComputeCheckoutDiscount_NoDiscountWhenCapturedMore(t *testing.T) {
	session := &stripe.CheckoutSession{AmountTotal: 10500}

	discount, _, err := computeCheckoutDiscount(session, decimal.NewFromInt(100))

	require.NoError(t, err)
	require.True(t, decimal.Zero.Equal(discount))
}

func TestComputeCheckoutDiscount_DiscountWithCouponAndPromoCode(t *testing.T) {
	session := &stripe.CheckoutSession{
		AmountTotal: 8000,
		Discounts: []*stripe.CheckoutSessionDiscount{
			{
				Coupon:        &stripe.Coupon{ID: "cp_1", Name: "20 off", AmountOff: 2000},
				PromotionCode: &stripe.PromotionCode{Code: "SAVE20"},
			},
		},
	}

	discount, metadataJSON, err := computeCheckoutDiscount(session, decimal.NewFromInt(100))

	require.NoError(t, err)
	require.True(t, decimal.NewFromInt(20).Equal(discount))

	var entries []stripeCheckoutDiscountEntry
	require.NoError(t, json.Unmarshal([]byte(metadataJSON), &entries))
	require.Len(t, entries, 1)
	require.Equal(t, "cp_1", entries[0].StripeCouponID)
	require.Equal(t, "SAVE20", entries[0].PromotionCode)
	require.EqualValues(t, 2000, entries[0].AmountOff)
}

func TestComputeCheckoutDiscount_MultipleStackedDiscounts(t *testing.T) {
	session := &stripe.CheckoutSession{
		AmountTotal: 7000,
		Discounts: []*stripe.CheckoutSessionDiscount{
			{Coupon: &stripe.Coupon{ID: "cp_1", Name: "First"}},
			{Coupon: &stripe.Coupon{ID: "cp_2", Name: "Second"}},
		},
	}

	discount, metadataJSON, err := computeCheckoutDiscount(session, decimal.NewFromInt(100))

	require.NoError(t, err)
	require.True(t, decimal.NewFromInt(30).Equal(discount))

	var entries []stripeCheckoutDiscountEntry
	require.NoError(t, json.Unmarshal([]byte(metadataJSON), &entries))
	require.Len(t, entries, 2)
}

func TestComputeCheckoutDiscount_DiscountWithNoCouponsListedYet(t *testing.T) {
	// AmountTotal reduced with no session.Discounts populated (e.g. a manual amount edit).
	session := &stripe.CheckoutSession{AmountTotal: 9000}

	discount, metadataJSON, err := computeCheckoutDiscount(session, decimal.NewFromInt(100))

	require.NoError(t, err)
	require.True(t, decimal.NewFromInt(10).Equal(discount))

	var entries []stripeCheckoutDiscountEntry
	require.NoError(t, json.Unmarshal([]byte(metadataJSON), &entries))
	require.Empty(t, entries)
}

func TestComputeCheckoutDiscount_NilCouponOrPromotionCode(t *testing.T) {
	session := &stripe.CheckoutSession{
		AmountTotal: 9000,
		Discounts: []*stripe.CheckoutSessionDiscount{
			{Coupon: nil, PromotionCode: &stripe.PromotionCode{Code: "NOCOUPON"}},
		},
	}

	_, metadataJSON, err := computeCheckoutDiscount(session, decimal.NewFromInt(100))

	require.NoError(t, err)
	var entries []stripeCheckoutDiscountEntry
	require.NoError(t, json.Unmarshal([]byte(metadataJSON), &entries))
	require.Len(t, entries, 1)
	require.Equal(t, "", entries[0].StripeCouponID)
	require.Equal(t, "NOCOUPON", entries[0].PromotionCode)
}

// ── fakes for HandleFlexPriceCheckoutPayment ordering tests ─────────────────────────────

type checkoutTestPaymentService struct {
	interfaces.PaymentService
	payment          *dto.PaymentResponse
	updatePaymentErr error
	updatePaymentReq dto.UpdatePaymentRequest
}

func (f *checkoutTestPaymentService) UpdatePayment(_ context.Context, _ string, req dto.UpdatePaymentRequest) (*dto.PaymentResponse, error) {
	f.updatePaymentReq = req
	if f.updatePaymentErr != nil {
		return nil, f.updatePaymentErr
	}
	return &dto.PaymentResponse{}, nil
}

func (f *checkoutTestPaymentService) GetPayment(_ context.Context, _ string) (*dto.PaymentResponse, error) {
	return f.payment, nil
}

type checkoutTestInvoiceService struct {
	interfaces.InvoiceService
	applyDiscountCalls int
	reconcileCalls     int
	paymentStatus      types.PaymentStatus
	metadata           types.Metadata
}

func (f *checkoutTestInvoiceService) ApplyExternalInvoiceDiscount(_ context.Context, _ string, _ dto.ApplyExternalInvoiceDiscountRequest) error {
	f.applyDiscountCalls++
	return nil
}

func (f *checkoutTestInvoiceService) GetInvoice(_ context.Context, id string) (*dto.InvoiceResponse, error) {
	return &dto.InvoiceResponse{Invoice: invoice.Invoice{
		ID:              id,
		AmountDue:       decimal.NewFromInt(80),
		AmountPaid:      decimal.Zero,
		AmountRemaining: decimal.NewFromInt(80),
		PaymentStatus:   f.paymentStatus,
		Metadata:        f.metadata,
	}}, nil
}

func (f *checkoutTestInvoiceService) ReconcilePaymentStatus(_ context.Context, _ string, _ types.PaymentStatus, _ *decimal.Decimal, _ ...string) error {
	f.reconcileCalls++
	return nil
}

func TestHandleFlexPriceCheckoutPayment_DiscountNotAppliedWhenPaymentClaimFails(t *testing.T) {
	s := &PaymentService{logger: logger.NewNoopLogger()}
	session := &stripe.CheckoutSession{
		AmountTotal: 8000,
		Discounts:   []*stripe.CheckoutSessionDiscount{{Coupon: &stripe.Coupon{ID: "cp_1"}}},
	}
	payment := &dto.PaymentResponse{ID: "pay_1", Amount: decimal.NewFromInt(100), DestinationID: "inv_1"}
	invoiceSvc := &checkoutTestInvoiceService{}
	paymentSvc := &checkoutTestPaymentService{payment: payment, updatePaymentErr: errors.New("payment status changed during update")}

	err := s.HandleFlexPriceCheckoutPayment(context.Background(), session, nil, payment, nil, invoiceSvc, paymentSvc)

	require.Error(t, err)
	require.Equal(t, 0, invoiceSvc.applyDiscountCalls, "a failed/conflicting payment claim must never apply the discount")
}

func TestReconcilePaymentWithInvoiceIfNeeded_SkipsWhenInvoiceAlreadyReconciled(t *testing.T) {
	s := &PaymentService{logger: logger.NewNoopLogger()}
	payment := &dto.PaymentResponse{ID: "pay_1", Amount: decimal.NewFromInt(100), DestinationID: "inv_1"}
	invoiceSvc := &checkoutTestInvoiceService{}
	invoiceSvc.paymentStatus = types.PaymentStatusSucceeded
	paymentSvc := &checkoutTestPaymentService{payment: payment}

	err := s.ReconcilePaymentWithInvoiceIfNeeded(context.Background(), payment.ID, payment.Amount, paymentSvc, invoiceSvc)

	require.NoError(t, err)
	require.Equal(t, 0, invoiceSvc.reconcileCalls, "an already-reconciled invoice must not be reconciled again")
}

func TestReconcilePaymentWithInvoiceIfNeeded_ReconcilesWhenLedgerOmitsThisPayment(t *testing.T) {
	s := &PaymentService{logger: logger.NewNoopLogger()}
	payment := &dto.PaymentResponse{ID: "pay_1", Amount: decimal.NewFromInt(100), DestinationID: "inv_1"}
	// The invoice is settled, but its ledger accounts for a different payment - so
	// this one has genuinely never been credited and a settled-looking status must
	// not be read as evidence that it was.
	invoiceSvc := &checkoutTestInvoiceService{
		paymentStatus: types.PaymentStatusSucceeded,
		metadata:      types.Metadata{types.InvoiceMetadataKeyAppliedPaymentIDs: `["pay_other"]`},
	}
	paymentSvc := &checkoutTestPaymentService{payment: payment}

	err := s.ReconcilePaymentWithInvoiceIfNeeded(context.Background(), payment.ID, payment.Amount, paymentSvc, invoiceSvc)

	require.NoError(t, err)
	require.Equal(t, 1, invoiceSvc.reconcileCalls, "a payment missing from an existing ledger must still be credited")
}

func TestReconcilePaymentWithInvoiceIfNeeded_SkipsWhenPaymentIDAlreadyApplied(t *testing.T) {
	s := &PaymentService{logger: logger.NewNoopLogger()}
	payment := &dto.PaymentResponse{ID: "pay_1", Amount: decimal.NewFromInt(100), DestinationID: "inv_1"}
	invoiceSvc := &checkoutTestInvoiceService{
		paymentStatus: types.PaymentStatusPending,
		metadata:      types.Metadata{types.InvoiceMetadataKeyAppliedPaymentIDs: `["pay_1"]`},
	}
	paymentSvc := &checkoutTestPaymentService{payment: payment}

	err := s.ReconcilePaymentWithInvoiceIfNeeded(context.Background(), payment.ID, payment.Amount, paymentSvc, invoiceSvc)

	require.NoError(t, err)
	require.Equal(t, 0, invoiceSvc.reconcileCalls, "a payment already credited in invoice metadata must not be reconciled again even on a pending invoice")
}

func TestReconcilePaymentWithInvoiceIfNeeded_ReconcilesWhenInvoiceStillUnpaid(t *testing.T) {
	s := &PaymentService{logger: logger.NewNoopLogger()}
	payment := &dto.PaymentResponse{ID: "pay_1", Amount: decimal.NewFromInt(100), DestinationID: "inv_1"}
	invoiceSvc := &checkoutTestInvoiceService{}
	paymentSvc := &checkoutTestPaymentService{payment: payment}

	err := s.ReconcilePaymentWithInvoiceIfNeeded(context.Background(), payment.ID, payment.Amount, paymentSvc, invoiceSvc)

	require.NoError(t, err)
	require.Equal(t, 1, invoiceSvc.reconcileCalls, "a payment claimed as succeeded but not yet reflected on the invoice must be reconciled")
}

func TestHandleFlexPriceCheckoutPayment_DiscountAppliedAfterSuccessfulClaim(t *testing.T) {
	s := &PaymentService{logger: logger.NewNoopLogger()}
	session := &stripe.CheckoutSession{
		AmountTotal: 8000,
		Discounts:   []*stripe.CheckoutSessionDiscount{{Coupon: &stripe.Coupon{ID: "cp_1"}}},
	}
	payment := &dto.PaymentResponse{ID: "pay_1", Amount: decimal.NewFromInt(100), DestinationID: "inv_1"}
	invoiceSvc := &checkoutTestInvoiceService{}
	paymentSvc := &checkoutTestPaymentService{payment: payment}

	err := s.HandleFlexPriceCheckoutPayment(context.Background(), session, nil, payment, nil, invoiceSvc, paymentSvc)

	require.NoError(t, err)
	require.Equal(t, 1, invoiceSvc.applyDiscountCalls)
	require.NotNil(t, paymentSvc.updatePaymentReq.Amount)
	require.True(t, decimal.NewFromInt(80).Equal(*paymentSvc.updatePaymentReq.Amount))
}

func TestHandleFlexPriceCheckoutPayment_AlreadySucceededAppliesDiscountAndSkipsReconciled(t *testing.T) {
	s := &PaymentService{logger: logger.NewNoopLogger()}
	session := &stripe.CheckoutSession{
		ID:             "cs_test_discount",
		AmountSubtotal: 10000,
		AmountTotal:    8000,
		Discounts:      []*stripe.CheckoutSessionDiscount{{Coupon: &stripe.Coupon{ID: "cp_1"}}},
	}
	// Payment was claimed as SUCCEEDED by payment_intent.succeeded earlier
	payment := &dto.PaymentResponse{
		ID:            "pay_1",
		Amount:        decimal.NewFromInt(80),
		PaymentStatus: types.PaymentStatusSucceeded,
		DestinationID: "inv_1",
	}
	invoiceSvc := &checkoutTestInvoiceService{
		paymentStatus: types.PaymentStatusPending,
		// payment pay_1 was already credited by payment_intent.succeeded
		metadata: types.Metadata{types.InvoiceMetadataKeyAppliedPaymentIDs: `["pay_1"]`},
	}
	paymentSvc := &checkoutTestPaymentService{payment: payment}

	err := s.HandleFlexPriceCheckoutPayment(context.Background(), session, nil, payment, nil, invoiceSvc, paymentSvc)

	require.NoError(t, err)
	// Discount must be applied even though payment was already succeeded
	require.Equal(t, 1, invoiceSvc.applyDiscountCalls, "discount from checkout session must be applied")
	// Reconcile calls must be 0 because pay_1 is already in applied_payment_ids
	require.Equal(t, 0, invoiceSvc.reconcileCalls, "payment amount must not be credited again")
}
