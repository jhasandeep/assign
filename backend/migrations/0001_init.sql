-- Sutra Mini schema. Money is stored as integer paise (bigint), never floats.

CREATE TABLE skus (
    id        bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    code      text NOT NULL UNIQUE,
    name      text NOT NULL,
    image_url text NOT NULL
);

CREATE TABLE parties (            -- job-workers
    id    bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name  text NOT NULL,
    phone text NOT NULL
);

CREATE TABLE jobwork_issues (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    party_id    bigint NOT NULL REFERENCES parties (id),
    sku_id      bigint NOT NULL REFERENCES skus (id),
    qty         integer NOT NULL CHECK (qty > 0),
    -- Running total of receipts, maintained by the trigger below. The CHECK is
    -- the guarantee: Postgres refuses any write that pushes received past issued.
    received_qty integer NOT NULL DEFAULT 0,
    rate_paise  bigint NOT NULL CHECK (rate_paise >= 0),   -- job-work rate per piece
    issued_on   date NOT NULL,
    CONSTRAINT received_within_issued CHECK (received_qty >= 0 AND received_qty <= qty)
);

CREATE TABLE jobwork_receipts (
    id              bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    issue_id        bigint NOT NULL REFERENCES jobwork_issues (id),
    qty             integer NOT NULL CHECK (qty > 0),
    received_on     date NOT NULL,
    idempotency_key text NOT NULL UNIQUE,
    request_hash    text NOT NULL,    -- detects a key reused with a different payload
    created_at      timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX jobwork_receipts_issue_idx ON jobwork_receipts (issue_id);

-- Every receipt row bumps its issue's running total. The UPDATE takes a row lock
-- on the issue, so concurrent receipts for one issue are serialised, and the
-- CHECK above rejects the one that would overshoot. Nothing in app memory.
CREATE FUNCTION apply_receipt_to_issue() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    UPDATE jobwork_issues
       SET received_qty = received_qty + NEW.qty
     WHERE id = NEW.issue_id;
    RETURN NEW;
END
$$;

CREATE TRIGGER jobwork_receipts_apply
    AFTER INSERT ON jobwork_receipts
    FOR EACH ROW EXECUTE FUNCTION apply_receipt_to_issue();

CREATE TABLE vouchers (
    id              bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    type            text NOT NULL CHECK (type IN ('jobwork_receipt')),
    source_id       bigint NOT NULL,
    amount_paise    bigint NOT NULL CHECK (amount_paise >= 0),
    status          text NOT NULL DEFAULT 'pending'
                    CHECK (status IN ('pending', 'pushed', 'failed')),
    attempts        integer NOT NULL DEFAULT 0,
    last_error      text,
    -- Stable idempotency key sent to the accounting system on every attempt.
    client_ref      text NOT NULL UNIQUE DEFAULT gen_random_uuid()::text,
    external_ref    text,
    -- Worker bookkeeping: backoff schedule and a lease so two workers never
    -- hold the same voucher at once (and a dead worker's lease expires).
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    locked_until    timestamptz,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (type, source_id),
    CONSTRAINT pushed_has_external_ref CHECK (status <> 'pushed' OR external_ref IS NOT NULL)
);

CREATE INDEX vouchers_claim_idx ON vouchers (next_attempt_at) WHERE status = 'pending';
CREATE INDEX vouchers_status_idx ON vouchers (status, id);
