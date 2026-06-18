-- API Key 限额收敛 v1 (NEUTREE-GENERAL-9): 把 quota + access 收敛为 API Key 自身
-- 的一个扩展字段 spec.limits（单一对象），保证创建/编辑的一致性与原子性。
--
-- limits JSONB 形状（缺省/缺字段=不限）：
--   {
--     "token_quota": { "limit": <bigint>, "period": "daily|weekly|monthly|yearly" },
--     "rps": <int>, "rpm": <int>, "concurrency": <int>,
--     "allowed_models": ["model-a", ...],   -- 空数组/缺省=不限
--     "disabled": <bool>
--   }
--
-- 边界：合并的是「配置」。用量/剩余仍由不可变流水 api_daily_usage 聚合得出，
-- 不写进 spec（spec.limits 只存配置）。仅 api_key 维度。

-- 1) 扩 api_key_spec（沿用 012 的 ALTER TYPE ADD ATTRIBUTE 手法）
ALTER TYPE api.api_key_spec ADD ATTRIBUTE limits JSONB;

-- 2) 回填遗留 spec.quota -> limits.token_quota（默认 monthly），仅当 quota>0
UPDATE api.api_keys k
SET spec = ROW(
        (k.spec).quota,
        (k.spec).expires_in,
        jsonb_build_object(
            'token_quota',
            jsonb_build_object('limit', (k.spec).quota, 'period', 'monthly')
        )
    )::api.api_key_spec
WHERE (k.spec).quota IS NOT NULL
  AND (k.spec).quota > 0
  AND (k.spec).limits IS NULL;

-- 3) create_api_key 增加 p_limits（在 019 版基础上，ROW 加第三个属性）。
-- 注意：新增形参会创建“重载”而非替换，导致 create_api_key(p_workspace,p_name,p_quota)
-- 调用产生歧义（function is not unique）。因此先 DROP 旧的 5 参签名，再建 6 参版本。
DROP FUNCTION IF EXISTS api.create_api_key(TEXT, TEXT, INTEGER, TEXT, INTEGER);
CREATE OR REPLACE FUNCTION api.create_api_key(
    p_workspace TEXT,
    p_name TEXT,
    p_quota INTEGER,
    p_display_name TEXT DEFAULT NULL,
    p_expires_in INTEGER DEFAULT NULL,
    p_limits JSONB DEFAULT NULL
) RETURNS api.api_keys
SECURITY DEFINER
AS $$
DECLARE
    p_user_id UUID;
    v_key_id UUID;
    v_key_value TEXT;
    v_result api.api_keys;
BEGIN
    p_user_id = auth.uid();

    IF NOT EXISTS (SELECT 1 FROM api.user_profiles WHERE id = p_user_id) THEN
        RAISE EXCEPTION 'User profile not found';
    END IF;

    IF p_display_name IS NULL THEN
        p_display_name := p_name;
    END IF;

    v_key_id := gen_random_uuid();
    v_key_value := api.generate_api_key(p_user_id, v_key_id, p_expires_in);

    INSERT INTO api.api_keys (
        id, api_version, kind, metadata, spec, status, user_id
    ) VALUES (
        v_key_id,
        'v1',
        'ApiKey',
        ROW(p_name, p_display_name, p_workspace, NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata,
        ROW(p_quota, p_expires_in, p_limits)::api.api_key_spec,
        ROW('Pending', CURRENT_TIMESTAMP, NULL, v_key_value, 0, CURRENT_TIMESTAMP, NULL)::api.api_key_status,
        p_user_id
    )
    RETURNING * INTO v_result;

    RETURN v_result;
END;
$$ LANGUAGE plpgsql;

-- 4) set_api_key_limits：编辑限额（owner 鉴权）。SECURITY DEFINER + 显式 owner 校验。
CREATE OR REPLACE FUNCTION api.set_api_key_limits(p_id UUID, p_limits JSONB)
RETURNS api.api_keys
SECURITY DEFINER
AS $$
DECLARE
    v_result api.api_keys;
BEGIN
    UPDATE api.api_keys k
    SET spec = ROW((k.spec).quota, (k.spec).expires_in, p_limits)::api.api_key_spec
    WHERE k.id = p_id AND k.user_id = auth.uid()
    RETURNING * INTO v_result;

    IF NOT FOUND THEN
        RAISE EXCEPTION 'API key not found or not owned by caller';
    END IF;
    RETURN v_result;
END;
$$ LANGUAGE plpgsql;

-- 5) 周期用量助手：当前周期窗口内该 key 的 total_usage 求和（不可变流水聚合）。
CREATE OR REPLACE FUNCTION api.api_key_period_usage(p_id UUID, p_period TEXT)
RETURNS BIGINT
LANGUAGE sql STABLE SECURITY DEFINER
AS $$
    SELECT COALESCE(SUM((d.spec).total_usage), 0)::bigint
    FROM api.api_daily_usage d
    WHERE (d.spec).api_key_id = p_id
      AND (d.spec).usage_date >= CASE p_period
            WHEN 'daily'   THEN CURRENT_DATE
            WHEN 'weekly'  THEN date_trunc('week',  CURRENT_DATE)::date
            WHEN 'monthly' THEN date_trunc('month', CURRENT_DATE)::date
            WHEN 'yearly'  THEN date_trunc('year',  CURRENT_DATE)::date
            ELSE date_trunc('month', CURRENT_DATE)::date
          END
      AND (d.spec).usage_date <= CURRENT_DATE;
$$;

-- 6) get_api_key_remaining：网关 quota 插件按请求拉取的「剩余 token」标量（A1）。
--    无 token_quota 配额 -> NULL（不限）；否则 limit - 当期用量（可为负，网关判 ≤0 拦截）。
CREATE OR REPLACE FUNCTION api.get_api_key_remaining(p_id UUID)
RETURNS BIGINT
LANGUAGE plpgsql STABLE SECURITY DEFINER
AS $$
DECLARE
    v_limits  JSONB;
    v_limit   BIGINT;
    v_period  TEXT;
BEGIN
    SELECT (spec).limits INTO v_limits FROM api.api_keys WHERE id = p_id;
    IF v_limits IS NULL THEN
        RETURN NULL;
    END IF;
    v_limit := (v_limits #>> '{token_quota,limit}')::bigint;
    IF v_limit IS NULL OR v_limit <= 0 THEN
        RETURN NULL;
    END IF;
    v_period := COALESCE(v_limits #>> '{token_quota,period}', 'monthly');
    RETURN v_limit - api.api_key_period_usage(p_id, v_period);
END;
$$;

-- 7) get_api_key_limits：UI 读单一对象（配置 + 当期 used/remaining 展示）。owner 限定。
CREATE OR REPLACE FUNCTION api.get_api_key_limits(p_id UUID)
RETURNS JSONB
LANGUAGE plpgsql STABLE SECURITY DEFINER
AS $$
DECLARE
    v_limits JSONB;
    v_uid    UUID;
    v_limit  BIGINT;
    v_period TEXT;
    v_used   BIGINT;
BEGIN
    SELECT (spec).limits, user_id INTO v_limits, v_uid FROM api.api_keys WHERE id = p_id;
    IF NOT FOUND OR v_uid <> auth.uid() THEN
        RETURN NULL;
    END IF;
    v_limits := COALESCE(v_limits, '{}'::jsonb);

    v_limit := (v_limits #>> '{token_quota,limit}')::bigint;
    IF v_limit IS NOT NULL AND v_limit > 0 THEN
        v_period := COALESCE(v_limits #>> '{token_quota,period}', 'monthly');
        v_used := api.api_key_period_usage(p_id, v_period);
        v_limits := jsonb_set(
            v_limits, '{token_quota}',
            (v_limits->'token_quota')
                || jsonb_build_object('used', v_used, 'remaining', v_limit - v_used)
        );
    END IF;
    RETURN v_limits;
END;
$$;

NOTIFY pgrst, 'reload schema';
