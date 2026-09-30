package service

import (
	"github.com/flexprice/flexprice/internal/api/dto"
	"github.com/flexprice/flexprice/internal/domain/customer"
	"github.com/flexprice/flexprice/internal/domain/settings"
	"github.com/flexprice/flexprice/internal/types"
	"github.com/flexprice/flexprice/internal/utils"
	"github.com/samber/lo"
	"github.com/shopspring/decimal"
)

// seedCustomCurrencyConfigMultiFiat configures "mac" with factors for both the tenant default
// (usd, 1 mac = $0.10) and inr (1 mac = ₹8.30), so a custom-currency draft can be billed in a
// customer's non-default billing currency.
func (s *InvoiceServiceSuite) seedCustomCurrencyConfigMultiFiat() {
	cfg := types.CustomCurrencyConfig{
		CustomCurrencies: map[string]types.CustomCurrencyDefinition{
			"mac": {
				Name:   "MoEngage AI Credits",
				Symbol: "MAC",
				FiatConversionFactors: map[string]decimal.Decimal{
					"usd": decimal.NewFromFloat(0.1),
					"inr": decimal.NewFromFloat(8.3),
				},
			},
		},
		DefaultFiatCurrency: "usd",
	}
	s.NoError(cfg.Validate())
	value, err := utils.ToMap(cfg)
	s.NoError(err)
	s.NoError(s.GetStores().SettingsRepo.Create(s.GetContext(), &settings.Setting{
		ID:            types.GenerateUUIDWithPrefix(types.UUID_PREFIX_SETTING),
		Key:           types.SettingKeyCustomCurrencyConfig,
		Value:         value,
		EnvironmentID: types.GetEnvironmentID(s.GetContext()),
		BaseModel:     types.GetDefaultBaseModel(s.GetContext()),
	}))
}

func (s *InvoiceServiceSuite) seedCustomerWithBillingCurrency(id, billing string) *customer.Customer {
	cust := &customer.Customer{
		ID:              id,
		ExternalID:      "ext_" + id,
		Name:            "CC " + billing,
		Email:           id + "@example.com",
		BillingCurrency: lo.ToPtr(billing),
		BaseModel:       types.GetDefaultBaseModel(s.GetContext()),
	}
	s.NoError(s.GetStores().CustomerRepo.Create(s.GetContext(), cust))
	return cust
}

// A custom-currency draft for a customer with a billing currency is denominated directly in that
// billing currency via the custom factor — not the tenant default — so it needs no later FX (§5.5).
func (s *InvoiceServiceSuite) TestCreateDraftInvoice_CustomCurrencyUsesCustomerBillingCurrency() {
	s.seedCustomCurrencyConfigMultiFiat()
	cust := s.seedCustomerWithBillingCurrency("cust_cc_inr", "inr")

	resp, err := s.service.CreateEmptyDraftInvoice(s.GetContext(), dto.CreateDraftInvoiceRequest{
		CustomerID:  cust.ID,
		InvoiceType: types.InvoiceTypeOneOff,
		Currency:    "mac",
	})
	s.NoError(err)
	s.Equal("inr", resp.Currency, "custom-currency draft must bill in the customer's billing currency, not the tenant default")
	s.Require().NotNil(resp.CustomCurrency)
	s.Equal("mac", resp.CustomCurrency.Code)
	s.True(resp.CustomCurrency.Rate.Equal(decimal.NewFromFloat(8.3)),
		"rate must be the mac->inr factor, got %s", resp.CustomCurrency.Rate)
	s.Nil(resp.FxConversion, "a custom-currency invoice converts once (custom->fiat); FX never applies")
}

// When the customer's billing currency has no factor for the custom code, the draft falls back to
// the tenant default fiat (today's behaviour). The §8.2/§8.3 guardrails prevent this for
// subscriptions and wallets; a stray one-off is caught by the finalize guard.
func (s *InvoiceServiceSuite) TestCreateDraftInvoice_CustomCurrencyNoFactorFallsBackToDefault() {
	s.seedCustomCurrencyConfig() // mac -> usd only
	cust := s.seedCustomerWithBillingCurrency("cust_cc_nofactor", "inr")

	resp, err := s.service.CreateEmptyDraftInvoice(s.GetContext(), dto.CreateDraftInvoiceRequest{
		CustomerID:  cust.ID,
		InvoiceType: types.InvoiceTypeOneOff,
		Currency:    "mac",
	})
	s.NoError(err)
	s.Equal("usd", resp.Currency, "no mac->inr factor: fall back to the tenant default fiat")
	s.Require().NotNil(resp.CustomCurrency)
	s.True(resp.CustomCurrency.Rate.Equal(decimal.NewFromFloat(0.1)), "rate is the default-fiat factor, got %s", resp.CustomCurrency.Rate)
}

// A custom-currency draft for a customer whose billing currency IS the tenant default is unchanged.
func (s *InvoiceServiceSuite) TestCreateDraftInvoice_CustomCurrencyBillingMatchesDefault() {
	s.seedCustomCurrencyConfigMultiFiat()
	cust := s.seedCustomerWithBillingCurrency("cust_cc_usd", "usd")

	resp, err := s.service.CreateEmptyDraftInvoice(s.GetContext(), dto.CreateDraftInvoiceRequest{
		CustomerID:  cust.ID,
		InvoiceType: types.InvoiceTypeOneOff,
		Currency:    "mac",
	})
	s.NoError(err)
	s.Equal("usd", resp.Currency)
	s.Require().NotNil(resp.CustomCurrency)
	s.True(resp.CustomCurrency.Rate.Equal(decimal.NewFromFloat(0.1)), "rate is the mac->usd factor, got %s", resp.CustomCurrency.Rate)
}
