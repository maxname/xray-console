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
- UI language is Russian, and only Russian: an English catalogue is not
  planned. Strings still go through i18next (`web/src/i18n/`).
- The panel serves plain HTTP and is meant to sit behind a reverse proxy that
  terminates TLS. It will not do TLS itself; what belongs to the panel is
  behaving correctly behind the proxy (`--trusted-proxies`, the cookie's
  `Secure` flag) and saying so when it is set up wrongly.
- `.claude/launch.json` and the local path below stay in the public
  repository.
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
  nor a route to GitHub. The checksum is kept next to the cached archive and
  the archive is checked against it again every time it is used; a copy that
  no longer matches is downloaded again.
- A client of a server cannot reach the server's own loopback or private
  addresses through it. The panel writes no rule for that: Xray's `freedom`
  outbound refuses them to proxied clients by itself. `make integration`
  checks that the release on the node still does.
- An installation is an operation of eight steps (`internal/install`) — nine
  for a server with its own domain, see below — each resumable and undoable. A failure pauses it; the administrator retries from
  that step or rolls everything back. Everything changed on the server is
  recorded in the server's manifest.
- The connection check starts a throwaway Xray client on the server and sends
  one HTTPS request through the new inbound over the SSH connection, then does
  a TLS handshake from the panel to the public port.
  The client's loopback port is picked by the panel, below the range Linux
  gives to outgoing connections; one that is taken on the server is not a
  failed check — another is tried (`checkPortAttempts`).
- Deleting a server from the panel leaves Xray running but takes the panel's
  key off the server and turns password login back on if the panel turned it
  off.

## How masking with the server's own domain works

- A server masquerades either as someone else's site (`Inbound.SetMaskHost`)
  or as its own domain (`Inbound.SetDomain`, `Inbound.Domain`): a choice per
  server, at installation and afterwards. With its own domain REALITY's
  `dest` is a stub site on the server's loopback (`xray.StubListen`,
  127.0.0.1:8443) and `serverNames` is the domain; strangers who come to 443
  are handed to the stub and see a real certificate for the name.
- The stub (`internal/install/site.go`) is nginx from the distribution's
  packages — TLS on the loopback, port 80 for the certificate authority's
  HTTP-01 check and a redirect — one built-in page that says nothing about
  what the server is for, and a Let's Encrypt certificate that acme.sh
  obtains. It has places of its own (`/usr/local/lib/xray-console-site`,
  `/usr/local/etc/xray-console-site`, `/var/www/xray-console-site`,
  `/etc/nginx/conf.d/xray-console-site.conf`) and its own manifest kinds,
  `packages` and `site`.
- acme.sh reaches a server the way Xray does (`internal/acmesh`): the panel
  downloads one pinned release, checks it against a SHA-256 that is part of
  the panel's code, and uploads it. A newer acme.sh comes with a newer panel.
  It is run in place, never installed: renewal is a systemd timer the panel
  writes (`xray-console-site-renew.timer`, daily), and nginx is reloaded by
  the command acme.sh keeps with the certificate.
- The stub is never set up next to what is someone else's. A busy port 80 or
  8443, or an nginx the panel did not install, stops the job before anything
  is changed (`sitePreflight`: `port_in_use`, `nginx_exists`), and so does a
  domain that does not lead to the server and nowhere else
  (`domainPointsHere`: the panel resolves the name and compares it with the
  server's address and the addresses on its interfaces). A domain with an
  IPv6 address is refused too, even the server's own (`domain_ipv6`): Xray
  listens on IPv4 only, and over IPv6 the domain's site would not open. An
  installation asks in its system step; an apply, at the start of its stub
  step.
- Which nginx packages the panel installed is worked out on the server, by
  comparing with the list of packages taken before (`listPackages`), and only
  those are ever purged. curl, openssl and ca-certificates, if apt added
  them, stay.
- An installation with a domain has the step `site` between the firewall and
  the connection check; port 80 is the stub's and is opened and closed by its
  step. When that step, the checks before it or the connection check after
  it fail, the installation can be gone on with without the domain
  (`Installer.WithoutDomain`): the job's params and the server's inbound are
  changed to someone else's site, and the stub's own step — made a step to
  do again if it was done already (`ops.Reopen`) — takes away what was set
  up and gives Xray a configuration that no longer points at it.
- The domain is part of the plan (`reality.domain`), so giving a server a
  domain, moving it to another or giving it up are changes to apply. An apply
  that sets or changes the domain has `site` as its first step: the stub has
  to answer for the new name before Xray is pointed at it. If anything after
  that fails, the configuration goes back first and the stub after it
  (`session.restore`), and `restored` covers both. A certificate that was
  obtained on the way is not thrown away with the stub (`parkSite`): acme.sh's
  account and the certificates stay, in the manifest, for the next attempt —
  the authority issues only a few certificates a week for one name. A domain
  that is given up needs no step: the stub goes once the new settings have
  passed their check (`settleSite`, which also removes certificates of names
  the server no longer answers for), and what could not be removed stays in
  the manifest for the next apply or the removal of Xray. An update of Xray
  does not touch the stub.
- The connection check of a server with a domain wants a certificate a
  visitor would accept only from the job that gives the server that domain
  (`session.givesDomain`): an installation with it, an apply that sets or
  changes it. For any other job — an apply of something else, an update — a
  certificate that is bad already is said in the log and does not fail it: a
  renewal that stopped must not also keep a subscription from being taken
  off.
- Removing Xray from such a server has a step `site` too, after Xray's files
  and before the ports. Deleting the server from the panel leaves the stub
  and its timer running with Xray.
- What certificate each server shows is looked at from outside
  (`internal/certs`): once an hour, and at once after an installation, an
  apply or a renewal, the panel connects to port 443 asking for the domain
  and reads the certificate's dates — no SSH involved, and what it sees is
  what a visitor sees. The overview's tile shows the one that runs out
  first; a later end for the same name is the event `cert_renewed`.
- A certificate is overdue (`certs.State.Overdue`) when less than 20 days of
  it are left, or a quarter of its life if that is shorter: acme.sh renews
  long before. An overdue one is told once per certificate
  (`alerts.KindCertNotRenewed`, the `on_cert` switch).
- "Renew now" (`Installer.RenewCert`) runs acme.sh with `--force` over SSH, a
  one-off command, but one that keeps the server to itself while it runs
  (`ops.Hold`): no job starts there meanwhile, and no second renewal. The
  authority's refusals come back as `cert_failed` or `cert_rate_limited`.
- The certificate authority is Let's Encrypt. Only tests name another
  (`Installer.UseACME`): `make integration-domain` runs the whole thing
  against Pebble on a Docker network of its own.

Not built yet from the masking designs: IPv6 for the domain (Xray would have
to listen there as well), a site of one's own from an archive
and a mirror of another site as the stub's content (every stub shows the same
page, which is something an active prober could learn to recognise), adding
the stub to an nginx that is already on the server, choices when port 80 is
busy, any check other than HTTP-01, a way to remove a stub that an apply left
behind other than applying again or removing Xray, the mail address for the
authority's notices (Let's Encrypt no longer sends them), the log of issues,
choosing how long before its end a certificate is renewed, setting the XHTTP
Host from the domain (it stays a template field).

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
  servers" does them far end first, and so does "apply to all"
  (`Planner.FarFirst`).
- A link's desired configuration dials the next link the way that one should
  accept connections, not the way it does yet. So a server whose chains would
  change is refused an apply (`upstream_pending`, naming the servers) while a
  server it hands traffic to has its own settings edited and not applied
  (`Planner.Awaiting`): the far end goes first.
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
  server to the administrator. An apply that does not take the subscription
  off is not repeated a minute later — every one restarts Xray there: the
  next comes after 15 minutes, then an hour, then every six hours.
- A chain cannot be deleted, or have its entry moved, while a rule set sends
  traffic into it; a server cannot be deleted while it is a rule set's entry;
  a rule set cannot be deleted while subscriptions are on it.

Not built yet from the routing and clients designs: hit counts per rule, the
"check an address" tester, JSON import, reordering by dragging (there are up
and down buttons), a rule set made as a copy of another, updating geosite and
geoip lists (they are the ones installed with Xray), "local addresses direct"
(rules run on the server, where that has no meaning), limits and device counts
(decided against), choosing which chains a subscription may use, adding users
without restarting Xray, a subscription over several entry servers. The `vless://` link
follows the XTLS share-link proposal; it connected in one client app on a real
server, and other apps have not been tried.

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
than the newest, a list of commands to finish a removal by hand.

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
- Before the panel restarts Xray itself — an apply, an update, a restart
  asked for — it reads the counters one more time
  (`Installer.RecordTraffic`), so what was gained since the last round is
  kept. A restart the panel did not make still loses up to one interval.
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
  that fails is retried without the other getting the message twice, after
  waits that grow from a minute to six hours (`retry_at`), and a message is
  dropped after ten attempts — most of a day. A list too long for one
  Telegram message is sent in parts. Settings are sealed (they hold the
  bot token) and the token is never sent back to the browser.
- Quiet hours, the times in messages and the days of the graphs go by the
  panel's clock: the machine's zone, or `--timezone` (an IANA name; the zone
  database is built in). It is set once at start as `time.Local`, so code
  reads it with `.Local()` and never names a zone itself. Changing it moves
  where a "day" begins for history already recorded.
- The sentences alerts send are Russian, in `internal/alerts/messages.go` —
  the one place where text for people is written in Go.

Not built yet from the statistics and alerts designs: checks from other
vantage points (an entry node, clients), mail as a channel (the panel has no
mail server settings), alerts about a chain going to its reserve (Xray
decides that on the server and does not say), subscription limits (there
are none), keeping a removed server's statistics,
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
- Setting a device up is a job of six steps in the same engine that runs
  jobs on servers (`ops`; a job belongs to a server or to a device, kind
  `device_setup`): connect and put the panel's key there, check the system,
  upload Xray, make the entry server let the device in
  (`Installer.EnsureUser`: applies the entry by itself, but only when rules
  and subscriptions are all that is waiting there), put the configuration and
  start, and send one request through the client's SOCKS port. Sending
  changed settings later (`device_push`) is connect, entry, configuration and
  check. Steps are safe to repeat, and each finds its own way onto the
  device, so a failed job is gone on with from the step it stopped at
  (`Manager.Retry`) or run again from the start.
- A set-up that stopped partway can be undone (`Manager.Rollback`): the
  client comes off the device, and the panel's key too if this set-up added
  it; the device stays in the panel as one not set up. A push has nothing to
  undo — it puts the previous settings back by itself.
- A device's row says where it stands (`new`, `working`, `ready`, `failed`);
  what its job is doing, why it stopped and what it printed are the job's
  (`Manager.Last`, `/api/operations/<id>`). The device's dialog shows the
  steps while a job runs and steps with the log afterwards.
- A device's configuration follows what its entry server runs, not what the
  server is about to run. When the server's applied settings change — a key
  rotation — the device shows as waiting, and one that has "send changes
  automatically" gets them at the panel's next look at it (once a minute,
  which is also how "online" is known). A configuration the client does not
  start with is replaced by the previous one, and the same configuration is
  then not sent again at once: after 5 minutes, then 30, then every 3 hours.
- A device's proxies, when open to its network (`lan`), can ask for a user
  and password (`proxy_auth`). The password is derived from the device's
  secret, shown in the device's dialog and nowhere stored; the check after a
  push signs in with it (`install.SOCKSAuth`). Off by default: not every
  program can do SOCKS with a password.
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
checks from other vantage points, speed measurement.

Not built yet from the settings designs: arbitrary template variables (the
field list is fixed in code), changing the port, xmux and split channels
(client-side settings — they arrive with clients), node roles, keeping old
keys for 24 hours on rotation, inbound users.

Not built yet from the install designs: taking over an existing Xray, choices
when port 443 is busy, editing nftables rules, automatic reconnect during an installation,
importing other SSH keys.

## Layout

- `cmd/xray-console` — the binary: `serve`, `reset-password`, `version`.
- `internal/auth` — the administrator account, sessions, lockout, TOTP. The
  account is managed on the Security page (`web/src/pages/security.tsx`):
  changing the password signs every other browser out; two-factor sign-in
  is turned on with the password and a confirmed code and off with the
  password and a code; sessions are listed and ended by an id that is the
  start of the token's stored hash, never the token.
- `internal/servers` — the servers the panel manages and their manifests.
- `internal/sshx` — SSH client: pinned host keys, commands, file uploads.
- `internal/sshkeys` — the panel's own SSH key.
- `internal/remote` — how a failed connection or command is told to the
  administrator; shared by jobs on servers and on devices.
- `internal/ops` — runs step-by-step jobs with a log, retry and rollback, on
  a server or on a device; one job at a time on each.
  Finished jobs older than 90 days are pruned, except each server's latest.
- `internal/install` — the installation steps, applying settings, the
  connection check, updating Xray, removing it, the stub site of a server
  with its own domain.
- `internal/acmesh` — the pinned acme.sh the panel downloads and checks.
- `internal/certs` — the certificates servers with their own domain show, as
  seen from outside.
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
- `make test` — `go vet`, Go tests, ESLint, Vitest. CI
  (`.github/workflows/ci.yml`) runs the build, `gofmt` and this on every push
  and pull request; the Docker integration tests are run by hand.
- `make run` — build and start on 127.0.0.1:8080 with `./data`.
- `make node` — start a throwaway Debian node in Docker (`test/node`); it
  prints the address and the root password to add it in the UI.
- `make integration` — run the real installer against that node: install,
  failure and rollback, applies, a device set up on the same machine, an
  update from an older release and one that fails, removal.
- `make integration-chain` — three nodes: a chain with a balanced group,
  failover and recovery, then subscriptions and rules on its entry.
- `make integration-domain` — a node and Pebble (a test certificate
  authority) on a Docker network of their own (`test/domain`): a server with
  its own domain installed, moved to another, given the domain up, a
  certificate that is refused, renewal, removal.
- Frontend only: `cd web && pnpm dev` (proxies `/api` to 127.0.0.1:8080).

## Conventions

- API errors are `{"error": {"code", "message", "details"}}`. The UI translates
  by `code`; `message` is English, for logs.
- State-changing requests must carry the `X-XC-Request` header (CSRF).
- What can be asked without signing in — setup, sign-in, `/s/<token>` — goes
  through `Server.limited` (a burst, then a trickle, per address; IPv6 by
  /64). Failed sign-ins are counted the same way (`auth.throttleKey`), one
  attempt at a time. A session not asked to be remembered ends a week after
  it began however much it is used (`auth.SessionMax`).
- A panel listening on loopback believes a reverse proxy on its own machine
  without `--trusted-proxies` (`config.Loopback`); anything else has to be
  named. The setup link carries its code after `#`, which no server logs.
- What a managed server or device says is not trusted to be small or true:
  command output, log lines, counters and facts are all bounded where they
  come in (`sshx.maxOutput`, `ops.maxLogLine`, `traffic.maxCounters`).
- Outgoing requests go where they were told and nowhere a redirect points:
  webhooks follow none and their answers are not quoted back; a release is
  followed only to GitHub's own hosts (`xray.releaseHost`).
- Every user-visible string goes through `t()`; Russian plurals use the
  `_one/_few/_many/_other` suffixes.
- Do not show a control for something that does not work yet.
- A page explains a refused request with `explainKnown` (`lib/errors.ts`),
  naming the codes its own `<namespace>.errors` has; special cases come
  before it. `i18n/catalogue.test.ts` fails on a key the code asks for and
  the catalogue lacks.
- A page file holds the page; its dialogs and editors live next to it
  (`routing-editors.tsx`, `device-dialogs.tsx`, `template-dialogs.tsx`,
  `chain-editor.tsx`, `alerts-dialog.tsx`, `server-apply.tsx`, …) and what
  is not a component — error lists, query keys, request builders — in
  `lib/<page>.ts`. Parts several pages use are in `components/`
  (`operation-view`, `server-parts`, `failure-explained`).
- A failure's details reach the translator only through `failureValues`
  (`lib/failures.ts`): they come from the backend and from servers' output,
  and a detail called `count` or `defaultValue` must not pick the sentence.
- One of several options is a `Segmented` (a radio group with arrow keys),
  not hand-made buttons with `role="tab"`.
- Deleting anything takes two clicks: a typed name for a server, the
  `ConfirmStrip` inside the dialog for everything else. A dialog's form with
  state of its own is a component inside `DialogContent`, so closing the
  dialog forgets what was typed.
- Copying goes through `useCopy` / `copyText` (`lib/clipboard.ts`): the
  clipboard API does not exist on a page opened over plain HTTP.
- What a failed apply or update left behind is shown as the job reported it
  (`aftermath` in `lib/failures.ts`): restored, not restored, or unknown.
  Never assume "restored".
- Operation failures carry a `code`; the UI has a title and a paragraph per
  code in `server.errors`. A new code needs both, or it reads as "internal".
- Step details and log lines are technical facts in English; everything else
  the administrator reads is translated.
- The installer's jobs talk to a server through `sshx.Remote`, so they can
  be tested against `fakeServer` (`internal/install/unit_test.go`): a server
  that is not there and fails as told. A change to apply, update or their
  restore paths comes with a test there; the Docker tests cover the path
  where everything works.
- A step of an operation is looked up by the ids the job was created with
  (`session.steps`, `runUninstall`), not rebuilt from what the server looks
  like now: a job that began with a stub site keeps that step when resumed.
- A taken name is recognised with `store.IsUnique`, by the database's result
  code, never by the words of its message.
- Anything written to a shell command is either validated to a strict shape
  (`servers.ValidateTarget`, `xray.NormalizeMaskHost`) or quoted with
  `sshx.Quote`.
- A root script of several commands that changes the server starts with
  `set -e`: a shell script's status is its last command's, so without it a
  failed copy is followed by a restart that looks fine. Never judge a command
  by a pipeline (`xray version | head`) for the same reason.
- A step has a time limit (`ops.DefaultStepTimeout`, or its own `Timeout`)
  and fails with `timeout` past it; an SSH connection whose server stops
  answering is closed by the keepalive (`sshx`). Either way a job ends and
  lets go of its server instead of holding it until someone cancels.
- A job cut short by the panel restarting is marked failed (`interrupted`),
  and for an apply or an update the overview's feed says the server's state
  is unknown. Nothing is put back automatically: the server is to be looked
  at and applied again.
- Background rounds — checks, traffic, the look at devices — do their hosts
  several at a time and recover a panic per host: one that does not answer,
  or one stored row that does not hold what it should, is not everyone's
  problem.
- An operation's `finish` runs before its server is released (`ops`): until
  then nothing else may start there.
- Whether a service stayed up after a restart is asked with
  `sshx.ServiceSettled`: active, and the same process as before the wait. A
  bare `sleep N; systemctl is-active` takes a service that systemd keeps
  starting again for one that runs.
- On the way out the panel stops answering, then stops the background rounds
  and the jobs and waits for them (`shutdownGrace`) before the database is
  closed. A loop started in `main` goes through `alongside`, or it is not
  waited for.
- A form that takes a machine's address shows its SSH key first:
  `useHostKeyCheck` (`lib/host-key.ts`) and `HostKeyCheck`
  (`components/`). What was fetched and confirmed is remembered with the
  address it was for, so the inputs do not have to reset anything.
