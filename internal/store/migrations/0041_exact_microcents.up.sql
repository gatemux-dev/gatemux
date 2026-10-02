-- Run with inference drained and one migration owner.
-- Exact costs in micro-cents (1/1,000,000 cent). NULL marks whole-cent history,
-- so no row is rewritten; new writes set both columns, cents rounded up.
LOCK TABLE budget_reservations, usage_log IN SHARE ROW EXCLUSIVE MODE;

ALTER TABLE usage_log ADD COLUMN cost_microcents BIGINT NULL,
    ADD CONSTRAINT usage_log_cost_microcents_exact CHECK (cost_microcents IS NULL OR
        (cost_microcents >= 0 AND cost_cents = cost_microcents / 1000000 + (cost_microcents % 1000000 <> 0)::int));

ALTER TABLE budget_reservations ADD COLUMN estimated_cost_microcents BIGINT NULL,
    ADD COLUMN settled_cost_microcents BIGINT NULL,
    ADD CONSTRAINT budget_reservations_estimated_microcents_exact CHECK (estimated_cost_microcents IS NULL OR
        (estimated_cost_microcents >= 0 AND estimated_cost_cents = estimated_cost_microcents / 1000000 + (estimated_cost_microcents % 1000000 <> 0)::int)),
    ADD CONSTRAINT budget_reservations_settled_microcents_exact CHECK (settled_cost_microcents IS NULL OR
        (settled_cost_microcents >= 0 AND settled_cost_cents = settled_cost_microcents / 1000000 + (settled_cost_microcents % 1000000 <> 0)::int));

-- One exact total column. A stale binary's cents read now fails, and admission
-- refuses, instead of reading a total nothing maintains any more.
ALTER TABLE budget_daily_totals ADD COLUMN cost_microcents BIGINT;
UPDATE budget_daily_totals SET cost_microcents = cost_cents * 1000000;
ALTER TABLE budget_daily_totals ALTER COLUMN cost_microcents SET NOT NULL,
    ADD CONSTRAINT budget_daily_totals_cost_microcents_check CHECK (cost_microcents >= 0),
    DROP COLUMN cost_cents;

-- Same functions as 0039; only the amounts are effective micro-cents.
CREATE OR REPLACE FUNCTION budget_apply_daily(team BIGINT, usr BIGINT, sa BIGINT, vk BIGINT,
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
            INSERT INTO budget_daily_totals(scope,subject_id,day,cost_microcents)
            VALUES(identity.scope,identity.subject_id,(stamp AT TIME ZONE 'UTC')::date,delta)
            ON CONFLICT (scope,subject_id,day) DO UPDATE
                SET cost_microcents=budget_daily_totals.cost_microcents+EXCLUDED.cost_microcents;
        ELSE
            UPDATE budget_daily_totals SET cost_microcents=cost_microcents+delta
            WHERE scope=identity.scope AND subject_id=identity.subject_id
                AND day=(stamp AT TIME ZONE 'UTC')::date;
            IF NOT FOUND THEN RAISE EXCEPTION 'missing budget daily total'; END IF;
        END IF;
    END LOOP;
END;
$$;

CREATE OR REPLACE FUNCTION budget_usage_daily_trigger() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    -- Serialize correlation with a reservation insert/delete for the same ID.
    -- The ordinary journaled path already has one owner; this also protects
    -- legacy writers without counting their usage and reservation twice.
    IF TG_OP <> 'INSERT' THEN
        PERFORM pg_advisory_xact_lock(hashtextextended('budget-charge:' || OLD.request_id,0));
        IF OLD.cost_cents <> 0 AND NOT EXISTS (SELECT 1 FROM budget_reservations
            WHERE request_id=OLD.request_id AND team_id=OLD.team_id) THEN
            PERFORM budget_apply_daily(OLD.team_id,OLD.user_id,OLD.service_account_id,
                OLD.key_id,OLD.customer_id,OLD.ts,-COALESCE(OLD.cost_microcents,OLD.cost_cents*1000000));
        END IF;
    END IF;
    IF TG_OP <> 'DELETE' THEN
        PERFORM pg_advisory_xact_lock(hashtextextended('budget-charge:' || NEW.request_id,0));
        IF NEW.cost_cents <> 0 AND NOT EXISTS (SELECT 1 FROM budget_reservations
            WHERE request_id=NEW.request_id AND team_id=NEW.team_id) THEN
            PERFORM budget_apply_daily(NEW.team_id,NEW.user_id,NEW.service_account_id,
                NEW.key_id,NEW.customer_id,NEW.ts,COALESCE(NEW.cost_microcents,NEW.cost_cents*1000000));
        END IF;
    END IF;
    RETURN NULL;
END;
$$;

CREATE OR REPLACE FUNCTION budget_reservation_daily_trigger() RETURNS TRIGGER LANGUAGE plpgsql AS $$
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
            charge := (CASE NEW.status WHEN 'reserved' THEN COALESCE(NEW.estimated_cost_microcents,NEW.estimated_cost_cents*1000000)
                WHEN 'settled' THEN COALESCE(NEW.settled_cost_microcents,NEW.settled_cost_cents*1000000) ELSE 0 END)
                    - (CASE OLD.status WHEN 'reserved' THEN COALESCE(OLD.estimated_cost_microcents,OLD.estimated_cost_cents*1000000)
                WHEN 'settled' THEN COALESCE(OLD.settled_cost_microcents,OLD.settled_cost_cents*1000000) ELSE 0 END);
            PERFORM budget_apply_daily(NEW.team_id,NEW.user_id,NEW.service_account_id,
                NEW.key_id,NEW.customer_id,NEW.created_at,charge);
            RETURN NULL;
        END IF;
    END IF;
    IF TG_OP <> 'INSERT' THEN
        PERFORM pg_advisory_xact_lock(hashtextextended('budget-charge:' || OLD.request_id,0));
        charge := CASE OLD.status WHEN 'reserved' THEN COALESCE(OLD.estimated_cost_microcents,OLD.estimated_cost_cents*1000000)
                WHEN 'settled' THEN COALESCE(OLD.settled_cost_microcents,OLD.settled_cost_cents*1000000) ELSE 0 END;
        PERFORM budget_apply_daily(OLD.team_id,OLD.user_id,OLD.service_account_id,
            OLD.key_id,OLD.customer_id,OLD.created_at,-charge);
        IF correlation_changed THEN
            FOR u IN SELECT * FROM usage_log WHERE request_id=OLD.request_id AND team_id=OLD.team_id AND cost_cents <> 0 LOOP
                PERFORM budget_apply_daily(u.team_id,u.user_id,u.service_account_id,u.key_id,u.customer_id,u.ts,COALESCE(u.cost_microcents,u.cost_cents*1000000));
            END LOOP;
        END IF;
    END IF;
    IF TG_OP <> 'DELETE' THEN
        PERFORM pg_advisory_xact_lock(hashtextextended('budget-charge:' || NEW.request_id,0));
        charge := CASE NEW.status WHEN 'reserved' THEN COALESCE(NEW.estimated_cost_microcents,NEW.estimated_cost_cents*1000000)
                WHEN 'settled' THEN COALESCE(NEW.settled_cost_microcents,NEW.settled_cost_cents*1000000) ELSE 0 END;
        PERFORM budget_apply_daily(NEW.team_id,NEW.user_id,NEW.service_account_id,
            NEW.key_id,NEW.customer_id,NEW.created_at,charge);
        IF correlation_changed THEN
            FOR u IN SELECT * FROM usage_log WHERE request_id=NEW.request_id AND team_id=NEW.team_id AND cost_cents <> 0 LOOP
                PERFORM budget_apply_daily(u.team_id,u.user_id,u.service_account_id,u.key_id,u.customer_id,u.ts,-COALESCE(u.cost_microcents,u.cost_cents*1000000));
            END LOOP;
        END IF;
    END IF;
    RETURN NULL;
END;
$$;
