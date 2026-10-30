CREATE TABLE actions_attachments (
    key text NOT NULL,
    payload jsonb NOT NULL
);
CREATE TABLE actions_evidence (
    key text NOT NULL,
    run_id bigint NOT NULL,
    attempt integer NOT NULL,
    payload jsonb NOT NULL
);
CREATE TABLE coordinator_audit (
    id bigint NOT NULL,
    worker text NOT NULL,
    task text NOT NULL,
    action text NOT NULL,
    created_at timestamp with time zone NOT NULL
);
CREATE SEQUENCE coordinator_audit_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;
ALTER SEQUENCE coordinator_audit_id_seq OWNED BY coordinator_audit.id;
CREATE TABLE coordinator_invitations (
    name text NOT NULL,
    hash bytea NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    consumed boolean DEFAULT false NOT NULL
);
CREATE TABLE coordinator_tasks (
    id text NOT NULL,
    kind text NOT NULL,
    repo text NOT NULL,
    issue bigint DEFAULT 0 NOT NULL,
    pr bigint DEFAULT 0 NOT NULL,
    operator text NOT NULL,
    payload jsonb NOT NULL,
    tags jsonb DEFAULT '[]'::jsonb NOT NULL,
    priority integer DEFAULT 0 NOT NULL,
    queue_position bigint DEFAULT 0 NOT NULL,
    state text NOT NULL,
    attempt integer DEFAULT 0 NOT NULL,
    lease_owner text DEFAULT ''::text NOT NULL,
    lease_expires_at timestamp with time zone,
    fencing_token bigint DEFAULT 0 NOT NULL,
    result jsonb,
    created_at timestamp with time zone NOT NULL,
    updated_at timestamp with time zone NOT NULL,
    application_attempts integer DEFAULT 0 NOT NULL,
    application_error text DEFAULT ''::text NOT NULL,
    result_digest text DEFAULT ''::text NOT NULL,
    CONSTRAINT coordinator_tasks_check CHECK (((issue > 0) OR (pr > 0))),
    CONSTRAINT coordinator_tasks_kind_check CHECK ((kind = ANY (ARRAY['implement'::text, 'repair'::text, 'acceptance'::text, 'reply'::text]))),
    CONSTRAINT coordinator_tasks_state_check CHECK ((state = ANY (ARRAY['pending'::text, 'leased'::text, 'done'::text, 'failed'::text, 'cancelled'::text, 'head-mismatch'::text])))
);
CREATE TABLE coordinator_tokens (
    name text NOT NULL,
    hash bytea NOT NULL,
    created_at timestamp with time zone NOT NULL,
    last_seen timestamp with time zone,
    revoked boolean DEFAULT false NOT NULL,
    scope text DEFAULT 'worker'::text NOT NULL,
    CONSTRAINT coordinator_tokens_hash_check CHECK ((octet_length(hash) = 32)),
    CONSTRAINT coordinator_tokens_scope_check CHECK ((scope = ANY (ARRAY['worker'::text, 'admin'::text])))
);
CREATE TABLE coordinator_workers (
    id text NOT NULL,
    tags jsonb DEFAULT '[]'::jsonb NOT NULL,
    run_untagged boolean DEFAULT true NOT NULL,
    max_parallel integer NOT NULL,
    roles jsonb DEFAULT '[]'::jsonb NOT NULL,
    backend text DEFAULT ''::text NOT NULL,
    model text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone NOT NULL,
    updated_at timestamp with time zone NOT NULL,
    paused boolean DEFAULT false NOT NULL,
    durable boolean DEFAULT false NOT NULL,
    CONSTRAINT coordinator_workers_max_parallel_check CHECK ((max_parallel > 0))
);
CREATE TABLE effect_intents (
    id text NOT NULL,
    task_id text NOT NULL,
    lease_epoch bigint NOT NULL,
    "position" integer NOT NULL,
    kind text NOT NULL,
    repo text NOT NULL,
    number bigint DEFAULT 0 NOT NULL,
    idempotency_key text NOT NULL,
    payload_digest text NOT NULL,
    payload jsonb NOT NULL,
    status text NOT NULL,
    applied_reference text DEFAULT ''::text NOT NULL,
    updated_at timestamp with time zone NOT NULL,
    CONSTRAINT effect_intents_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'applied'::text, 'uncertain'::text, 'failed'::text])))
);
CREATE TABLE inbox_events (
    source text NOT NULL,
    external_id text NOT NULL,
    task_id text NOT NULL,
    received_at timestamp with time zone NOT NULL,
    processed_at timestamp with time zone NOT NULL
);
CREATE TABLE pilot_runs (
    offload_run_id text,
    caller_run_id text,
    session_id text,
    exit_code bigint,
    error_kind text,
    elapsed_ms bigint,
    execution_status text,
    run_dir text NOT NULL,
    repo text,
    project text,
    issue_number bigint,
    pr_url text,
    operator text,
    status text,
    started_at timestamp with time zone,
    ended_at timestamp with time zone,
    duration_ms bigint,
    summary text,
    error text,
    profile text,
    backend text,
    model text,
    machine text,
    cost_usd numeric,
    input_tokens bigint,
    output_tokens bigint,
    cache_read_input_tokens bigint,
    cache_creation_input_tokens bigint,
    usage_reported boolean,
    events_path text,
    log_path text,
    payload jsonb
);
CREATE TABLE run_model_usage (
    run_dir text NOT NULL,
    kind text NOT NULL,
    model text NOT NULL,
    cost_usd numeric,
    input_tokens bigint,
    output_tokens bigint,
    cache_read_input_tokens bigint,
    cache_creation_input_tokens bigint
);
CREATE TABLE runs (
    offload_run_id text,
    caller_run_id text,
    session_id text,
    exit_code bigint,
    error_kind text,
    elapsed_ms bigint,
    execution_status text,
    run_dir text NOT NULL,
    repo text,
    project text,
    issue_number bigint,
    pr_url text,
    operator text,
    status text,
    started_at timestamp with time zone,
    ended_at timestamp with time zone,
    duration_ms bigint,
    summary text,
    error text,
    profile text,
    backend text,
    model text,
    machine text,
    cost_usd numeric,
    input_tokens bigint,
    output_tokens bigint,
    cache_read_input_tokens bigint,
    cache_creation_input_tokens bigint,
    usage_reported boolean,
    events_path text,
    log_path text,
    payload jsonb
);
CREATE TABLE scheduled_check_runs (
    id text NOT NULL,
    check_id text,
    started_at timestamp with time zone,
    ended_at timestamp with time zone,
    deadline timestamp with time zone,
    status text,
    summary text,
    actions jsonb,
    usage jsonb
);
CREATE TABLE scheduled_checks (
    id text NOT NULL,
    cron text,
    timezone text,
    prompt text,
    ops bigint,
    timeout_seconds bigint,
    enabled boolean,
    owner text,
    created_at timestamp with time zone,
    updated_at timestamp with time zone,
    last_run_at timestamp with time zone,
    last_status text,
    last_summary text,
    failures bigint,
    last_tick timestamp with time zone
);
CREATE TABLE schema_migrations (
    version integer NOT NULL,
    name text NOT NULL,
    applied_at timestamp with time zone DEFAULT now() NOT NULL
);
CREATE TABLE task_attempts (
    task_id text NOT NULL,
    attempt_number bigint NOT NULL,
    lease_epoch bigint NOT NULL,
    worker_id text NOT NULL,
    kind text NOT NULL,
    status text NOT NULL,
    started_at timestamp with time zone NOT NULL,
    ended_at timestamp with time zone,
    error_kind text DEFAULT ''::text NOT NULL,
    result_digest text DEFAULT ''::text NOT NULL,
    CONSTRAINT task_attempts_kind_check CHECK ((kind = ANY (ARRAY['infra'::text, 'product'::text])))
);
ALTER TABLE ONLY coordinator_audit ALTER COLUMN id SET DEFAULT nextval('coordinator_audit_id_seq'::regclass);
ALTER TABLE ONLY actions_attachments
    ADD CONSTRAINT actions_attachments_pkey PRIMARY KEY (key);
ALTER TABLE ONLY actions_evidence
    ADD CONSTRAINT actions_evidence_pkey PRIMARY KEY (key, run_id, attempt);
ALTER TABLE ONLY coordinator_audit
    ADD CONSTRAINT coordinator_audit_pkey PRIMARY KEY (id);
ALTER TABLE ONLY coordinator_invitations
    ADD CONSTRAINT coordinator_invitations_pkey PRIMARY KEY (name);
ALTER TABLE ONLY coordinator_tasks
    ADD CONSTRAINT coordinator_tasks_pkey PRIMARY KEY (id);
ALTER TABLE ONLY coordinator_tokens
    ADD CONSTRAINT coordinator_tokens_pkey PRIMARY KEY (name);
ALTER TABLE ONLY coordinator_workers
    ADD CONSTRAINT coordinator_workers_pkey PRIMARY KEY (id);
ALTER TABLE ONLY effect_intents
    ADD CONSTRAINT effect_intents_idempotency_key_key UNIQUE (idempotency_key);
ALTER TABLE ONLY effect_intents
    ADD CONSTRAINT effect_intents_pkey PRIMARY KEY (id);
ALTER TABLE ONLY inbox_events
    ADD CONSTRAINT inbox_events_pkey PRIMARY KEY (source, external_id);
ALTER TABLE ONLY pilot_runs
    ADD CONSTRAINT pilot_runs_pkey PRIMARY KEY (run_dir);
ALTER TABLE ONLY run_model_usage
    ADD CONSTRAINT run_model_usage_pkey PRIMARY KEY (run_dir, kind, model);
ALTER TABLE ONLY runs
    ADD CONSTRAINT runs_pkey PRIMARY KEY (run_dir);
ALTER TABLE ONLY scheduled_check_runs
    ADD CONSTRAINT scheduled_check_runs_pkey PRIMARY KEY (id);
ALTER TABLE ONLY scheduled_checks
    ADD CONSTRAINT scheduled_checks_pkey PRIMARY KEY (id);
ALTER TABLE ONLY schema_migrations
    ADD CONSTRAINT schema_migrations_pkey PRIMARY KEY (version);
ALTER TABLE ONLY task_attempts
    ADD CONSTRAINT task_attempts_pkey PRIMARY KEY (task_id, attempt_number);
ALTER TABLE ONLY task_attempts
    ADD CONSTRAINT task_attempts_task_id_lease_epoch_key UNIQUE (task_id, lease_epoch);
CREATE UNIQUE INDEX coordinator_active_implementation ON coordinator_tasks USING btree (repo, issue) WHERE ((kind = 'implement'::text) AND (issue > 0) AND (state = ANY (ARRAY['pending'::text, 'leased'::text])));
CREATE UNIQUE INDEX coordinator_active_task ON coordinator_tasks USING btree (repo, (
CASE
    WHEN (pr > 0) THEN 'pr'::text
    ELSE 'issue'::text
END), (
CASE
    WHEN (pr > 0) THEN pr
    ELSE issue
END), kind) WHERE (state = ANY (ARRAY['pending'::text, 'leased'::text]));
CREATE INDEX coordinator_leases ON coordinator_tasks USING btree (lease_owner, lease_expires_at) WHERE (state = 'leased'::text);
CREATE INDEX coordinator_pending_order ON coordinator_tasks USING btree (priority, queue_position, created_at, id) WHERE (state = 'pending'::text);
CREATE INDEX effect_intents_reconcile ON effect_intents USING btree (task_id, "position") WHERE (status = ANY (ARRAY['pending'::text, 'uncertain'::text]));
CREATE INDEX idx_pilot_runs_machine ON pilot_runs USING btree (machine);
CREATE UNIQUE INDEX idx_pilot_runs_offload_attempt ON pilot_runs USING btree (machine, offload_run_id) WHERE ((offload_run_id <> ''::text) AND (machine <> ''::text) AND (machine <> 'unknown'::text));
CREATE INDEX idx_pilot_runs_profile ON pilot_runs USING btree (profile);
CREATE INDEX idx_pilot_runs_project_started ON pilot_runs USING btree (project, started_at);
CREATE INDEX idx_pilot_runs_repo_started ON pilot_runs USING btree (repo, started_at);
CREATE INDEX idx_pilot_runs_started ON pilot_runs USING btree (started_at);
CREATE INDEX idx_runs_machine ON runs USING btree (machine);
CREATE UNIQUE INDEX idx_runs_offload_attempt ON runs USING btree (machine, offload_run_id) WHERE ((offload_run_id <> ''::text) AND (machine <> ''::text) AND (machine <> 'unknown'::text));
CREATE INDEX idx_runs_profile ON runs USING btree (profile);
CREATE INDEX idx_runs_repo_started ON runs USING btree (repo, started_at);
CREATE INDEX idx_runs_started_at ON runs USING btree (started_at);
CREATE INDEX idx_scheduled_check_runs_check_id ON scheduled_check_runs USING btree (check_id);
ALTER TABLE ONLY actions_evidence
    ADD CONSTRAINT actions_evidence_key_fkey FOREIGN KEY (key) REFERENCES actions_attachments(key);
ALTER TABLE ONLY effect_intents
    ADD CONSTRAINT effect_intents_task_id_fkey FOREIGN KEY (task_id) REFERENCES coordinator_tasks(id);
ALTER TABLE ONLY inbox_events
    ADD CONSTRAINT inbox_events_task_id_fkey FOREIGN KEY (task_id) REFERENCES coordinator_tasks(id);
ALTER TABLE ONLY task_attempts
    ADD CONSTRAINT task_attempts_task_id_fkey FOREIGN KEY (task_id) REFERENCES coordinator_tasks(id);
