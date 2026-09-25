-- Roll back only after draining all newer replicas. This removes recovery state.
DROP TABLE inference_journal;
ALTER TABLE usage_log DROP COLUMN accounting_id, DROP COLUMN accounting_state;
