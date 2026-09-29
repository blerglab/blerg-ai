-- Always-on plugins (per account, per engine): the ordered list of engine plugins that is
-- installed into every new cluster session started as that account. NOT secret — a marketplace
-- source and a plugin name — so it lives here rather than in the credential vault.
--
-- engine is a registry key (core/internal/plugins), so an engine other than claude can register
-- its own plugin sets later without a schema change. marketplace_source is what the engine's
-- "marketplace add" takes (a GitHub owner/repo in v1); position keeps the user's order.
--
-- The list is only ever replaced as a whole, inside one transaction (plugins.Service.Replace),
-- and only by a signed-in human: an agent that could add a plugin would gain persistent code
-- execution in every future session of that account.
CREATE TABLE user_plugins (
    account_id         uuid NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    engine             text NOT NULL,
    position           integer NOT NULL,
    marketplace_source text NOT NULL,
    plugin_name        text NOT NULL,
    created_at         timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (account_id, engine, position),
    UNIQUE (account_id, engine, marketplace_source, plugin_name)
);
