-- Run with inference drained and one migration owner. Backfill and triggers
-- become visible together: never serve partially initialized budget counters.
LOCK TABLE budget_reservations, usage_log IN SHARE ROW EXCLUSIVE MODE;

CREATE TABLE budget_daily_totals (
    scope TEXT NOT NULL CHECK (scope IN ('team','user','service_account','key','customer')),
    subject_id BIGINT NOT NULL,
    day DATE NOT NULL,
    cost_cents BIGINT NOT NULL CHECK (cost_cents >= 0),
    PRIMARY KEY (scope, subject_id, day)
);

-- Normally no usage exists when its reservation is inserted. Index the legacy
-- correlation check, including repeated non-journaled request IDs, explicitly.
CREATE INDEX usage_log_budget_request ON usage_log(request_id, team_id) WHERE cost_cents <> 0;

INSERT INTO budget_daily_totals(scope,subject_id,day,cost_cents)
SELECT identity.scope,identity.subject_id,(charges.ts AT TIME ZONE 'UTC')::date,SUM(charges.cost)
FROM (
    SELECT team_id,user_id,service_account_id,key_id,customer_id,created_at AS ts,
           CASE WHEN status='reserved' THEN estimated_cost_cents ELSE settled_cost_cents END AS cost
    FROM budget_reservations WHERE status IN ('reserved','settled')
    UNION ALL
    SELECT u.team_id,u.user_id,u.service_account_id,u.key_id,u.customer_id,u.ts,u.cost_cents
    FROM usage_log u WHERE NOT EXISTS (
        SELECT 1 FROM budget_reservations b WHERE b.request_id=u.request_id AND b.team_id=u.team_id
    )
) charges
CROSS JOIN LATERAL (VALUES ('team',team_id),('user',user_id),
    ('service_account',service_account_id),('key',key_id),('customer',customer_id)) identity(scope,subject_id)
WHERE identity.subject_id IS NOT NULL AND charges.cost <> 0
GROUP BY identity.scope,identity.subject_id,(charges.ts AT TIME ZONE 'UTC')::date;

CREATE FUNCTION budget_apply_daily(team BIGINT, usr BIGINT, sa BIGINT, vk BIGINT,
                                  customer BIGINT, stamp TIMESTAMPTZ, delta BIGINT)
RETURNS VOID LANGUAGE plpgsql AS $$
DECLARE identity RECORD;
BEGIN
    IF delta = 0 THEN RETURN; END IF;
    -- Same order for all normal writes, at most five rows. No request-indexed
    -- in-memory state or growing billing-period scan in admission.
    FOR identity IN SELECT scope,subject_id FROM (VALUES ('team',team),('user',usr),
        ('service_account',sa),('key',vk),('customer',customer)) v(scope,subject_id)
        WHERE subject_id IS NOT NULL ORDER BY scope,subject_id
    LOOP
        IF delta > 0 THEN
            INSERT INTO budget_daily_totals(scope,subject_id,day,cost_cents)
            VALUES(identity.scope,identity.subject_id,(stamp AT TIME ZONE 'UTC')::date,delta)
            ON CONFLICT (scope,subject_id,day) DO UPDATE
                SET cost_cents=budget_daily_totals.cost_cents+EXCLUDED.cost_cents;
        ELSE
            UPDATE budget_daily_totals SET cost_cents=cost_cents+delta
            WHERE scope=identity.scope AND subject_id=identity.subject_id
                AND day=(stamp AT TIME ZONE 'UTC')::date;
            IF NOT FOUND THEN RAISE EXCEPTION 'missing budget daily total'; END IF;
        END IF;
    END LOOP;
END;
$$;

CREATE FUNCTION budget_usage_daily_trigger() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    -- Serialize correlation with a reservation insert/delete for the same ID.
    -- The ordinary journaled path already has one owner; this also protects
    -- legacy writers without counting their usage and reservation twice.
    IF TG_OP <> 'INSERT' THEN
        PERFORM pg_advisory_xact_lock(hashtextextended('budget-charge:' || OLD.request_id,0));
        IF OLD.cost_cents <> 0 AND NOT EXISTS (SELECT 1 FROM budget_reservations
            WHERE request_id=OLD.request_id AND team_id=OLD.team_id) THEN
            PERFORM budget_apply_daily(OLD.team_id,OLD.user_id,OLD.service_account_id,
                OLD.key_id,OLD.customer_id,OLD.ts,-OLD.cost_cents);
        END IF;
    END IF;
    IF TG_OP <> 'DELETE' THEN
        PERFORM pg_advisory_xact_lock(hashtextextended('budget-charge:' || NEW.request_id,0));
        IF NEW.cost_cents <> 0 AND NOT EXISTS (SELECT 1 FROM budget_reservations
            WHERE request_id=NEW.request_id AND team_id=NEW.team_id) THEN
            PERFORM budget_apply_daily(NEW.team_id,NEW.user_id,NEW.service_account_id,
                NEW.key_id,NEW.customer_id,NEW.ts,NEW.cost_cents);
        END IF;
    END IF;
    RETURN NULL;
END;
$$;

CREATE FUNCTION budget_reservation_daily_trigger() RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE charge BIGINT; u RECORD; correlation_changed BOOLEAN;
BEGIN
    correlation_changed := TG_OP <> 'UPDATE';
    IF TG_OP = 'UPDATE' THEN
        correlation_changed := ROW(OLD.request_id,OLD.team_id) IS DISTINCT FROM ROW(NEW.request_id,NEW.team_id);
        IF NOT correlation_changed AND ROW(OLD.user_id,OLD.service_account_id,OLD.key_id,OLD.customer_id,
            (OLD.created_at AT TIME ZONE 'UTC')::date) IS NOT DISTINCT FROM
            ROW(NEW.user_id,NEW.service_account_id,NEW.key_id,NEW.customer_id,(NEW.created_at AT TIME ZONE 'UTC')::date) THEN
            -- Normal settlement changes only the amount. Apply one delta, not
            -- subtract/add pairs; holds the same bounded scope rows atomically.
            PERFORM pg_advisory_xact_lock(hashtextextended('budget-charge:' || NEW.request_id,0));
            charge := (CASE NEW.status WHEN 'reserved' THEN NEW.estimated_cost_cents WHEN 'settled' THEN NEW.settled_cost_cents ELSE 0 END)
                    - (CASE OLD.status WHEN 'reserved' THEN OLD.estimated_cost_cents WHEN 'settled' THEN OLD.settled_cost_cents ELSE 0 END);
            PERFORM budget_apply_daily(NEW.team_id,NEW.user_id,NEW.service_account_id,
                NEW.key_id,NEW.customer_id,NEW.created_at,charge);
            RETURN NULL;
        END IF;
    END IF;
    IF TG_OP <> 'INSERT' THEN
        PERFORM pg_advisory_xact_lock(hashtextextended('budget-charge:' || OLD.request_id,0));
        charge := CASE OLD.status WHEN 'reserved' THEN OLD.estimated_cost_cents WHEN 'settled' THEN OLD.settled_cost_cents ELSE 0 END;
        PERFORM budget_apply_daily(OLD.team_id,OLD.user_id,OLD.service_account_id,
            OLD.key_id,OLD.customer_id,OLD.created_at,-charge);
        IF correlation_changed THEN
            FOR u IN SELECT * FROM usage_log WHERE request_id=OLD.request_id AND team_id=OLD.team_id AND cost_cents <> 0 LOOP
                PERFORM budget_apply_daily(u.team_id,u.user_id,u.service_account_id,u.key_id,u.customer_id,u.ts,u.cost_cents);
            END LOOP;
        END IF;
    END IF;
    IF TG_OP <> 'DELETE' THEN
        PERFORM pg_advisory_xact_lock(hashtextextended('budget-charge:' || NEW.request_id,0));
        charge := CASE NEW.status WHEN 'reserved' THEN NEW.estimated_cost_cents WHEN 'settled' THEN NEW.settled_cost_cents ELSE 0 END;
        PERFORM budget_apply_daily(NEW.team_id,NEW.user_id,NEW.service_account_id,
            NEW.key_id,NEW.customer_id,NEW.created_at,charge);
        IF correlation_changed THEN
            FOR u IN SELECT * FROM usage_log WHERE request_id=NEW.request_id AND team_id=NEW.team_id AND cost_cents <> 0 LOOP
                PERFORM budget_apply_daily(u.team_id,u.user_id,u.service_account_id,u.key_id,u.customer_id,u.ts,-u.cost_cents);
            END LOOP;
        END IF;
    END IF;
    RETURN NULL;
END;
$$;

CREATE TRIGGER budget_usage_daily AFTER INSERT OR UPDATE OR DELETE ON usage_log
    FOR EACH ROW EXECUTE FUNCTION budget_usage_daily_trigger();
CREATE TRIGGER budget_reservation_daily AFTER INSERT OR UPDATE OR DELETE ON budget_reservations
    FOR EACH ROW EXECUTE FUNCTION budget_reservation_daily_trigger();

-- TRUNCATE bypasses row triggers; prevent silently corrupting admission totals.
-- Operators may delete audited rows transactionally or rebuild offline instead.
CREATE FUNCTION budget_reject_truncate() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'budget accounting tables cannot be truncated; use transactional DELETE or an offline rebuild';
END;
$$;
CREATE TRIGGER budget_usage_no_truncate BEFORE TRUNCATE ON usage_log
    EXECUTE FUNCTION budget_reject_truncate();
CREATE TRIGGER budget_reservation_no_truncate BEFORE TRUNCATE ON budget_reservations
    EXECUTE FUNCTION budget_reject_truncate();
CREATE TRIGGER budget_totals_no_truncate BEFORE TRUNCATE ON budget_daily_totals
    EXECUTE FUNCTION budget_reject_truncate();
