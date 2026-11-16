ALTER TABLE payment_provider_instances ADD COLUMN IF NOT EXISTS recharge_policy JSONB;
ALTER TABLE payment_orders ADD COLUMN IF NOT EXISTS pricing_snapshot JSONB;

DO $$
DECLARE
    legacy_tiers JSONB := '[]'::jsonb;
    legacy_mode TEXT;
    legacy_notice TEXT;
BEGIN
    BEGIN
        SELECT COALESCE(NULLIF(value, ''), '[]')::jsonb INTO legacy_tiers
        FROM settings WHERE key = 'RECHARGE_BONUS_TIERS';
    EXCEPTION WHEN invalid_text_representation THEN
        legacy_tiers := '[]'::jsonb;
    END;
    IF legacy_tiers IS NULL OR jsonb_typeof(legacy_tiers) <> 'array' THEN
        legacy_tiers := '[]'::jsonb;
    END IF;
    SELECT value INTO legacy_mode FROM settings WHERE key = 'RECHARGE_BONUS_MODE';
    SELECT value INTO legacy_notice FROM settings WHERE key = 'RECHARGE_BONUS_NOTICE';
    UPDATE payment_provider_instances
    SET recharge_policy = jsonb_build_object(
        'mode', CASE WHEN legacy_mode = 'discount' THEN 'discount' ELSE 'bonus' END,
        'tiers', legacy_tiers,
        'notice', COALESCE(legacy_notice, '')
    )
    WHERE recharge_policy IS NULL;
END $$;

ALTER TABLE payment_provider_instances ALTER COLUMN recharge_policy SET DEFAULT '{}'::jsonb;
ALTER TABLE payment_provider_instances ALTER COLUMN recharge_policy SET NOT NULL;
