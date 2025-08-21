-- ----------------------
-- Notify PGRST
-- ----------------------
DO $$
BEGIN
    NOTIFY pgrst, 'reload schema';
END
$$;
