export interface AuthState {
  setup_required: boolean
  authenticated: boolean
}

export interface OkResponse {
  ok: true
}

export interface SetupRequest {
  /** One-time code from the panel's start-up output. */
  token: string
  username: string
  password: string
  panel_url: string
  enable_totp: boolean
}

/** What an authenticator app needs; present only when 2FA was requested. */
export interface TotpEnrollment {
  secret: string
  url: string
}

export interface SetupResponse {
  ok: true
  totp?: TotpEnrollment
}

export interface LoginRequest {
  username: string
  password: string
  /** TOTP code; sent once the server has answered `totp_required`. */
  code?: string
  remember: boolean
}

export interface Me {
  username: string
  totp_enabled: boolean
  /** A secret was issued at setup but no code has confirmed it yet. */
  totp_pending: boolean
  panel_url: string
  version: string
  /** Unix timestamp, seconds. */
  last_login_at: number | null
  last_login_ip: string | null
}

/** `details` of an `invalid_credentials` error. */
export interface InvalidCredentialsDetails {
  attempts_left: number
  lock_minutes: number
  client_ip: string
  /** Unix timestamp, seconds. Absent if nobody has signed in yet. */
  previous_login_at?: number
}

/** `details` of a `locked` error. */
export interface LockedDetails {
  retry_after_seconds: number
  client_ip: string
}

/**
 * 'uninstalling' covers a removal of Xray that is running and one that stopped
 * partway and waits to be resumed.
 */
export type ServerStatus = 'new' | 'installing' | 'failed' | 'ready' | 'uninstalling'

/** One thing the panel changed on a server. */
export interface ServerChange {
  kind: 'ssh_key' | 'ssh_password' | 'files' | 'user' | 'service' | 'firewall'
  text: string
}

/** The public half of a server's VLESS + REALITY + XHTTP settings. */
export interface ServerInbound {
  port: number
  mask_host: string
  public_key: string
  /** How many shortIds exist — not the values. */
  short_ids: number
  xhttp_path: string
  xhttp_mode: string
}

export interface Server {
  id: number
  name: string
  location: string
  host: string
  ssh_port: number
  ssh_user: string
  host_key_type: string
  host_key_fingerprint: string
  status: ServerStatus
  os_name: string
  arch: string
  xray_version: string
  inbound: ServerInbound | null
  manifest: ServerChange[]
  /** The server's most recent job, if it ever had one. */
  operation_id: number | null
  /** Unix timestamp, seconds. */
  created_at: number
  template: ServerTemplate | null
  /** Null until the server is installed and has been checked once. */
  check: ServerCheck | null
}

/** Where a server stands with the availability checks. */
export interface ServerCheck {
  state: 'up' | 'down' | 'unknown'
  /** Unix seconds: when it entered that state. */
  since: number
  last_at: number | null
  /** Plain TCP connect time of the last check that got that far. */
  tcp_ms: number | null
  /** The whole request through the server, when the last check passed. */
  request_ms: number | null
  /** Which part of the last check failed: "tcp", "request" or "". */
  stage: string
  error: string
}

/** One day of one server's checks. */
export interface UptimeDay {
  /** YYYY-MM-DD, panel-local. */
  day: string
  total: number
  ok: number
}

export interface OverviewServer {
  id: number
  name: string
  location: string
  status: ServerStatus
  check: ServerCheck | null
  /** The last 30 days, oldest first; a day without checks has total 0. */
  days: UptimeDay[]
  uptime_percent: number | null
}

export type EventKind =
  | 'server_installed'
  | 'server_removed'
  | 'config_applied'
  | 'apply_failed'
  | 'server_down'
  | 'server_up'
  | 'xray_updated'
  | 'update_failed'
  | 'xray_uninstalled'

export interface PanelEvent {
  id: number
  at: number
  kind: EventKind
  server_id: number | null
  server_name: string
  data: Record<string, unknown>
}

export interface Overview {
  counts: {
    servers: number
    up: number
    down: number
    unknown: number
    not_installed: number
  }
  latency: {
    /** Mean TCP connect time over servers that are up. */
    avg_ms: number | null
    servers: number
    interval_seconds: number
  }
  servers: OverviewServer[]
  /** All servers' traffic by day for the last seven days, oldest first. */
  traffic: TrafficDay[]
  /** How many servers count traffic at all. */
  counting: number
  events: PanelEvent[]
}

/** Bytes from clients to the servers (up) and from the servers to clients (down). */
export interface TrafficTotals {
  up: number
  down: number
}

export interface TrafficDay extends TrafficTotals {
  /** YYYY-MM-DD, by the panel's calendar. */
  day: string
}

/** What a period's checks say about how a server answers; ms over passed checks. */
export interface Latency {
  checks: number
  ok: number
  tcp_median: number | null
  request_median: number | null
  request_p95: number | null
}

export interface Outage {
  /** Unix seconds; how long it lasted — so far, when ongoing. */
  start: number
  seconds: number
  ongoing: boolean
}

export interface StatsServer {
  id: number
  name: string
  location: string
  status: ServerStatus
  check: ServerCheck | null
  traffic: TrafficTotals
  /** False for a server whose applied configuration does not count traffic yet. */
  counting: boolean
  latency: Latency
  loss_percent: number | null
  /** The last 30 days whatever the period. */
  days: UptimeDay[]
  uptime_percent: number | null
  outages: number
  last_outage: Outage | null
}

export type StatsPeriod = '24h' | '7d' | '30d'

export interface Stats {
  period: StatsPeriod
  traffic: TrafficTotals
  /** The last 30 days, oldest first. */
  traffic_days: TrafficDay[]
  availability_percent: number | null
  latency: Latency
  incidents: { count: number; seconds: number }
  servers: StatsServer[]
}

/** Where and about what the panel tells the administrator. */
export interface AlertSettings {
  enabled: boolean
  /** The bot token itself never leaves the panel. */
  telegram_token_set: boolean
  telegram_chat: string
  webhook_url: string
  on_down: boolean
  on_up: boolean
  on_update: boolean
  down_after_minutes: number
  /** 0 never, else every 1 or 6 hours. */
  repeat_hours: number
  quiet_hours: boolean
}

export type AlertSettingsRequest = Omit<AlertSettings, 'telegram_token_set'> & {
  /** null keeps the saved token, "" removes it. */
  telegram_token: string | null
}

/** "" for a channel that is not set up, "ok", or what went wrong. */
export interface AlertTestResult {
  telegram: string
  webhook: string
}

/** A server's standing with its template. */
export interface ServerTemplate {
  id: number
  name: string
  /** The template's latest version. */
  version: number
  /** The version the server runs; null before the first apply. */
  applied_version: number | null
  /** Fields waiting to be applied. */
  changed: number
  /** Fields the server sets for itself. */
  overridden: number
}

export type FieldType = 'enum' | 'string' | 'range' | 'number'

/** One template field as a form control needs it. */
export interface ConfigField {
  key: string
  type: FieldType
  options?: string[]
  /** What takes effect. */
  value: string
  /** What the server would inherit. */
  template_value: string
  source: 'template' | 'override'
  overridable: boolean
  /** Shown, but cannot be changed yet. */
  fixed: boolean
}

export interface DiffLine {
  /** " " unchanged, "-" removed, "+" added, "@" a gap of unchanged lines. */
  op: ' ' | '-' | '+' | '@'
  text: string
}

export interface HistoryEntry {
  id: number
  kind: 'install' | 'apply'
  status: OperationStatus
  started_at: number
  finished_at: number | null
  /** Field keys an apply changed. */
  changed: string[]
  error_code: string
}

/** A server's desired settings and how they differ from what it runs. */
export interface ServerConfig {
  template: { id: number; name: string; version: number }
  fields: ConfigField[]
  mask_host: string
  public_key: string
  short_ids: string[]
  /** Field keys that differ from what the server runs. */
  changed: string[]
  diff: DiffLine[]
  /** Desired config.json, private key masked. */
  config: string
  applied_at: number | null
  applied_template_version: number | null
  history: HistoryEntry[]
  busy: boolean
}

export interface UpdateConfigRequest {
  /** A string sets the server's own value; null goes back to the template's. */
  overrides?: Record<string, string | null>
  mask_host?: string
  template_id?: number
}

export interface TemplateSummary {
  id: number
  name: string
  description: string
  version: number
  server_count: number
  /** Servers with changes to apply. */
  changed: number
  /** Servers that override something. */
  overridden: number
}

export interface TemplateServer {
  id: number
  name: string
  status: ServerStatus
  state: 'synced' | 'changed' | 'not_installed'
  overrides: string[]
  changed: string[]
  applied_at: number | null
  applied_version: number | null
}

export interface TemplateVersion {
  version: number
  /** What changed from the version before, e.g. "xhttp.mode: auto → packet-up". */
  note: string
  created_at: number
}

export type OnError = 'stop' | 'continue'

export type ChainStrategy = 'leastPing' | 'roundRobin' | 'random'

/** A server as a link of a chain shows it. */
export interface ChainServer {
  id: number
  name: string
  location: string
  state: 'up' | 'down' | 'unknown' | 'not_installed' | 'missing'
  /** The panel's last connect time to the server, when it is up. */
  tcp_ms: number | null
  /** The server has changes waiting to be applied. */
  pending: boolean
  /** What is waiting: the names of the changed fields, "policies" among them. */
  changed: string[]
  /** A job, most likely an apply, is running on the server. */
  busy: boolean
}

export interface ChainHop {
  servers: ChainServer[]
  strategy: ChainStrategy
  /** Takes over when every server of the link is down. */
  reserve: ChainServer | null
}

export interface Chain {
  id: number
  name: string
  description: string
  /** Links in order from the client outward; the first is the entry. */
  hops: ChainHop[]
  /** The last request sent through the whole chain; null before the first. */
  check: { ok: boolean; at: number; ms: number; error: string } | null
  /** Names of member servers with changes to apply, in apply order. */
  pending: string[]
  batch: TemplateBatch | null
}

export interface ChainRequest {
  name: string
  description: string
  hops: { servers: number[]; strategy: ChainStrategy; reserve: number }[]
}

export type RuleConditionType =
  | 'domain'
  | 'full'
  | 'keyword'
  | 'regexp'
  | 'geosite'
  | 'ip'
  | 'geoip'

export interface RuleCondition {
  type: RuleConditionType
  value: string
}

export type RuleAction = 'chain' | 'direct' | 'block'

export interface Rule {
  /** Stable across reordering; empty for a rule not saved yet. */
  id: string
  name: string
  /** The destination matches when any one of these does. */
  conditions: RuleCondition[]
  /** "443,8000-8100"; empty is any port. */
  ports: string
  /** One of the protocols the list names; empty is any. */
  protocol: string
  action: RuleAction
  /** Set when the action is "chain". */
  chain_id: number
  enabled: boolean
}

export interface RuleSet {
  id: number
  name: string
  /** The server the rules run on: the one the set's clients connect to. */
  entry: ChainServer
  rules: Rule[]
  /** Where everything no rule claimed goes. */
  default_action: RuleAction
  default_chain_id: number
  subscriptions: number
  /** Subscriptions that work right now. */
  active: number
}

export interface RuleSetRequest {
  name: string
  entry_server_id: number
  rules: Rule[]
  default_action: RuleAction
  default_chain_id: number
}

export interface Subscription {
  id: number
  name: string
  rule_set_id: number
  rule_set_name: string
  /** The server the subscription's user lives on; null if its rule set is gone. */
  entry: ChainServer | null
  enabled: boolean
  /** Unix seconds; null for no end date. */
  expires_at: number | null
  state: 'active' | 'disabled' | 'expired'
  /** The subscription link without the panel's address: /s/<token>. */
  path: string
  /** The connection itself as a vless:// link; empty while the entry has no Xray. */
  link: string
  /** Whether the entry server, as last applied, lets this subscription in. */
  on_server: boolean
  /** What the subscription carried over the last 30 days. */
  traffic: TrafficTotals
  created_at: number
  /** When an app last fetched the link; null if never. */
  fetched_at: number | null
}

export interface SubscriptionRequest {
  name: string
  rule_set_id: number
  enabled: boolean
  expires_at: number | null
}

/** A machine on the administrator's side with an Xray client the panel keeps. */
export interface Device {
  id: number
  name: string
  host: string
  ssh_port: number
  ssh_user: string
  rule_set_id: number
  rule_set_name: string
  /** The server the device connects to; null if its rule set is gone. */
  entry: ChainServer | null
  socks_port: number
  http_port: number
  /** The proxies listen on every interface, for the device's network. */
  lan: boolean
  /** Changed settings are sent without being asked. */
  auto_push: boolean
  status: 'new' | 'working' | 'ready' | 'failed'
  /** The step a job is on, or stopped at. */
  step: string
  os_name: string
  arch: string
  xray_version: string
  failure: OperationFailure | null
  log: string
  /** What the last look at the device found. */
  online: boolean
  service: string
  seen_at: number | null
  /** The device runs something other than it should. */
  pending: boolean
  traffic: TrafficTotals
}

export interface DeviceSettings {
  name: string
  rule_set_id: number
  socks_port: number
  http_port: number
  lan: boolean
  auto_push: boolean
}

export interface NewDevice extends DeviceSettings {
  host: string
  ssh_port: number
  ssh_user: string
  /** The key the administrator saw and accepted. */
  host_key: string
  /** Used once, to put the panel's key on the device. */
  password: string
}

/** The client inside the panel, for the computer the panel runs on. */
export interface LocalClient {
  enabled: boolean
  rule_set_id: number
  socks_port: number
  http_port: number
  state: 'off' | 'running' | 'failed'
  /** Why it is "failed": port_in_use | no_entry | start_failed. */
  error: string
  detail: string
  /** When a request was last sent through it, and how that went. */
  checked_at: number | null
  check_ok: boolean
  check_error: string
  entry: ChainServer | null
  traffic: TrafficTotals
}

export interface DevicesResponse {
  devices: Device[]
  local: LocalClient
}

/** Which Xray release is the newest, and who is behind it. */
export interface XrayRelease {
  /** Empty when the panel could not find out. */
  latest: string
  outdated: { id: number; name: string; version: string }[]
  /** Progress of the last "update all", if there was one. */
  batch: TemplateBatch | null
}

/** Progress of applying changes across several servers. */
export interface TemplateBatch {
  on_error: OnError
  total: number
  done: number
  /** Name of the server being worked on. */
  current: string
  failed: string[]
  running: boolean
}

export interface TemplateDetail {
  template: TemplateSummary
  fields: ConfigField[]
  servers: TemplateServer[]
  versions: TemplateVersion[]
  batch: TemplateBatch | null
}

export interface UpdateTemplateRequest {
  name?: string
  description?: string
  values?: Record<string, string>
  overridable?: string[]
}

export interface PanelKey {
  name: string
  algorithm: string
  /** A complete authorized_keys line. */
  public_key: string
  fingerprint: string
}

export interface HostKeyProbe {
  type: string
  fingerprint: string
  /** Opaque; sent back unchanged to confirm this is the server meant. */
  host_key: string
}

export type AuthMethod = 'password' | 'panel_key'

export interface InstallOptions {
  auth: AuthMethod
  /** Only with auth "password". Used once and not stored. */
  password?: string
  disable_password_login: boolean
  mask_host: string
  /** Omitted or 0: the panel's default template. */
  template_id?: number
}

export interface CreateServerRequest extends InstallOptions {
  name: string
  location: string
  host: string
  ssh_port: number
  ssh_user: string
  host_key: string
}

export type StepId =
  | 'stage'
  | 'swap'
  | 'verify'
  | 'ssh'
  | 'system'
  | 'xray'
  | 'keys'
  | 'config'
  | 'service'
  | 'firewall'
  | 'check'

export type StepStatus = 'pending' | 'running' | 'done' | 'failed' | 'undone'

export interface StepState {
  id: StepId
  status: StepStatus
  /** A short technical fact about the outcome; shown as is. */
  detail: string
  started_at?: number
  finished_at?: number
}

export interface OperationFailure {
  /** Picks the explanation shown to the administrator. */
  code: string
  step: StepId | ''
  /** Raw text, for the log. */
  message: string
  details?: Record<string, unknown>
}

export type OperationStatus = 'running' | 'succeeded' | 'failed' | 'rolled_back'

export interface Operation {
  id: number
  server_id: number
  kind: string
  status: OperationStatus
  steps: StepState[]
  error: OperationFailure | null
  /** What the job was asked to do; which keys are there depends on its kind. */
  params: Partial<InstallOptions> & {
    /** update: the release replaced and the one put in its place. */
    from?: string
    to?: string
    /** uninstall */
    remove_backups?: boolean
  }
  result: Record<string, unknown>
  /** Unix timestamps, seconds. */
  started_at: number
  finished_at: number | null
}

export interface LogLine {
  seq: number
  at: number
  step: string
  kind: 'cmd' | 'out' | 'info' | 'error'
  text: string
}

export interface OperationFeed {
  operation: Operation
  log: LogLine[]
}

export class ApiError extends Error {
  readonly status: number
  /** Stable machine-readable code; the UI translates by this, not by message. */
  readonly code: string
  readonly details: Record<string, unknown>

  constructor(
    status: number,
    code: string,
    message: string,
    details: Record<string, unknown> = {},
  ) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
    this.details = details
  }
}

type Method = 'GET' | 'POST' | 'PUT' | 'DELETE'

interface RequestOptions {
  /**
   * When false, a 401 response does not redirect to /login.
   * Used by the auth endpoints themselves.
   */
  redirectOn401?: boolean
}

interface ErrorBody {
  error: {
    code: string
    message: string
    details?: Record<string, unknown>
  }
}

function isErrorBody(data: unknown): data is ErrorBody {
  if (typeof data !== 'object' || data === null || !('error' in data)) {
    return false
  }
  const error = (data as { error: unknown }).error
  return (
    typeof error === 'object' &&
    error !== null &&
    typeof (error as { code?: unknown }).code === 'string' &&
    typeof (error as { message?: unknown }).message === 'string'
  )
}

function redirectToLogin(): void {
  if (window.location.pathname !== '/login') {
    window.location.assign('/login')
  }
}

async function request<T>(
  method: Method,
  path: string,
  body?: unknown,
  options: RequestOptions = {},
): Promise<T> {
  const headers: Record<string, string> = {}
  if (method !== 'GET') {
    // CSRF defence: the backend rejects state-changing requests without it.
    headers['X-XC-Request'] = '1'
  }
  if (body !== undefined) {
    headers['Content-Type'] = 'application/json'
  }

  const response = await fetch(path, {
    method,
    credentials: 'same-origin',
    headers,
    body: body !== undefined ? JSON.stringify(body) : undefined,
  })

  if (response.ok) {
    return (await response.json()) as T
  }

  let code = 'unknown_error'
  let message = `Request failed with status ${response.status}`
  let details: Record<string, unknown> = {}
  try {
    const data: unknown = await response.json()
    if (isErrorBody(data)) {
      code = data.error.code
      message = data.error.message
      details = data.error.details ?? {}
    }
  } catch {
    // Non-JSON error body; keep the fallback code and message.
  }

  if (response.status === 401 && options.redirectOn401 !== false) {
    redirectToLogin()
  }

  throw new ApiError(response.status, code, message, details)
}

export const api = {
  getAuthState: () =>
    request<AuthState>('GET', '/api/auth/state', undefined, { redirectOn401: false }),

  setup: (body: SetupRequest) =>
    request<SetupResponse>('POST', '/api/setup', body, { redirectOn401: false }),

  login: (body: LoginRequest) =>
    request<OkResponse>('POST', '/api/auth/login', body, { redirectOn401: false }),

  logout: () =>
    request<OkResponse>('POST', '/api/auth/logout', undefined, { redirectOn401: false }),

  confirmTotp: (code: string) =>
    request<OkResponse>('POST', '/api/auth/totp/confirm', { code }),

  me: () => request<Me>('GET', '/api/me'),

  getPanelKey: () => request<PanelKey>('GET', '/api/ssh/panel-key'),

  probeServer: (host: string, sshPort: number) =>
    request<HostKeyProbe>('POST', '/api/servers/probe', { host, ssh_port: sshPort }),

  listServers: () => request<{ servers: Server[] }>('GET', '/api/servers'),

  getServer: (id: number) => request<Server>('GET', `/api/servers/${id}`),

  createServer: (body: CreateServerRequest) =>
    request<{ server: Server; operation: Operation }>('POST', '/api/servers', body),

  installServer: (id: number, body: InstallOptions) =>
    request<{ server: Server; operation: Operation }>(
      'POST',
      `/api/servers/${id}/install`,
      body,
    ),

  acceptHostKey: (id: number, hostKey: string) =>
    request<OkResponse>('POST', `/api/servers/${id}/host-key`, { host_key: hostKey }),

  deleteServer: (id: number, name: string) =>
    request<{ ok: true; access_removed: boolean }>('DELETE', `/api/servers/${id}`, {
      name,
    }),

  getOperation: (id: number) => request<OperationFeed>('GET', `/api/operations/${id}`),

  /** Takes Xray off the server; the server leaves the panel when it is done. */
  uninstallServer: (id: number, body: { name: string; remove_backups: boolean }) =>
    request<{ operation: Operation }>('POST', `/api/servers/${id}/uninstall`, body),

  getRelease: () => request<XrayRelease>('GET', '/api/xray/release'),

  /** Updates one server to the newest release. */
  updateXray: (id: number) =>
    request<{ operation: Operation }>('POST', `/api/servers/${id}/xray/update`),

  /** Updates every outdated server, one at a time. */
  updateAllXray: (onError: OnError) =>
    request<XrayRelease>('POST', '/api/xray/update', { on_error: onError }),

  retryOperation: (id: number, password?: string) =>
    request<OkResponse>('POST', `/api/operations/${id}/retry`, {
      password: password ?? '',
    }),

  rollbackOperation: (id: number) =>
    request<OkResponse>('POST', `/api/operations/${id}/rollback`),

  cancelOperation: (id: number) =>
    request<OkResponse>('POST', `/api/operations/${id}/cancel`),

  getServerConfig: (id: number) =>
    request<ServerConfig>('GET', `/api/servers/${id}/config`),

  updateServerConfig: (id: number, body: UpdateConfigRequest) =>
    request<ServerConfig>('PUT', `/api/servers/${id}/config`, body),

  resetServerConfig: (id: number) =>
    request<ServerConfig>('POST', `/api/servers/${id}/config/reset`),

  applyServerConfig: (id: number) =>
    request<{ operation: Operation }>('POST', `/api/servers/${id}/config/apply`),

  addShortId: (id: number) =>
    request<ServerConfig>('POST', `/api/servers/${id}/short-ids`),

  removeShortId: (id: number, shortId: string) =>
    request<ServerConfig>(
      'DELETE',
      `/api/servers/${id}/short-ids/${encodeURIComponent(shortId)}`,
    ),

  rotateKeys: (id: number) =>
    request<ServerConfig>('POST', `/api/servers/${id}/rotate-keys`),

  restartXray: (id: number) =>
    request<{ state: string }>('POST', `/api/servers/${id}/restart`),

  getJournal: (id: number) =>
    request<{ text: string }>('GET', `/api/servers/${id}/journal`),

  listChains: () => request<{ chains: Chain[] }>('GET', '/api/chains'),

  createChain: (body: ChainRequest) => request<Chain>('POST', '/api/chains', body),

  updateChain: (id: number, body: ChainRequest) =>
    request<Chain>('PUT', `/api/chains/${id}`, body),

  deleteChain: (id: number) => request<OkResponse>('DELETE', `/api/chains/${id}`),

  /** Sends one request through the chain now. */
  checkChain: (id: number) => request<Chain>('POST', `/api/chains/${id}/check`),

  /** Applies pending changes on the chain's servers, far end first. */
  applyChain: (id: number) => request<Chain>('POST', `/api/chains/${id}/apply`),

  listRuleSets: () =>
    request<{ rule_sets: RuleSet[]; protocols: string[] }>('GET', '/api/rule-sets'),

  createRuleSet: (body: RuleSetRequest) =>
    request<RuleSet>('POST', '/api/rule-sets', body),

  updateRuleSet: (id: number, body: RuleSetRequest) =>
    request<RuleSet>('PUT', `/api/rule-sets/${id}`, body),

  deleteRuleSet: (id: number) => request<OkResponse>('DELETE', `/api/rule-sets/${id}`),

  listSubscriptions: () =>
    request<{ subscriptions: Subscription[] }>('GET', '/api/subscriptions'),

  createSubscription: (body: SubscriptionRequest) =>
    request<Subscription>('POST', '/api/subscriptions', body),

  updateSubscription: (id: number, body: SubscriptionRequest) =>
    request<Subscription>('PUT', `/api/subscriptions/${id}`, body),

  /** A new link and a new user; the old link stops answering at once. */
  reissueSubscription: (id: number) =>
    request<Subscription>('POST', `/api/subscriptions/${id}/reissue`),

  deleteSubscription: (id: number) =>
    request<OkResponse>('DELETE', `/api/subscriptions/${id}`),

  listDevices: () => request<DevicesResponse>('GET', '/api/devices'),

  /** Adds a device and starts setting it up. */
  createDevice: (body: NewDevice) => request<DevicesResponse>('POST', '/api/devices', body),

  updateDevice: (id: number, body: DeviceSettings) =>
    request<DevicesResponse>('PUT', `/api/devices/${id}`, body),

  /** Runs the set-up again from the start. */
  setupDevice: (id: number, password: string) =>
    request<DevicesResponse>('POST', `/api/devices/${id}/setup`, { password }),

  /** Sends the device its current settings. */
  pushDevice: (id: number) => request<DevicesResponse>('POST', `/api/devices/${id}/push`),

  deleteDevice: (id: number) =>
    request<{ ok: true; cleaned: boolean }>('DELETE', `/api/devices/${id}`),

  updateLocalClient: (body: {
    enabled: boolean
    rule_set_id: number
    socks_port: number
    http_port: number
  }) => request<DevicesResponse>('PUT', '/api/local-client', body),

  getOverview: () => request<Overview>('GET', '/api/overview'),

  getStats: (period: StatsPeriod) => request<Stats>('GET', `/api/stats?period=${period}`),

  getAlerts: () => request<AlertSettings>('GET', '/api/alerts'),

  updateAlerts: (body: AlertSettingsRequest) =>
    request<AlertSettings>('PUT', '/api/alerts', body),

  /** Sends a test message to every channel that is set up. */
  testAlerts: () => request<AlertTestResult>('POST', '/api/alerts/test'),

  /** Runs one round of checks now and returns the overview after it. */
  runChecks: () => request<Overview>('POST', '/api/checks/run'),

  listTemplates: () => request<{ templates: TemplateSummary[] }>('GET', '/api/templates'),

  getTemplate: (id: number) => request<TemplateDetail>('GET', `/api/templates/${id}`),

  createTemplate: (body: { name: string; description: string; from_server_id?: number }) =>
    request<TemplateDetail>('POST', '/api/templates', body),

  updateTemplate: (id: number, body: UpdateTemplateRequest) =>
    request<TemplateDetail>('PUT', `/api/templates/${id}`, body),

  revertTemplate: (id: number, version: number) =>
    request<TemplateDetail>('POST', `/api/templates/${id}/revert`, { version }),

  deleteTemplate: (id: number) => request<OkResponse>('DELETE', `/api/templates/${id}`),

  applyTemplate: (id: number, onError: OnError) =>
    request<TemplateDetail>('POST', `/api/templates/${id}/apply`, { on_error: onError }),
}
