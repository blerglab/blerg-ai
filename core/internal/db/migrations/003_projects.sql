CREATE TABLE projects (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name         text UNIQUE NOT NULL,
    secret_scope text NOT NULL DEFAULT '',
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE project_members (
    project_id    uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    principal_sub text NOT NULL,
    role          text NOT NULL,
    PRIMARY KEY (project_id, principal_sub)
);
