-- Old check results are deleted, and latency is read, by time alone; the only
-- index began with the server, so both went through the whole table.
CREATE INDEX probe_results_at ON probe_results (at);

-- "Who is on this rule set" is asked for every server's plan.
CREATE INDEX subscriptions_rule_set ON subscriptions (rule_set_id);
CREATE INDEX devices_rule_set ON devices (rule_set_id);
