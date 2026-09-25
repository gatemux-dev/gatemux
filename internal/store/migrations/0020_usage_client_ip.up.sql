-- Capture client IP on each /v1 row so the Caller card can show where the
-- request came from. INET keeps the type honest (IPv4 + IPv6) and lets us
-- index/aggregate later if we want per-IP analytics.
ALTER TABLE usage_log
    ADD COLUMN client_ip INET;
