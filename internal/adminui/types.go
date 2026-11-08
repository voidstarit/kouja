package adminui

type WarningView struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type APIError struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

type StatusResponse struct {
	Agent      AgentStatus     `json:"agent"`
	ISCSI      ISCSIInfo       `json:"iscsi"`
	Metrics    MetricsView     `json:"metrics"`
	VGs        []VGStatus      `json:"vgs"`
	Volumes    []VolumeStatus  `json:"volumes"`
	Counts     Counts          `json:"counts"`
	Operations []OperationView `json:"operations"`
	Errors     []ErrorView     `json:"errors"`
}

type MetricsView struct {
	SampledAt           string                 `json:"sampled_at"`
	Available           bool                   `json:"available"`
	RatesAvailable      bool                   `json:"rates_available"`
	Error               string                 `json:"error"`
	ReadBytes           int64                  `json:"read_bytes"`
	WriteBytes          int64                  `json:"write_bytes"`
	ReadBytesPerSecond  float64                `json:"read_bytes_per_second"`
	WriteBytesPerSecond float64                `json:"write_bytes_per_second"`
	Commands            int64                  `json:"commands"`
	CommandsPerSecond   float64                `json:"commands_per_second"`
	ErrorCount          int64                  `json:"error_count"`
	Connections         []ClientConnectionView `json:"connections"`
	ConnectionsError    string                 `json:"connections_error"`
	Targets             []TargetMetricsView    `json:"targets"`
}

type ClientConnectionView struct {
	ClientAddress string `json:"client_address"`
	ClientPort    int    `json:"client_port"`
	LocalAddress  string `json:"local_address"`
	LocalPort     int    `json:"local_port"`
	Target        string `json:"target"`
	Initiator     string `json:"initiator"`
	Ambiguous     bool   `json:"ambiguous"`
}

type TargetMetricsView struct {
	Target              string                 `json:"target"`
	VolumeOwner         string                 `json:"volume_owner"`
	VolumeID            string                 `json:"volume_id"`
	Sessions            int64                  `json:"sessions"`
	Initiators          []InitiatorMetricsView `json:"initiators"`
	LUNs                []LUNMetricsView       `json:"luns"`
	ClientAddresses     []string               `json:"client_addresses"`
	ReadBytes           int64                  `json:"read_bytes"`
	WriteBytes          int64                  `json:"write_bytes"`
	ReadBytesPerSecond  float64                `json:"read_bytes_per_second"`
	WriteBytesPerSecond float64                `json:"write_bytes_per_second"`
	Commands            int64                  `json:"commands"`
	CommandsPerSecond   float64                `json:"commands_per_second"`
	RatesAvailable      bool                   `json:"rates_available"`
	ErrorCount          int64                  `json:"error_count"`
	Errors              MetricsErrorsView      `json:"errors"`
	Error               string                 `json:"error"`
}

type InitiatorMetricsView struct {
	Initiator               string `json:"initiator"`
	RXBytes                 int64  `json:"rx_bytes"`
	TXBytes                 int64  `json:"tx_bytes"`
	CommandPDUs             int64  `json:"command_pdus"`
	ResponsePDUs            int64  `json:"response_pdus"`
	ConnectionTimeoutErrors int64  `json:"connection_timeout_errors"`
	ConnectionDigestErrors  int64  `json:"connection_digest_errors"`
}

type LUNMetricsView struct {
	LUN                    int32  `json:"lun"`
	ResourceUUID           string `json:"resource_uuid"`
	Commands               int64  `json:"commands"`
	ReadBytes              int64  `json:"read_bytes"`
	WriteBytes             int64  `json:"write_bytes"`
	Resets                 int64  `json:"resets"`
	AbortsComplete         int64  `json:"aborts_complete"`
	AbortsNoTask           int64  `json:"aborts_no_task"`
	Status                 string `json:"status"`
	StateBit               string `json:"state_bit"`
	BusyCount              int64  `json:"busy_count"`
	InCommands             int64  `json:"in_commands"`
	HighPriorityInCommands int64  `json:"high_priority_in_commands"`
	Error                  string `json:"error"`
}

type MetricsErrorsView struct {
	LoginFailures          int64 `json:"login_failures"`
	AuthenticationFailures int64 `json:"authentication_failures"`
	AuthorizationFailures  int64 `json:"authorization_failures"`
	NegotiationFailures    int64 `json:"negotiation_failures"`
	OtherLoginFailures     int64 `json:"other_login_failures"`
	AbnormalLogouts        int64 `json:"abnormal_logouts"`
	FormatErrors           int64 `json:"format_errors"`
	ConnectionErrors       int64 `json:"connection_errors"`
	DigestErrors           int64 `json:"digest_errors"`
	ConnectionTimeouts     int64 `json:"connection_timeouts"`
	ConnectionDigestErrors int64 `json:"connection_digest_errors"`
	AbortsComplete         int64 `json:"aborts_complete"`
	AbortsNoTask           int64 `json:"aborts_no_task"`
	Resets                 int64 `json:"resets"`
}

type ISCSIInfo struct {
	Enabled   bool     `json:"enabled"`
	IQNPrefix string   `json:"iqn_prefix"`
	Portals   []string `json:"portals"`
}

type AgentStatus struct {
	Version         string   `json:"version"`
	APIVersion      string   `json:"api_version"`
	HostUUID        string   `json:"host_uuid"`
	Ready           bool     `json:"ready"`
	StartedAt       string   `json:"started_at"`
	UptimeSeconds   int64    `json:"uptime_seconds"`
	ConfigPath      string   `json:"config_path"`
	ConfigChanged   bool     `json:"config_changed"`
	RestartRequired bool     `json:"restart_required"`
	RestartReason   string   `json:"restart_reason"`
	LastGoodError   string   `json:"last_good_error"`
	ListenAddress   string   `json:"listen_address"`
	AdminAddress    string   `json:"admin_address"`
	Protocols       []string `json:"protocols"`
}

type VGStatus struct {
	Name             string `json:"name"`
	UUID             string `json:"uuid"`
	State            string `json:"state"`
	StorageClass     string `json:"storage_class"`
	Configured       bool   `json:"configured"`
	TotalBytes       int64  `json:"total_bytes"`
	FreeBytes        int64  `json:"free_bytes"`
	AllocatableBytes int64  `json:"allocatable_bytes"`
	ExtentSizeBytes  int64  `json:"extent_size_bytes"`
	MaxVolumeBytes   int64  `json:"max_volume_bytes"`
	ObservedAt       string `json:"observed_at"`
	Error            string `json:"error"`
}

type VolumeStatus struct {
	Owner              string                 `json:"owner"`
	VolumeID           string                 `json:"volume_id"`
	ResourceUUID       string                 `json:"resource_uuid"`
	LVName             string                 `json:"lv_name"`
	VGName             string                 `json:"vg_name"`
	StorageClass       string                 `json:"storage_class"`
	RequestedSizeBytes int64                  `json:"requested_size_bytes"`
	ActualSizeBytes    int64                  `json:"actual_size_bytes"`
	State              string                 `json:"state"`
	Serving            bool                   `json:"serving"`
	DriftReasons       []string               `json:"drift_reasons"`
	UpdatedAt          string                 `json:"updated_at"`
	Export             *ExportView            `json:"export"`
	Observation        *ObservationView       `json:"observation"`
	ExportObservation  *ExportObservationView `json:"export_observation"`
	LastErrorCode      string                 `json:"last_error_code,omitempty"`
}

type ExportView struct {
	Protocol   string `json:"protocol"`
	Initiator  string `json:"initiator"`
	TargetName string `json:"target_name"`
	LUN        int32  `json:"lun"`
	NSID       int32  `json:"nsid"`
	Portal     string `json:"portal"`
	State      string `json:"state"`
}

type ObservationView struct {
	LVPresent         bool   `json:"lv_present"`
	ObservedSizeBytes int64  `json:"observed_size_bytes"`
	DevicePath        string `json:"device_path"`
	ObservedAt        string `json:"observed_at"`
	Source            string `json:"source"`
	Stale             bool   `json:"stale"`
}

type ExportObservationView struct {
	TargetPresent     bool    `json:"target_present"`
	NamespacePresent  bool    `json:"namespace_present"`
	ACLPresent        bool    `json:"acl_present"`
	ActiveSessions    *int32  `json:"active_sessions"`
	SessionState      string  `json:"session_state"`
	ObservedInitiator *string `json:"observed_initiator"`
	InitiatorSource   string  `json:"initiator_source"`
	ObservedAt        string  `json:"observed_at"`
	Stale             bool    `json:"stale"`
}

type Counts struct {
	Volumes             int            `json:"volumes"`
	ByState             map[string]int `json:"by_state"`
	TruncatedVolumes    bool           `json:"truncated_volumes"`
	Operations          int            `json:"operations"`
	TruncatedOperations bool           `json:"truncated_operations"`
}

type OperationView struct {
	ID                 string `json:"id"`
	Owner              string `json:"owner"`
	VolumeID           string `json:"volume_id"`
	ResourceUUID       string `json:"resource_uuid"`
	Action             string `json:"action"`
	State              string `json:"state"`
	Attempt            int    `json:"attempt"`
	MaxAttempts        int    `json:"max_attempts"`
	DeadlineAt         string `json:"deadline_at"`
	LastErrorCode      string `json:"last_error_code"`
	LastErrorRetryable bool   `json:"last_error_retryable"`
	CreatedAt          string `json:"created_at"`
	UpdatedAt          string `json:"updated_at"`
}

type ErrorView struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type ConfigResponse struct {
	Path            string        `json:"path"`
	BackupPath      string        `json:"backup_path"`
	LastGoodPath    string        `json:"last_good_path"`
	DiskYAML        string        `json:"disk_yaml"`
	RunningYAML     string        `json:"running_yaml"`
	DiskETag        string        `json:"disk_etag"`
	DiskValid       bool          `json:"disk_valid"`
	DiskError       string        `json:"disk_error"`
	DiskErrorCode   string        `json:"disk_error_code"`
	ConfigChanged   bool          `json:"config_changed"`
	RestartRequired bool          `json:"restart_required"`
	RestartReason   string        `json:"restart_reason"`
	LastGoodError   string        `json:"last_good_error"`
	Warnings        []WarningView `json:"warnings"`
	TLS             TLSInfo       `json:"tls"`
}

type TLSInfo struct {
	Subject     string   `json:"subject"`
	ServerNames []string `json:"server_names"`
	IPAddresses []string `json:"ip_addresses"`
	NotAfter    string   `json:"not_after"`
	Error       string   `json:"error"`
}

type SaveConfigResponse struct {
	Saved           bool          `json:"saved"`
	Path            string        `json:"path"`
	BackupPath      string        `json:"backup_path"`
	DiskETag        string        `json:"disk_etag"`
	ConfigChanged   bool          `json:"config_changed"`
	RestartRequired bool          `json:"restart_required"`
	RestartReason   string        `json:"restart_reason"`
	Warnings        []WarningView `json:"warnings"`
}

type VGListResponse struct {
	VGs []VGStatus `json:"vgs"`
}

type RegisterVGResponse struct {
	VG       VGStatus      `json:"vg"`
	Warnings []WarningView `json:"warnings"`
}

type ClassListResponse struct {
	Classes map[string]string `json:"classes"`
}

type SetClassResponse struct {
	Classes         map[string]string `json:"classes"`
	ConfigChanged   bool              `json:"config_changed"`
	RestartRequired bool              `json:"restart_required"`
	Warnings        []WarningView     `json:"warnings"`
}

type CertificateFile struct {
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	Role        string `json:"role"`
	SizeBytes   int64  `json:"size_bytes"`
	ModifiedAt  string `json:"modified_at"`
	DownloadURL string `json:"download_url"`
}

type CertificatesResponse struct {
	Directory string            `json:"directory"`
	Files     []CertificateFile `json:"files"`
	CA        *CertificateFile  `json:"ca"`
	Error     string            `json:"error"`
}

type CreateCertificateResponse struct {
	Certificate CertificateFile `json:"certificate"`
	PrivateKey  CertificateFile `json:"private_key"`
}
