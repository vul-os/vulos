# Mail AI Endpoint Pricing

All mail AI endpoints route through the standard airouter billing path.
Pricing is approximate and depends on thread length and model.

| Endpoint                | Typical tokens | Est. cost (Pro tier) | Est. debit (over-quota) |
|-------------------------|---------------|----------------------|------------------------|
| smart-compose           | ~200           | inclusive            | ~R0.02 per call        |
| summarize               | ~500           | inclusive            | ~R0.05 per call        |
| reply-suggestions       | ~600           | inclusive            | ~R0.06 per call        |
| extract-actions         | ~400           | inclusive            | ~R0.04 per call        |

- **Pro tier**: calls within the monthly token allowance are charged at a flat rate included in the subscription.
- **Over-quota / Free tier**: each call debits the account wallet at the per-token rate for the active model.
- Token counts are approximate; actual counts depend on thread length and model tokenization.
- Pricing in ZAR; USD display uses the live exchange-rate table in the billing service.

## BYO model

If the user has configured a BYO (Bring-Your-Own) API key and endpoint in airouter settings, all mail AI calls route to **their endpoint** — mail content never touches Vulos LLM infrastructure. No wallet debit occurs for BYO calls.
