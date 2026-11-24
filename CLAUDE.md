# Xray Console

Single-administrator web panel that installs and manages Xray-core
(VLESS + REALITY + XHTTP) on remote VPSes over SSH. The UI design is the
requirements document: a Design canvas of ~50 artboards (link in the user's
memory, `xray-console-design-canvas`).

## Decisions that are settled

- The panel is a web service on its own server; Xray runs on the remote hosts.
- Adding a server takes host, port, login and password in the UI. The panel
  installs its own SSH key and everything else itself. A small agent on the
  node is allowed if SSH alone is not enough.
- Balancing inside a chain is done by Xray itself on the entry VPS
  (`routing.balancers` + observatory). A client connects to one entry node;
  the rules that pick a chain live on that node, so a subscription is an
  ordinary `vless://` link.
- No subscription limits (traffic, speed, device count).
- Node side: nginx as the stub site, acme.sh for certificates, ufw or nftables
  depending on what is found.
- UI language is Russian, through i18next so English can be added as one more
  catalogue (`web/src/i18n/`).
- Not related to Angie Panel. Only its look is reused; shadcn components may be
  copied from `/Users/maxname/dev/archive/reverse-proxy/frontend`.

Build order: sign-in and setup (done) → servers, SSH keys, install with
preflight and rollback (done, see "Not built yet" below) → server config and
templates with `xray -test` (done, same note) →
availability checks and overview (done, same note) → chains (done, same note)
→ routing and subscriptions (done, same note) → update and uninstall (done,
same note) → statistics and alerts (done, same note) → devices and local
client (done, same note). Every stage of the agreed order is built; what is
left is in the "Not built yet" notes below.

## How installing works

- The panel downloads the Xray release itself, checks it against the published
  SHA-256 and uploads the files over SSH. A server needs neither curl, unzip
  nor a route to GitHub.
- An installation is an operation of eight steps (`internal/install`), each
  resumable and undoable. A failure pauses it; the administrator retries from
  that step or rolls everything back. Everything changed on the server is
  recorded in the server's manifest.
- The connection check starts a throwaway Xray client on the server and sends
  one HTTPS request through the new inbound over the SSH connection, then does
  a TLS handshake from the panel to the public port.
- Deleting a server from the panel leaves Xray running but takes the panel's
  key off the server and turns password login back on if the panel turned it
  off.

## How settings work

- A template is a versioned set of values for a fixed list of fields
  (`templates.Fields`), each marked common or overridable. Editing a template
  makes a new version; servers always aim for the latest.
- A server's desired configuration (`internal/plan`) is its template's latest
  version, its overrides for fields the template opens up, and what is its own:
  keys, shortIds, masking site. "Changed" is the difference between that and
  the snapshot recorded at the last successful apply (`servers.Applied`).
- Editing never touches a server. Applying does: `xray -test` on the new file,
  swap, restart, connection check — and the previous file goes back by itself
  if the restart or the check fails.
- "Apply to all" walks a template's changed servers one at a time.

## How availability checks work

- Xray-core is built into the panel as a library (`internal/probe`). Once a
  minute, for every installed server that is not mid-job, the panel opens the
  server's port and then sends a real request through VLESS + REALITY + XHTTP
  with that server's own keys — from outside, as a client would.
- A check uses the settings the server runs (`plan.RunningInbound`, from the
  applied snapshot), never the desired ones.
- Three failures in a row make a server "down" (since the first of them), three
  successes bring it back; a server never checked before is "up" on its first
  success. Every result is kept for 90 days; per-day totals are kept for good
  and are what the 30-day bars read.
- State changes, installations, applies and removals are written to `events`
  for the overview's feed.
- Because of the built-in Xray the module needs Go 1.27; `go` downloads that
  toolchain by itself.

## How chains work

- A chain is an ordered list of links (`internal/chains`); a link is one
  server or a group, optionally with a reserve. The first link is the entry
  and is always one server.
- A chain is a user on each of its servers: email `chain-<id>`, UUID derived
  from the chain's secret and the server's id, so nothing per-server is stored.
  The entry's user is what clients (and the panel's chain check) sign in as;
  every other server's user is what the link before it signs in as.
- `internal/planner` turns that into each server's routing: an outbound per
  server of the next link, a routing rule by user, and — for a group or a
  reserve — a balancer with `fallbackTag` and an `observatory` watching the
  group. Balancing and failover are Xray's own, on the link before the group.
  Strategies are Xray's: leastPing, roundRobin, random. A link with no server
  left and no reserve falls back to `block`, never to the entry's own exit.
- Chain membership is part of a server's plan (the `chains` field), so adding
  a server to a chain shows on that server as a change to apply. "Apply on the
  servers" does them far end first.
- The prober sends a request through every chain each round, entering at the
  entry as the chain's user.

## How routing and subscriptions work

- A rule set (`internal/rulesets`) belongs to one entry server — the server
  its clients connect to — and is an ordered list of rules plus a default
  route. A rule matches the destination by any of its conditions (domain,
  full, keyword, regexp, geosite, ip, geoip), optionally narrowed by ports and
  a sniffed protocol, and sends the traffic into a chain, straight out of the
  entry ("direct"), or nowhere ("block"). A rule may only name a chain that is
  entered on the set's own server.
- A subscription (`internal/subs`) is one person's access: a user `sub-<id>`
  on its rule set's entry server, with a UUID derived from the subscription's
  sealed secret, and a link `/s/<token>`. Only a subscription that is switched
  on and not past its end date is a user.
- The planner turns that into `xray.Policy` on the entry server: the users,
  and one Xray routing rule per rule (two when it has both names and
  addresses — inside one Xray rule those would both have to match), all
  limited to the set's users by `user`. A rule into a chain points at the same
  outbound or balancer the chain's own rule does (`Route.Target`). A chain
  with nobody to hand over to, or one that no longer starts here, resolves to
  `block`, never to `direct`. A set with no working subscription renders
  nothing.
- All of this is part of the entry server's plan (the `policies` field), so a
  new rule or subscription shows on the server as a change to apply; the
  Routing and Clients pages offer that apply themselves.
- `GET /s/<token>` is public: the token is the credential. It answers with the
  `vless://` link (`Inbound.ShareLink`, built from what the server runs) in
  base64, an empty list for a subscription that is off or over, and 404 for
  an unknown token. The panel's address in the link shown is `panel_url` from
  setup.
- An end date has to reach the server with nobody there to apply it:
  `internal/lapse` looks once a minute for servers that still let in a
  subscription past its end date and applies them — only when `policies` is
  the server's sole pending change. Otherwise it logs once and leaves the
  server to the administrator.
- A chain cannot be deleted, or have its entry moved, while a rule set sends
  traffic into it; a server cannot be deleted while it is a rule set's entry;
  a rule set cannot be deleted while subscriptions are on it.

Not built yet from the routing and clients designs: hit counts per rule, the
"check an address" tester, JSON import, reordering by dragging (there are up
and down buttons), a rule set made as a copy of another, updating geosite and
geoip lists (they are the ones installed with Xray), "local addresses direct"
(rules run on the server, where that has no meaning), limits and device counts
(decided against), choosing which chains a subscription may use, adding users
without restarting Xray, a subscription over several entry servers, this
computer and remote devices as clients (the last stage). The `vless://` link
follows the XTLS share-link proposal and has not been tried in client apps.

## How updating and removing Xray work

- The panel knows the newest Xray release from GitHub (`Releases.Latest`,
  remembered for an hour; `Installer.UseRelease` replaces the lookup). A server
  is outdated when `xray.CompareVersions` says its release is older. When
  GitHub cannot be reached there is simply nothing to offer.
- An update (`internal/install/update.go`, operation kind `update`) is three
  steps. `fetch` uploads the new release next to the running one
  (`/usr/local/share/xray/next`) and has it run `-test` on the server's live
  config.json — a release that reads the configuration differently is found
  out while the old one still serves. `replace` keeps the running release in
  `/usr/local/share/xray/prev`, moves the new files in and restarts. `verify`
  is the connection check with the settings the server runs. If the restart
  or the check fails the previous release goes back by itself, and the
  failure says whether that worked (`restored`), as a failed apply does.
- "Update all" walks the outdated servers one at a time, the next one only
  after the previous passed its check; on a failure it stops or goes on, as
  asked. It is a batch like "apply to all".
- Removing Xray (`internal/install/uninstall.go`, kind `uninstall`) undoes the
  installation's manifest in four steps — service, files and user, firewall
  rule, and last the panel's key and its sshd drop-in — and then deletes the
  server from the panel. The copies of earlier configurations in
  `/root/xray-console-backup` stay unless asked otherwise. Steps done are not
  put back: a removal that stops leaves the server in status `uninstalling`
  with a failed operation, to be retried from that step or deleted from the
  panel as it is.
- A server that is a link of a chain or the entry of a rule set cannot have
  its Xray removed, for the same reason it cannot be deleted.

Not built yet from the update and uninstall designs: moving traffic off a
server before touching it and waiting for open connections to end (Xray's own
balancer notices the restart; a server alone in its link is briefly down),
skipping servers that are the only exit of a chain, choosing a release other
than the newest, the stub site and certificate step (there is no stub site
yet), a list of commands to finish a removal by hand.

## How statistics and alerts work

- Every server's configuration turns on Xray's traffic counters — per inbound
  and per user — and its API on the server's loopback (`xray.APIListen`).
  Because that is something the panel writes into every configuration, it
  came with `xray.ConfigRevision`: the plan's `revision` field, which makes
  servers applied before it show a change to apply. Bump it whenever the
  renderer starts writing something new for every server.
- `internal/traffic`: every five minutes (`--traffic-interval`) the panel runs
  `xray api statsquery` on each server over SSH and stores what the counters
  gained, by the hour (`traffic_hourly`, kept 90 days). Counters are
  cumulative since Xray started; one that went down means a restart, and what
  it shows is all new. "Up" is from clients to the server, "down" from the
  server to clients. A server's traffic is its inbound's, which includes
  chain traffic passing through; a subscription's is its user's.
- Latency and loss on the statistics page come from the availability checks
  already kept (`probe.Store.Latency`: medians and the 95th percentile by
  position in the sorted column); outages from the `server_up` events, which
  carry how long each lasted, plus whatever is down right now.
- `internal/alerts` tells the administrator about outages past a threshold
  ("down for longer than N minutes" — a shorter one is never mentioned, its
  end included), recoveries, and a new Xray release with servers behind it
  (once per release). It sends to a Telegram chat through a bot and to a
  webhook as JSON. Messages wait in `alert_outbox`: quiet hours (23:00–08:00,
  the panel's clock) hold them and Telegram then gets one list; a channel
  that fails is retried without the other getting the message twice, and
  after five rounds a message is dropped. Settings are sealed (they hold the
  bot token) and the token is never sent back to the browser.
- The sentences alerts send are Russian, in `internal/alerts/messages.go` —
  the one place where text for people is written in Go.

Not built yet from the statistics and alerts designs: checks from other
vantage points (an entry node, clients), mail as a channel (the panel has no
mail server settings), alerts about a chain going to its reserve (Xray
decides that on the server and does not say), certificate renewal and
subscription limits (neither exists), keeping a removed server's statistics,
per-chain traffic, the checks' settings screen.

## How devices and the local client work

- A device (`internal/devices`) is a machine on the administrator's side —
  Linux with systemd — on which the panel installs an Xray client over SSH.
  To the servers it is what a subscription is: a user `dev-<id>` on its rule
  set's entry server, so rules still run on the entry. The client only offers
  local proxies (SOCKS and HTTP, on the loopback or on every interface) and
  sends everything to the server; it routes nothing itself and needs no rule
  files.
- The client has places of its own on the device (`/usr/local/lib/xray-console`,
  `/usr/local/etc/xray-console`, `xray-console-client.service`, user
  `xray-console`), not Xray's usual ones: a device may have an Xray of its
  own — it may even be one of the panel's servers.
- Setting a device up is six steps in one background job (`devices.Manager`,
  not the `ops` engine — that one is tied to servers): connect and put the
  panel's key there, check the system, upload Xray, make the entry server let
  the device in (`Installer.EnsureUser`: applies the entry by itself, but only
  when rules and subscriptions are all that is waiting there), put the
  configuration and start, and send one request through the client's SOCKS
  port. Steps are safe to repeat; a failure is kept with its log and the job
  is run again from the start.
- A device's configuration follows what its entry server runs, not what the
  server is about to run. When the server's applied settings change — a key
  rotation — the device shows as waiting, and one that has "send changes
  automatically" gets them at the panel's next look at it (once a minute,
  which is also how "online" is known). A configuration the client does not
  start with is replaced by the previous one.
- Deleting a device takes the client off it and the panel's key too if the
  panel put it there; a device that cannot be reached is deleted from the
  panel only, and the answer says so.
- The local client (`internal/localclient`) is the same kind of client run
  inside the panel process by the built-in Xray, for the computer the panel
  is on: SOCKS and HTTP on 127.0.0.1, user `local`. It restarts itself when
  its settings or the entry server's change, and every few minutes sends a
  request through itself to say whether it still gets out.
- The planner learns about both through `Planner.Members`, next to
  subscriptions; a rule set cannot be deleted while a device is on it.

Not built yet from the devices and local client designs: OpenWrt and anything
without systemd, transparent proxy for a whole network and TUN (both change
the device's routing and can cut the panel off from it), a system proxy on
the panel's computer (the panel is a web service, not a desktop program),
several profiles for the local client, a default exit separate from the rule
set, DNS settings, "block traffic when the connection drops", a queue of
changes for a device that is offline (it gets the current settings when it
is next seen), updating Xray on a device other than by setting it up again,
changing a device's address.

Not built yet from the chains designs: choosing by speed or by load (Xray has
no such strategies as the design describes them), "skip the link" on failure,
the switching threshold and exclusion counts (Xray's observatory decides),
a group as the entry, a link to hand to a client (that is subscriptions).

Not built yet from the checks designs: the settings screen (interval and URL
are the `--check-interval` and `--check-url` flags), a per-server switch,
checks from other vantage points, speed measurement, the certificate tile on
the overview.

Not built yet from the settings designs: arbitrary template variables (the
field list is fixed in code), changing the port, xmux and split channels
(client-side settings — they arrive with clients), node roles, keeping old
keys for 24 hours on rotation, inbound users.

Not built yet from the install designs: masking with the server's own domain
(nginx stub, acme.sh), taking over an existing Xray, choices when port 443 is
busy, editing nftables rules, automatic reconnect during an installation,
importing other SSH keys.

## Layout

- `cmd/xray-console` — the binary: `serve`, `reset-password`, `version`.
- `internal/auth` — the administrator account, sessions, lockout, TOTP.
- `internal/servers` — the servers the panel manages and their manifests.
- `internal/sshx` — SSH client: pinned host keys, commands, file uploads.
- `internal/sshkeys` — the panel's own SSH key.
- `internal/ops` — runs step-by-step jobs with a log, retry and rollback.
- `internal/install` — the installation steps, applying settings, the
  connection check, updating Xray, removing it.
- `internal/templates` — templates, their fields and versions.
- `internal/plan` — a server's desired configuration and its diff from the
  applied one.
- `internal/chains` — chains: their links, validation, last check.
- `internal/rulesets` — rule sets: rules, their validation, the default route.
- `internal/subs` — subscriptions: the link's token and the user's secret.
- `internal/lapse` — takes subscriptions past their end date off the server.
- `internal/devices` — devices: the Xray client the panel installs on them.
- `internal/localclient` — the client inside the panel, for its own computer.
- `internal/planner` — the one place that combines template, server, chains,
  rule sets and their clients into a plan; everything that needs a plan asks
  it.
- `internal/probe` — availability checks through the built-in Xray, their
  history and state.
- `internal/events` — what happened, for the overview's feed.
- `internal/traffic` — traffic counters read off the servers, by the hour.
- `internal/alerts` — alert settings, the outbox, Telegram and webhook.
- `internal/xray` — keys, config.json, systemd unit, release downloads.
- `internal/httpapi` — JSON API under `/api`, serves the embedded SPA.
- `internal/store` — SQLite (modernc, no cgo) and numbered SQL migrations.
- `internal/secrets` — AES-GCM with `data/master.key`; use it for anything
  private the panel stores (SSH keys, REALITY keys).
- `internal/webui` — embeds `dist/`, which `pnpm build` in `web/` writes.
- `web/` — React 19, Tailwind v4, shadcn/ui, TanStack Router and Query.

## Commands

- `make build` — frontend, then the single binary at `bin/xray-console`.
- `make test` — `go vet`, Go tests, ESLint, Vitest.
- `make run` — build and start on 127.0.0.1:8080 with `./data`.
- `make node` — start a throwaway Debian node in Docker (`test/node`); it
  prints the address and the root password to add it in the UI.
- `make integration` — run the real installer against that node: install,
  failure and rollback, applies, a device set up on the same machine, an
  update from an older release and one that fails, removal.
- `make integration-chain` — three nodes: a chain with a balanced group,
  failover and recovery, then subscriptions and rules on its entry.
- Frontend only: `cd web && pnpm dev` (proxies `/api` to 127.0.0.1:8080).

## Conventions

- API errors are `{"error": {"code", "message", "details"}}`. The UI translates
  by `code`; `message` is English, for logs.
- State-changing requests must carry the `X-XC-Request` header (CSRF).
- Every user-visible string goes through `t()`; Russian plurals use the
  `_one/_few/_many/_other` suffixes.
- Do not show a control for something that does not work yet.
- Operation failures carry a `code`; the UI has a title and a paragraph per
  code in `server.errors`. A new code needs both, or it reads as "internal".
- Step details and log lines are technical facts in English; everything else
  the administrator reads is translated.
- Anything written to a shell command is either validated to a strict shape
  (`servers.ValidateTarget`, `xray.NormalizeMaskHost`) or quoted with
  `sshx.Quote`.
