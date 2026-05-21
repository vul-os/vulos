# LINT-CORE-DEFERRED

Items that could not be fixed as pure lint-only changes (require behavior or architecture changes).

## react-hooks/exhaustive-deps warnings (in-scope files)

These are all **warnings** (not errors) and represent intentional dependency omissions.
Fixing them would require behavior analysis to ensure no infinite loops or stale closures.

| File | Line | Missing dep |
|------|------|-------------|
| src/builtin/files/FileManager.jsx | 325 | loadDir |
| src/builtin/files/FileManager.jsx | 490 | goUp |
| src/builtin/peering/call/useMeshCall.js | 569 | leave |
| src/builtin/peering/call/useSFUCall.js | 206 | signalSend |
| src/builtin/peering/call/useSFUCall.js | 275 | reconnectToSFU |
| src/builtin/peering/call/useSFUCall.js | 504 | leave |
| src/builtin/peering/call/useVideoCall.js | 262 | stopScreenShare |
| src/core/Portal.jsx | 371 | processAIResponse |
| src/core/ThemeProvider.jsx | 108,113 | tick (unnecessary dep) |
| src/providers/ShellProvider.jsx | 288 | state |
| src/providers/ShellProvider.jsx | 297 | allWindows not memoized |
| src/shell/Window.jsx | 85 | applySnap, snapZone |

## Out-of-scope files (belong to OSS-LINT-AUTH agent)

The following files have lint errors but are excluded from this agent's scope:
- `src/auth/AuthProvider.jsx` — react-refresh/only-export-components
- `src/auth/Setup.jsx` — no-empty, no-unused-vars, react-hooks/set-state-in-effect
