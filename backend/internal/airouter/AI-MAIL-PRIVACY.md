# AI Mail Privacy

## What is processed

The four mail AI endpoints (smart-compose, summarize, reply-suggestions, extract-actions) send the thread content you supply in the request body to a language model for processing. No other data is included.

## Where it goes

**BYO mode (user-supplied API key)**: If you have configured a BYO provider (your own OpenAI-compatible endpoint and API key) in Settings > AI > Provider, ALL requests — including mail AI — route exclusively to YOUR endpoint. Mail content never leaves your infrastructure or touches Vulos LLM.

**Cloud mode (Vulos relay)**: Requests are forwarded to the Vulos cloud AI proxy (`VULOS_AI_PROXY_URL`), authenticated with your OS device certificate. The proxy forwards to the configured upstream model. The proxy operator (Vulos) may log aggregated usage metrics but does not store message content.

## What is logged

Regardless of mode, the airouter logs only:

- Token count (prompt + completion)
- Model slug used
- Endpoint name (e.g. "mail/summarize")
- Timestamp

**Mail content is never written to any log.**

## Audit trail

The enable/disable toggle (`POST /api/airouter/mail/enable` / `POST /api/airouter/mail/disable`) is recorded in the airouter audit log with the account ID and timestamp, so account owners can see when the feature was toggled.

## Opt-in

Mail AI is **disabled by default** (`accounts.ai_mail_enabled = 0`). You must explicitly enable it in Settings > Privacy > AI features. You can disable it at any time and the feature gate takes effect immediately.

## Data minimisation

- Thread content is transmitted only for the duration of the LLM call and is not cached by the router.
- Summarize, reply-suggestions, and extract-actions accept up to 16 000 tokens of thread content; smart-compose accepts up to 4 000 tokens of context.
- You control what you send — truncating the thread before calling the API is your responsibility.
