package types

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInvoiceBillingReason_TrialStart_Validate(t *testing.T) {
	err := InvoiceBillingReasonSubscriptionTrialStart.Validate()
	require.NoError(t, err, "SUBSCRIPTION_TRIAL_START must be a valid billing reason")
}

func TestInvoiceBillingReason_TrialStart_NotFirstOpenInvoiceReason(t *testing.T) {
	// Trial start invoices must NOT trigger subscription activation when paid.
	assert.False(t,
		InvoiceBillingReasonSubscriptionTrialStart.IsFirstSubscriptionOpenInvoiceReason(),
		"SUBSCRIPTION_TRIAL_START must not activate subscription on payment",
	)
}

func TestInvoiceBillingReason_TrialStart_StringValue(t *testing.T) {
	assert.Equal(t, "SUBSCRIPTION_TRIAL_START", string(InvoiceBillingReasonSubscriptionTrialStart))
}

func TestWithCollapsedInvoiceDisplayName(t *testing.T) {
	tests := []struct {
		name string
		md   Metadata
		in   string
		want string
	}{
		{name: "sets on nil map", md: nil, in: "Upgrade: Team → Starter", want: "Upgrade: Team → Starter"},
		{name: "preserves other keys", md: Metadata{"foo": "bar"}, in: "Quantity change", want: "Quantity change"},
		{name: "trims space", md: nil, in: "  Plan change  ", want: "Plan change"},
		{name: "ignores empty", md: Metadata{"foo": "bar"}, in: "   ", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := WithCollapsedInvoiceDisplayName(tt.md, tt.in)
			assert.Equal(t, tt.want, CollapsedInvoiceDisplayName(got))
			if tt.md != nil {
				assert.Equal(t, "bar", got["foo"])
			}
		})
	}
}

func TestIsPaymentAppliedToInvoice(t *testing.T) {
	tests := []struct {
		name      string
		md        Metadata
		paymentID string
		want      bool
	}{
		{name: "nil metadata", md: nil, paymentID: "pay_1", want: false},
		{name: "no ledger key", md: Metadata{"other": "x"}, paymentID: "pay_1", want: false},
		{name: "empty ledger value", md: Metadata{InvoiceMetadataKeyAppliedPaymentIDs: ""}, paymentID: "pay_1", want: false},
		{
			name:      "payment present",
			md:        Metadata{InvoiceMetadataKeyAppliedPaymentIDs: `["pay_1","pay_2"]`},
			paymentID: "pay_1",
			want:      true,
		},
		{
			name:      "payment absent from a populated ledger",
			md:        Metadata{InvoiceMetadataKeyAppliedPaymentIDs: `["pay_2"]`},
			paymentID: "pay_1",
			want:      false,
		},
		{
			// Unreadable is treated as "not applied": reconciling again may overstate
			// the invoice, but claiming a credit landed when we cannot tell is worse.
			name:      "malformed ledger json",
			md:        Metadata{InvoiceMetadataKeyAppliedPaymentIDs: `{"not":"a list"}`},
			paymentID: "pay_1",
			want:      false,
		},
		{
			name:      "empty payment id never matches",
			md:        Metadata{InvoiceMetadataKeyAppliedPaymentIDs: `[""]`},
			paymentID: "",
			want:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, IsPaymentAppliedToInvoice(tt.md, tt.paymentID))
		})
	}
}

func TestWithPaymentAppliedToInvoice(t *testing.T) {
	t.Run("records a payment on metadata that has no ledger", func(t *testing.T) {
		md, err := WithPaymentAppliedToInvoice(nil, "pay_1")
		require.NoError(t, err)
		assert.True(t, IsPaymentAppliedToInvoice(md, "pay_1"))
	})

	t.Run("appends without dropping existing entries", func(t *testing.T) {
		md, err := WithPaymentAppliedToInvoice(Metadata{InvoiceMetadataKeyAppliedPaymentIDs: `["pay_1"]`}, "pay_2")
		require.NoError(t, err)
		assert.True(t, IsPaymentAppliedToInvoice(md, "pay_1"))
		assert.True(t, IsPaymentAppliedToInvoice(md, "pay_2"))
	})

	t.Run("adding the same payment twice does not duplicate it", func(t *testing.T) {
		md, err := WithPaymentAppliedToInvoice(Metadata{InvoiceMetadataKeyAppliedPaymentIDs: `["pay_1"]`}, "pay_1")
		require.NoError(t, err)
		assert.Equal(t, `["pay_1"]`, md[InvoiceMetadataKeyAppliedPaymentIDs])
	})

	t.Run("preserves unrelated metadata", func(t *testing.T) {
		md, err := WithPaymentAppliedToInvoice(Metadata{"foo": "bar"}, "pay_1")
		require.NoError(t, err)
		assert.Equal(t, "bar", md["foo"])
	})

	t.Run("empty payment id is a no-op", func(t *testing.T) {
		md, err := WithPaymentAppliedToInvoice(Metadata{"foo": "bar"}, "")
		require.NoError(t, err)
		assert.False(t, HasAppliedPaymentIDs(md))
	})

	t.Run("malformed existing ledger is reported rather than overwritten", func(t *testing.T) {
		_, err := WithPaymentAppliedToInvoice(Metadata{InvoiceMetadataKeyAppliedPaymentIDs: `{"not":"a list"}`}, "pay_1")
		require.Error(t, err)
	})
}

func TestHasAppliedPaymentIDs(t *testing.T) {
	assert.False(t, HasAppliedPaymentIDs(nil))
	assert.False(t, HasAppliedPaymentIDs(Metadata{"other": "x"}))
	assert.False(t, HasAppliedPaymentIDs(Metadata{InvoiceMetadataKeyAppliedPaymentIDs: ""}))
	assert.True(t, HasAppliedPaymentIDs(Metadata{InvoiceMetadataKeyAppliedPaymentIDs: `[]`}))
	assert.True(t, HasAppliedPaymentIDs(Metadata{InvoiceMetadataKeyAppliedPaymentIDs: `["pay_1"]`}))
}
