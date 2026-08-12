-- Terminal OAuth revocation failures remain observable without retaining a
-- worker lease forever. terminal_reason is an allowlisted, secret-free code;
-- provider errors and token material must never be stored in this column.

-- The final terminal columns, invariant, and index are part of the first
-- unreleased baseline in 0005 so CockroachDB never has to unlock this table.
