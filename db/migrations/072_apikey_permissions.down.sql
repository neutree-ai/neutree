-- PostgreSQL cannot safely drop enum values once added (every dependent column /
-- stored value would have to be rewritten), so this is intentionally a no-op.
-- The grants and RPC enforcement that USE these values are reverted in 073.down.
SELECT 1;
