# Vulos Office – Service Level Objectives

| # | Surface | Target | Measurement | Error budget (99.9% / month) | Rollback trigger |
|---|---------|--------|-------------|------------------------------|------------------|
| 1 | **WebSocket signaling p95** | < 100 ms for CRDT op round-trip | Trace span `crdt.sync` | 43.2 min/month | p95 > 200 ms for 5 min → halt deploy |
| 2 | **File API p95** | < 300 ms (GET /api/files/:id) | `vulos_office_request_duration_seconds` p95 | 43.2 min/month | p95 > 600 ms for 5 min → alert |
| 3 | **Signing workflow p99** | < 2 s from POST /sign/:envelope/send | Trace span `signing.send` | 43.2 min/month | p99 > 5 s → alert |
| 4 | **API error rate** | < 0.5% | `vulos_office_error_count_total / vulos_office_request_count_total` | 26 min/month | Rate > 2% for 2 min → halt deploy + alert |
| 5 | **Availability** | 99.5% | HTTP `/api/auth/status` probe every 60 s | 3.6 h/month | 3 consecutive failures → restart + alert |
| 6 | **CRDT merge time** | < 50 ms for a 1 000-op document | Trace span `crdt.merge` | Advisory | Merge > 200 ms p95 → alert; investigate before next deploy |

## Notes

- WebSocket signaling SLO covers the server-side fan-out path; client RTT is excluded.
- Signing SLO covers server-side envelope creation + email dispatch initiation.
- CRDT merge is advisory; excessive merge time indicates a modeling issue requiring investigation, not rollback.
