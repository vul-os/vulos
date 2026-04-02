# AI

## Choosing a Harness

The system already has a custom multi-provider AI layer (Ollama, Claude, OpenAI) with streaming, chat history, embeddings, sandbox execution, and viewport rendering. The question is whether to adopt an external harness for orchestration, tool use, and agent workflows — or continue building our own.

### Current Stack

- **Provider layer**: custom Go service supporting Ollama (default), Claude, OpenAI, any OpenAI-compatible endpoint
- **Embeddings**: Ollama `nomic-embed-text` for semantic search / Recall context
- **Sandbox**: ephemeral Python execution for AI-generated apps
- **Missions**: multi-step task orchestration
- **Proactive agent**: system monitoring with AI-enhanced alerts

### Harness Options

| Framework | Language | License | Multi-provider | Tool use | Notes |
|-----------|----------|---------|---------------|----------|-------|
| **Vercel AI SDK** | TypeScript | Apache 2.0 | Yes | Yes | Streaming-native, React/web-first. Best fit for WebKit-based UI |
| **LiteLLM** | Python | MIT | Yes (100+ providers) | Yes | Unified API proxy. Ideal as provider abstraction layer |
| **Haystack** | Python | Apache 2.0 | Yes | Yes | Pipeline-based, composable. Good for structured backend workflows |
| **CrewAI** | Python | MIT | Yes (via LiteLLM) | Yes | Role-based multi-agent. Simpler than AutoGen |
| **LangChain** | Python/TS | MIT | Yes | Yes | Most mature ecosystem but heavy abstraction, often over-engineered |
| **AutoGen (ag2)** | Python | MIT | Yes | Yes | Multi-agent conversations. Good for complex task decomposition |
| **Anthropic Agent SDK** | Python | MIT | No (Claude only) | Yes | Thin, opinionated. Not suitable as sole backbone |
| **OpenAI Agents SDK** | Python | MIT | Limited | Yes | Handoffs, guardrails, tracing. Tied to OpenAI patterns |
| **Semantic Kernel** | C#/Python | MIT | Yes | Yes | Enterprise-grade. Heavy .NET heritage, awkward on Alpine |
| **LlamaIndex** | Python/TS | MIT | Yes | Yes | Strongest for RAG/indexing. Complementary, not a replacement |
| **DSPy** | Python | MIT | Yes | Yes | Programmatic prompt optimization. Research-grade |
| Factory.ai | — | Proprietary | — | — | Commercial SaaS, not an open framework. **Not suitable** |

### Recommendation

**Web/UI layer**: Vercel AI SDK — streaming-native, React-first, multi-provider. Natural fit for our WebKit-based UI.

**Provider abstraction**: LiteLLM — unified proxy for 100+ providers. Replaces our custom provider switching with a battle-tested layer.

**Backend orchestration**: Haystack or CrewAI for multi-step agent workflows (missions, proactive agent, system tasks).

This keeps the stack lightweight for Alpine/postmarketOS while giving us proper tool use, agent patterns, and provider flexibility without reinventing everything.

### Decision needed

- [ ] Evaluate: keep custom Go AI service vs adopt Vercel AI SDK + LiteLLM
- [ ] If adopting: LiteLLM as a sidecar service or embedded in Go backend?
- [ ] Agent orchestration: Haystack vs CrewAI vs keep custom missions system
- [ ] How much Python do we want in the stack (LiteLLM, Haystack, CrewAI are all Python)

---

## Desktop & Apps Must Lead to Chat

The AI chat (Portal, Cmd+K) should be the primary interface users are guided toward. Every surface should funnel into it.

### Entry points to chat

- [ ] **Desktop**: first-run experience opens chat, explains what AI can do
- [ ] **Dock/taskbar**: persistent AI icon, always visible, single tap to open
- [ ] **App empty states**: when an app has no content (empty files, no messages), suggest "Ask AI to help"
- [ ] **Error states**: when something fails, offer "Ask AI about this error"
- [ ] **Search**: global search (Cmd+K) is already the chat — keep this as the primary entry
- [ ] **Context menu**: right-click on files, text, apps → "Ask AI about this"
- [ ] **Notifications**: actionable notifications can deep-link into chat with context
- [ ] **Settings**: AI section prominent, not buried. Guide users to configure their provider

### Behavior

- Chat should feel like the OS's brain, not a separate app
- Context-aware: opening chat from within an app should pre-fill context about that app
- Persistent across sessions — conversation history already exists, surface it better
- Voice input already works — make it more discoverable (mic icon always visible in chat)

---

## AI Apps

AI-generated apps (viewport rendering + sandbox) are already functional. Users can create apps by describing them in chat and save them. Improvements needed:

### Current state

- AI generates HTML + optional Python backend via `<viewport>` tags
- Sandbox runs Python on ephemeral ports (9100-9199) with security filtering
- Apps can be saved to `~/.vulos/ai-apps/`
- Apps can be retrieved and relaunched

### Improvements needed

- [ ] **App quality**: better templates, more robust generated code, error recovery in viewport
- [ ] **Editing**: let users iterate on saved AI apps ("make the button bigger", "add a dark theme")
- [ ] **Versioning**: keep history of app iterations so users can roll back
- [ ] **App icons**: AI-generate an icon for each app (SVG via LLM or simple emoji mapping)
- [ ] **Categories**: auto-categorize AI apps (tool, game, productivity, etc.)
- [ ] **Persistence**: AI apps should survive reboot — ensure they appear in app launcher like normal apps
- [ ] **Performance**: sandbox startup is slow — pre-warm a Python process pool

---

## Public Apps

Users should be able to make their apps (both AI-generated and normal installed apps) accessible to others on the network or internet.

### Visibility levels

- **Private** (default): only accessible on this device
- **Local network**: accessible to peers on the same network (ties into peering system)
- **Public**: accessible to anyone with the URL

### Implementation

- [ ] Add `visibility` field to app manifest: `private | local | public`
- [ ] API endpoints to toggle visibility per app
- [ ] Settings UI for each app: toggle visibility with clear explanation of what each level means
- [ ] Reverse proxy / tunnel for public apps (Cloudflare Tunnel, ngrok, or custom)
- [ ] Authentication: public apps should support optional auth (password, link-based access)
- [ ] Rate limiting on public apps to prevent abuse

### Topbar warning

**When any app is public, a persistent warning must appear in the topbar.**

- [ ] Warning indicator in topbar (icon + text): "1 public app" / "3 public apps"
- [ ] Clicking the warning opens a list of all public apps with quick toggles to make them private
- [ ] Warning is **always visible** — cannot be dismissed while public apps exist
- [ ] Color-coded: yellow for local network, red for public internet
- [ ] On first time making an app public: confirmation dialog explaining the risks

### Security

- [ ] Sandboxed: public apps run in isolated containers, no access to user data
- [ ] Bandwidth monitoring: alert if a public app is consuming excessive resources
- [ ] Auto-disable: if the device goes on battery or mobile data, prompt to disable public apps
- [ ] Audit log: who accessed your public apps and when

---

## TODO Summary

1. [ ] Evaluate harness options (Vercel AI SDK + LiteLLM vs keep custom)
2. [ ] Add AI entry points across desktop and all apps
3. [ ] First-run experience that introduces AI chat
4. [ ] AI app editing and iteration workflow
5. [ ] AI app versioning and rollback
6. [ ] App visibility system (private / local / public)
7. [ ] Topbar warning for public apps (always visible, non-dismissable)
8. [ ] Public app security: sandboxing, rate limiting, audit log
9. [ ] AI app persistence across reboots in app launcher
