# Vulos OS – Service Level Objectives

| # | Surface | Target | Measurement | Error budget (99.9% / month) | Rollback trigger |
|---|---------|--------|-------------|------------------------------|------------------|
| 1 | **App launch p95** | < 3 s from cold (WebSocket ready) | Histogram from `vulos_request_duration_seconds`, filter op=`app.launch` | 43.2 min/month | p95 > 5 s for 5 consecutive minutes |
| 2 | **Device-pair API p99** | < 500 ms | Same histogram, op=`device.pair` | 43.2 min/month | p99 > 1 s sustained 3 min |
| 3 | **API error rate** | < 0.5% of requests | `vulos_error_count_total / vulos_request_count_total` over 5 min | 26 min/month of >0.5% | Rate > 2% for 2 min → halt deploy + page oncall |
| 4 | **Backend availability** | 99.5% | HTTP `/health` success ratio from synthetic probe (30 s interval) | 3.6 h/month | 3 consecutive failures → restart + alert |
| 5 | **Auth token issuance p95** | < 200 ms | Histogram, op=`auth.login` | 43.2 min/month | p95 > 400 ms for 5 min |
| 6 | **Sync queue drain** | Queue depth < 50 items within 60 s of burst | `vulos_queue_depth` gauge | N/A (advisory) | Depth > 200 for > 2 min → alert; > 500 → halt new deploys |

## Notes

- **Error budget**: 99.9% availability = 43.2 min downtime / 28-day month.
- **Rollback trigger** means: the deploy pipeline should pause and alert oncall; it does not automatically revert unless the CI/CD gate is configured to do so.
- These SLOs apply to the self-hosted OSS build running on recommended hardware (Raspberry Pi 5 or equivalent x86_64). Cloud-hosted SLOs are in `vulos-cloud/SLOs.md`.
