-- Correct existing classifications only when the stored client-visible body
-- contains a structured policy error/stop reason. Never infer from message text
-- or from earlier, recovered upstream attempts. No response/status is rewritten.
CREATE OR REPLACE FUNCTION pg_temp.ops_safety_policy_code(raw_body TEXT)
RETURNS TEXT LANGUAGE plpgsql AS $$
DECLARE
    payload TEXT;
    doc JSONB;
    code TEXT;
    choice JSONB;
    policy_codes TEXT[] := ARRAY[
        'cyber_policy', 'bio_policy', 'misalignment_policy_violation',
        'image_content_policy_violation', 'moderation_blocked', 'content_policy_violation',
        'content_policy', 'content_filter',
        'cyber_policy_session_blocked', 'session_blocked_by_cyber_policy'
    ];
BEGIN
    IF raw_body IS NULL OR raw_body = '' THEN
        RETURN NULL;
    END IF;
    -- Try the complete JSON body, then each SSE data payload (including
    -- multiline data). Malformed or truncated payloads are left unchanged.
    FOR payload IN
        SELECT raw_body
        UNION ALL
        SELECT string_agg(regexp_replace(line, '^data: ?', ''), E'\n' ORDER BY line_no)
        FROM regexp_split_to_table(replace(replace(raw_body, E'\r\n', E'\n'), E'\r', E'\n'), E'\n\n')
             WITH ORDINALITY AS frames(frame, frame_no)
        CROSS JOIN LATERAL regexp_split_to_table(frame, E'\n')
             WITH ORDINALITY AS lines(line, line_no)
        WHERE line LIKE 'data:%'
        GROUP BY frame_no
    LOOP
        BEGIN
            doc := payload::JSONB;
        EXCEPTION WHEN invalid_text_representation OR untranslatable_character THEN
            CONTINUE;
        END;
        FOREACH code IN ARRAY ARRAY[
            doc #>> '{error,code}', doc #>> '{error,type}',
            doc #>> '{response,error,code}', doc #>> '{response,error,type}',
            CASE WHEN doc ->> 'type' = 'error' THEN doc ->> 'code' END
        ] LOOP
            code := lower(btrim(code));
            IF code = ANY(policy_codes) THEN
                RETURN code;
            END IF;
        END LOOP;
        IF (doc #>> '{incomplete_details,reason}' = 'content_filter'
                AND (doc ->> 'status' = 'incomplete' OR doc ->> 'type' = 'response.incomplete'))
            OR (doc #>> '{response,incomplete_details,reason}' = 'content_filter'
                AND (doc #>> '{response,status}' = 'incomplete' OR doc ->> 'type' = 'response.incomplete')) THEN
            RETURN 'content_filter';
        END IF;
        IF jsonb_typeof(doc -> 'choices') = 'array' THEN
            FOR choice IN SELECT value FROM jsonb_array_elements(doc -> 'choices') LOOP
                IF choice ->> 'finish_reason' = 'content_filter' THEN
                    RETURN 'content_filter';
                END IF;
            END LOOP;
        END IF;
    END LOOP;
    RETURN NULL;
END;
$$;

WITH classified AS MATERIALIZED (
    SELECT id, pg_temp.ops_safety_policy_code(error_body) AS policy_code
    FROM ops_error_logs
    WHERE error_body ~ '(cyber_policy|bio_policy|misalignment_policy_violation|image_content_policy_violation|moderation_blocked|content_policy_violation|content_policy|content_filter)'
)
UPDATE ops_error_logs AS e
SET error_type = classified.policy_code,
    error_phase = 'request'
FROM classified
WHERE e.id = classified.id
  AND classified.policy_code IS NOT NULL
  AND (e.error_type IS DISTINCT FROM classified.policy_code OR e.error_phase IS DISTINCT FROM 'request');

DROP FUNCTION pg_temp.ops_safety_policy_code(TEXT);
