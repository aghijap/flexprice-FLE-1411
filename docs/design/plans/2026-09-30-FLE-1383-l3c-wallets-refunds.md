# L3-C — Wallets, refunds & custom currency — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL at execution: superpowers:executing-plans, TDD each step. Scoping plan — fresh-explore the wallet/void/credit-note/custom-currency paths before implementing.

**Goal:** Make the wallet-adjacent flows correct for converted invoices: top-ups convert atomically, void returns the full funded value to the customer's **charge-currency** wallet at the frozen rate, credit-note prepaid-wallet refunds do the same, and custom-currency subscriptions bill directly in the billing currency via their factor (no double conversion).

**Spec:** ERD §5.5 (custom currency), §6.2 (top-up), §6.3 (void), §7 (credit notes).

**Depends on:** L3-A (`fx_conversion`, `ConvertInvoice`). Stacked on `feat/FLE-1383-l3b-checkout` (or on L3-A if B isn't merged — rebase as needed).

## Global Constraints

- **Wallets are never converted** except the one bounded exception: a **void/credit-note refund reverses its invoice at the invoice's own frozen rate** to land in the charge-currency wallet (§6, §6.3).
- Never a live rate for any of these — always the invoice's frozen `fx_conversion.rate`.
- Custom currency converts **once** (custom → fiat) at draft creation; FX never applies to a custom code.

## Review Focus

- **Void of a converted invoice** returns `funded = amount_paid + total_prepaid_credits_applied − refunded_amount` to the **charge-currency** prepaid wallet: cash leg `÷ frozen_rate`, credits leg from `fx_conversion.source.total_prepaid_credits_applied` (exact charge amount). `refunded_amount` stays the billing-currency (INR) field; the wallet credit is charge currency (verified funded formula `invoice.go:1444`).
- **Postpaid-paid converted invoice** (by design, #8): the INR cash it paid comes back as charge-currency prepaid credit — void already always refunds to a prepaid wallet, never the paying method. Confirm/document.
- **Top-up atomicity (#2):** conversion must run **inside** `handlePurchasedCreditInvoicedTransaction`'s `DB.WithTx` (`wallet.go:961`, invoice created at `:1202`), so a missing rate rolls back the pending wallet transaction. A stranded pending tx blocks the wallet's auto-top-up indefinitely (`wallet.go:4123`).
- **Credit-note `PREPAID_WALLET`** on a converted invoice → charge-currency wallet at frozen rate (today `settleToWallet` uses the invoice currency); `BACK_TO_SOURCE` → gateway INR, no rate.
- **Custom currency:** reject a custom-currency subscription/wallet whose custom code has no factor for the billing currency (§8.2/§8.3); when it has one, the draft is built directly in the billing currency (only code change: use the customer billing currency as the fiat target instead of the tenant default).

## File Structure

- `internal/ee/service/invoice.go` — `VoidInvoice`: split the funded refund by leg + currency (cash `÷ frozen`, credits from `fx_conversion.source`) into the charge-currency prepaid wallet.
- `internal/ee/service/refund.go` — route a converted-invoice `PREPAID_WALLET` refund to the charge-currency wallet at the frozen rate; keep `BACK_TO_SOURCE` in INR.
- `internal/ee/service/wallet.go` — top-up conversion inside the existing tx; (optional) a stale-pending-topup sweep.
- Draft-creation path (`CreateEmptyDraftInvoice`/subscription draft) — custom-currency fiat target = customer billing currency (§5.5).

## Tasks

### Task 1 — Custom-currency drafts (§5.5)
When the customer has a billing currency, build the custom-currency draft directly in that billing currency via the custom factor; reject subscriptions/wallets whose custom code lacks a factor for the billing currency. Tests: `mac→inr` factor present (draft in INR, custom rate frozen, no `fx_conversion`); factor absent (create/billing-currency-change rejected).

### Task 2 — Top-up conversion, atomic (§6.2, #2)
Convert the top-up invoice inside `handlePurchasedCreditInvoicedTransaction`'s tx; wallet credited in its own currency from the pending tx after payment. Missing rate ⇒ whole tx rolls back (no orphan). Test the rollback + the happy path.

### Task 3 — Void refund split (§6.3)
Modify `VoidInvoice` for converted invoices: return the credits leg (charge currency, from `fx_conversion.source`) + the cash leg (`(amount_paid − refunded_amount) ÷ frozen_rate`) to the charge-currency prepaid wallet; `refunded_amount` stays INR. Non-converted invoices unchanged. Tests: prepaid-only, cash-only, mixed, postpaid-paid, partial-prior-refund.

### Task 4 — Credit-note prepaid-wallet refund (§7)
`PREPAID_WALLET` refund on a converted invoice → charge-currency wallet at frozen rate; `BACK_TO_SOURCE` → gateway INR. Tests both targets + gateway-failure fallback.

### Task 5 — (Optional) stale-pending-topup sweep
A scheduled job that voids stale finalized-unpaid top-up invoices and releases their pending credits (pre-existing gap surfaced by #2). Flag for product before building.

## Verification

- Unit tests per task green; `make lint-ci`.
- **Local stack (integration) via the `flexprice-local-stack` skill** (`.agents/skills/flexprice-local-stack/SKILL.md`) — bring the stack up on the **existing named volumes**; **never** `docker compose down -v`:
  - `make app-up` → `make migrate-local` → `make app-rebuild-backend` (rebuild the Go image). To exercise **this branch's** code, build from the worktree under the `flexprice` project: `docker compose -p flexprice -f docker-compose.yml build flexprice-build` then `docker compose -p flexprice -f docker-compose.yml up -d flexprice-api flexprice-consumer flexprice-worker`.
  - Auth: `-H "x-api-key: sk_local_flexprice_test_key" -H "x-environment-id: 00000000-0000-0000-0000-000000000000"`.
  - Flow: converted invoice paid by prepaid+cash → void → assert USD wallet credited $X and INR cash leg converted at frozen rate; a `mac→inr` custom subscription bills in INR with the custom rate frozen (no `fx_conversion`).
