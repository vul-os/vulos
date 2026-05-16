# NOTIFY API FREEZE CONTRACT (authoritative — do not deviate)

This file is the single source of truth for the `notify` package API. Every
NOTIF-* agent MUST build against EXACTLY this. The recurring defer cause
(D70: "notify.go API lineage divergence", 3 failed rescues) was workers
redefining the struct / Send family from a stale base. That is now forbidden.

## FROZEN — existing public API (verbatim from current main, DO NOT MODIFY)

```go
type Level string
const ( LevelInfo Level="info"; LevelWarning Level="warning"; LevelUrgent Level="urgent" )

type Type string
const ( TypePresence Type="presence"; TypeEvent Type="event"; TypeCall Type="call"; TypeAlert Type="alert"; TypeAction Type="action" )

type Priority string
const ( PriorityLow Priority="low"; PriorityNormal Priority="normal"; PriorityHigh Priority="high"; PriorityCritical Priority="critical" )

type Notification struct {
	ID, Title string;  Body any;  Level Level;  Source string;  Action string
	Read bool;  CreatedAt time.Time
	Type Type;  Subtype string;  Priority Priority;  TTL int
}

type Service struct {            // ⚠ NOTIF-02 MAY add fields; nobody else may
	mu      sync.RWMutex
	history []Notification
	clients map[*websocket.Conn]bool
	maxHist int
}

func New() *Service
func (s *Service) SendNotification(n Notification) *Notification
func (s *Service) SendWithAction(title, body string, level Level, source, action string) *Notification
func (s *Service) Send(title, body string, level Level, source string) *Notification
func (s *Service) List(limit int) []Notification
func (s *Service) MarkRead(id string)
func (s *Service) MarkAllRead()
func (s *Service) UnreadCount() int
func (s *Service) Clear()
func (s *Service) NotifyOnConflict(path string) *Notification
func (s *Service) Handler() http.HandlerFunc
```

**RULE: nobody changes any signature above. All work is ADDITIVE.**

## Ownership split (so 4 agents never collide)

- **NOTIF-02 (keystone)** OWNS all `notify.go` Service-struct edits. It MAY add
  these fields to `Service` (and only these): `store *Store`, `dnd *dndState`.
  It adds the `Store` type + persistence in a NEW file `notify_store.go`. It
  adds `SetStore(*Store)`, and makes `SendNotification` ADDITIVELY call
  `s.store.Append(n)` when `s.store != nil` (in-memory path untouched, nil-safe).
  It also adds `(s *Service) DND() *dndState` accessor + the `dndState` type
  in NEW file `notify_dnd_state.go` — just the state holder + thread-safe
  get/set, NO http. Persistence path: `~/.vulos/db/notifications.json` (0600,
  atomic temp+rename), schema `{"version":1,"notifications":[...],"saved_at":""}`.

- **NOTIF-05+06 (Sonnet)** adds ONLY new files `notify_dnd.go` (DND modes:
  off/priority/total + schedule windows; suppression decision method
  `(s *Service) Suppressed(n *Notification) bool` that reads `s.DND()`) and
  `notify_action.go` (inline-action notifications: builds TypeAction
  notifications + an action-dispatch registry). Plus a NEW
  `backend/cmd/server/routes_notify.go` exporting
  `registerNotifyExtRoutes(mux, notifySvc, home)` with the DND + action
  endpoints. ONE wire line in main.go. ZERO edits to notify.go itself —
  it only calls `s.DND()` / `s.SetStore`-adjacent accessors NOTIF-02 exposes.
  If NOTIF-02 hasn't merged yet, code defensively against the contract
  (these accessors are guaranteed by this document).

- **NOTIF-04 (Sonnet)** is PURE FRONTEND: new `src/shell/NotificationCenter.jsx`
  (pull-down panel, history grouping by day/source, mark-read, DND toggle UI
  hitting the NOTIF-05 endpoints by path) + minimal additive mount in
  `src/shell/SystemPulse.jsx` (1 import + 1 element, like PublicAppsWarning).
  Zero backend. Consumes `GET /api/notifications`, `POST /api/notifications/read`,
  and the NOTIF-05 DND endpoints by URL contract below.

- **CLUSTER-02 (Opus)** is fully independent (auth package, not notify).

## Endpoint URL contract (NOTIF-05/06 implement; NOTIF-04 consumes)

- `GET  /api/notifications/dnd`            → `{mode, until, schedule:[{start,end,days}]}`
- `POST /api/notifications/dnd`            → set `{mode:"off|priority|total", until?, schedule?}`
- `POST /api/notifications/{id}/action`    → `{action_id}` dispatch an inline action
- (existing, already served by Service.Handler(): list/read/clear/unread)

Anything not specified here: choose the simplest additive option and document it
in the commit message. Never touch another agent's owned file set.
