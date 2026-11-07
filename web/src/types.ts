export interface WarningView {
  code: string;
  message: string;
}

export interface ApiErrorShape {
  error: string;
  code: string;
}

export class ApiError extends Error {
  status: number;
  code: string;

  constructor(status: number, code: string, message: string) {
    super(message);
    this.status = status;
    this.code = code;
  }
}

export interface AgentStatus {
  version: string;
  api_version: string;
  host_uuid: string;
  ready: boolean;
  started_at: string;
  uptime_seconds: number;
  config_path: string;
  config_changed: boolean;
  restart_required: boolean;
  restart_reason: string;
  last_good_error: string;
  listen_address: string;
  admin_address: string;
  protocols: string[];
}

export interface VGStatus {
  name: string;
  uuid: string;
  state: string;
  storage_class: string;
  configured: boolean;
  total_bytes: number;
  free_bytes: number;
  allocatable_bytes: number;
  extent_size_bytes: number;
  max_volume_bytes: number;
  observed_at: string;
  error: string;
}

export interface ExportView {
  protocol: string;
  initiator: string;
  target_name: string;
  lun: number;
  nsid: number;
  portal: string;
  state: string;
}

export interface ObservationView {
  lv_present: boolean;
  observed_size_bytes: number;
  device_path: string;
  observed_at: string;
  source: string;
  stale: boolean;
}

export interface ExportObservationView {
  target_present: boolean;
  namespace_present: boolean;
  acl_present: boolean;
  active_sessions: number | null;
  session_state: string;
  observed_initiator: string | null;
  initiator_source: "acl" | "session" | "none";
  observed_at: string;
  stale: boolean;
}

export interface VolumeStatus {
  owner: string;
  volume_id: string;
  resource_uuid: string;
  lv_name: string;
  vg_name: string;
  storage_class: string;
  requested_size_bytes: number;
  actual_size_bytes: number;
  state: string;
  serving: boolean;
  drift_reasons: string[];
  updated_at: string;
  export: ExportView | null;
  observation: ObservationView | null;
  export_observation: ExportObservationView | null;
  last_error_code?: string;
}

export interface OperationView {
  id: string;
  owner: string;
  volume_id: string;
  resource_uuid: string;
  action: string;
  state: string;
  attempt: number;
  max_attempts: number;
  deadline_at: string;
  last_error_code: string;
  last_error_retryable: boolean;
  created_at: string;
  updated_at: string;
}

export interface Counts {
  volumes: number;
  by_state: Record<string, number>;
  truncated_volumes: boolean;
  operations: number;
  truncated_operations: boolean;
}

export interface ErrorView {
  code: string;
  message: string;
}

export interface ISCSIInfo {
  enabled: boolean;
  iqn_prefix: string;
  portals: string[];
}

export interface MetricsErrorsView {
  login_failures: number;
  authentication_failures: number;
  authorization_failures: number;
  negotiation_failures: number;
  other_login_failures: number;
  abnormal_logouts: number;
  format_errors: number;
  connection_errors: number;
  digest_errors: number;
  connection_timeouts: number;
  connection_digest_errors: number;
  aborts_complete: number;
  aborts_no_task: number;
  resets: number;
}

export interface InitiatorMetricsView {
  initiator: string;
  rx_bytes: number;
  tx_bytes: number;
  command_pdus: number;
  response_pdus: number;
  connection_timeout_errors: number;
  connection_digest_errors: number;
}

export interface LUNMetricsView {
  lun: number;
  resource_uuid: string;
  commands: number;
  read_bytes: number;
  write_bytes: number;
  resets: number;
  aborts_complete: number;
  aborts_no_task: number;
  status: string;
  state_bit: string;
  busy_count: number;
  in_commands: number;
  high_priority_in_commands: number;
  error: string;
}

export interface ClientConnectionView {
  client_address: string;
  client_port: number;
  local_address: string;
  local_port: number;
  target: string;
  initiator: string;
  ambiguous: boolean;
}

export interface TargetMetricsView {
  target: string;
  volume_owner: string;
  volume_id: string;
  sessions: number;
  initiators: InitiatorMetricsView[];
  luns: LUNMetricsView[];
  client_addresses: string[];
  read_bytes: number;
  write_bytes: number;
  read_bytes_per_second: number;
  write_bytes_per_second: number;
  commands: number;
  commands_per_second: number;
  rates_available: boolean;
  error_count: number;
  errors: MetricsErrorsView;
  error: string;
}

export interface MetricsView {
  sampled_at: string;
  available: boolean;
  rates_available: boolean;
  error: string;
  read_bytes: number;
  write_bytes: number;
  read_bytes_per_second: number;
  write_bytes_per_second: number;
  commands: number;
  commands_per_second: number;
  error_count: number;
  connections: ClientConnectionView[];
  connections_error: string;
  targets: TargetMetricsView[];
}

export interface StatusResponse {
  agent: AgentStatus;
  iscsi: ISCSIInfo;
  metrics: MetricsView;
  vgs: VGStatus[];
  volumes: VolumeStatus[];
  counts: Counts;
  operations: OperationView[];
  errors: ErrorView[];
}

export interface TLSInfo {
  subject: string;
  server_names: string[];
  ip_addresses: string[];
  not_after: string;
  error: string;
}

export interface ConfigResponse {
  path: string;
  backup_path: string;
  last_good_path: string;
  disk_yaml: string;
  running_yaml: string;
  disk_etag: string;
  disk_valid: boolean;
  disk_error: string;
  disk_error_code: string;
  config_changed: boolean;
  restart_required: boolean;
  restart_reason: string;
  last_good_error: string;
  warnings: WarningView[];
  tls: TLSInfo;
}

export interface SaveConfigResponse {
  saved: boolean;
  path: string;
  backup_path: string;
  disk_etag: string;
  config_changed: boolean;
  restart_required: boolean;
  restart_reason: string;
  warnings: WarningView[];
}

export interface VGListResponse {
  vgs: VGStatus[];
}

export interface RegisterVGResponse {
  vg: VGStatus;
  warnings: WarningView[];
}

export interface ClassListResponse {
  classes: Record<string, string>;
}

export interface SetClassResponse {
  classes: Record<string, string>;
  config_changed: boolean;
  restart_required: boolean;
  warnings: WarningView[];
}

export interface CertificateFile {
  name: string;
  kind: "certificate" | "private_key";
  role: string;
  size_bytes: number;
  modified_at: string;
  download_url: string;
}

export interface CertificatesResponse {
  directory: string;
  files: CertificateFile[];
  ca: CertificateFile | null;
  error: string;
}

export interface StatusPageProps {
  status: StatusResponse | null;
  loading: boolean;
  error: string;
  onRefresh: () => void;
}

export interface ConfigPageProps {
  config: ConfigResponse;
  vgs: VGStatus[];
  classes: Record<string, string>;
  onSaved: (result: SaveConfigResponse) => void;
  onDirtyChange: (dirty: boolean) => void;
  onRefreshVGs: () => Promise<void>;
  onReloadConfig: () => Promise<void>;
}

export interface VgsPanelProps {
  vgs: VGStatus[];
  classes: Record<string, string>;
  classActionsDisabled: boolean;
  onRegister: (name: string) => Promise<void>;
  onDeregister: (name: string) => Promise<void>;
  onSetClass: (name: string, vg: string) => Promise<void>;
  onRemoveClass: (name: string) => Promise<void>;
}
