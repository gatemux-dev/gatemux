ALTER TABLE pricing ADD COLUMN tiers JSONB NOT NULL DEFAULT '{}';
ALTER TABLE pricing ADD CONSTRAINT pricing_tiers_object CHECK (jsonb_typeof(tiers) = 'object');
ALTER TABLE usage_log ADD COLUMN token_details JSONB;
ALTER TABLE usage_log ADD CONSTRAINT usage_token_details_object CHECK (token_details IS NULL OR jsonb_typeof(token_details) = 'object');
