-- 0005: seed the admin-editable daily alert-email cap.
--
-- Edge-triggered alerting sends at most one email per crossing (alert) and one
-- per return-to-normal (recovered); this is a hard ceiling on total alert emails
-- per UTC day, editable from the admin panel (PUT /api/config).

INSERT INTO config (key, value) VALUES ('alert_max_emails_per_day', '6')
    ON CONFLICT (key) DO NOTHING;
