package v2

import (
	"encoding/json"
	"time"
)

const (
	Version                       = "2.0"
	MethodAgentReport             = "agent.report"
	MethodAgentBasicInfo          = "agent.basicInfo"
	MethodAgentPingResult         = "agent.pingResult"
	MethodAgentTaskResult         = "agent.taskResult"
	MethodAgentExec               = "agent.exec"
	MethodAgentPing               = "agent.ping"
	MethodAgentMessage            = "agent.message"
	MethodAgentEvent              = "agent.event"
	MethodAgentTerminal           = "agent.terminal.request"
	MethodAgentPull               = "agent.pull"
	MethodAgentFile               = "agent.file"
	MethodAgentFileResult         = "agent.file.result"
	MethodAgentCarrierRouteProbe  = "agent.carrierRouteProbe"
	MethodAgentCarrierRouteResult = "agent.carrierRouteResult"
)

type Request struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
	ID      any    `json:"id,omitempty"`
}

type Response struct {
	JSONRPC string    `json:"jsonrpc"`
	ID      any       `json:"id,omitempty"`
	Result  any       `json:"result,omitempty"`
	Error   *RPCError `json:"error,omitempty"`
}

type Event struct {
	ID        string    `json:"id"`
	Method    string    `json:"method"`
	Params    any       `json:"params,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type ReportParams struct {
	Report      Report   `json:"report"`
	AckEventIDs []string `json:"ack_event_ids,omitempty"`
}

type Message struct {
	Type      string `json:"type"`
	Content   string `json:"content"`
	Sender    string `json:"sender"`
	Timestamp int64  `json:"timestamp"`
}

type IPAddress struct {
	Ipv4 string `json:"ipv4"`
	Ipv6 string `json:"ipv6"`
}

type Report struct {
	UUID        string            `json:"uuid,omitempty"`
	CPU         CPUReport         `json:"cpu"`
	Ram         RamReport         `json:"ram"`
	Swap        RamReport         `json:"swap"`
	Load        LoadReport        `json:"load"`
	Disk        DiskReport        `json:"disk"`
	Network     NetworkReport     `json:"network"`
	Connections ConnectionsReport `json:"connections"`
	GPU         *GPUDetailReport  `json:"gpu,omitempty"`
	Uptime      int64             `json:"uptime"`
	Process     int               `json:"process"`
	Message     string            `json:"message"`
	Method      string            `json:"method,omitempty"`
	UpdatedAt   time.Time         `json:"updated_at"`
}

type CPUReport struct {
	Name  string  `json:"name,omitempty"`
	Cores int     `json:"cores,omitempty"`
	Arch  string  `json:"arch,omitempty"`
	Usage float64 `json:"usage,omitempty"`
}

type GPUDetailReport struct {
	Count        int             `json:"count"`
	AverageUsage float64         `json:"average_usage"`
	DetailedInfo []GPUDeviceInfo `json:"detailed_info"`
}

type GPUDeviceInfo struct {
	Name        string  `json:"name"`
	MemoryTotal int64   `json:"memory_total"`
	MemoryUsed  int64   `json:"memory_used"`
	Utilization float64 `json:"utilization"`
	Temperature int     `json:"temperature"`
}

type RamReport struct {
	Total int64 `json:"total"`
	Used  int64 `json:"used"`
}

type LoadReport struct {
	Load1  float64 `json:"load1"`
	Load5  float64 `json:"load5"`
	Load15 float64 `json:"load15"`
}

type DiskReport struct {
	Total int64 `json:"total"`
	Used  int64 `json:"used"`
}

type NetworkReport struct {
	Up        int64 `json:"up"`
	Down      int64 `json:"down"`
	TotalUp   int64 `json:"totalUp"`
	TotalDown int64 `json:"totalDown"`
}

type ConnectionsReport struct {
	TCP int `json:"tcp"`
	UDP int `json:"udp"`
}

type BasicInfoParams struct {
	Info map[string]interface{} `json:"info"`
}

type PingResultParams struct {
	TaskID     uint      `json:"task_id"`
	PingType   string    `json:"ping_type"`
	Value      int       `json:"value"`
	FinishedAt time.Time `json:"finished_at"`
}

type TaskResultParams struct {
	TaskID     string    `json:"task_id"`
	Result     string    `json:"result"`
	ExitCode   int       `json:"exit_code"`
	FinishedAt time.Time `json:"finished_at"`
}

type PullParams struct {
	Capabilities []string `json:"capabilities,omitempty"`
	AckEventIDs  []string `json:"ack_event_ids,omitempty"`
	LastEventID  string   `json:"last_event_id,omitempty"`
}

type ExecParams struct {
	TaskID  string `json:"task_id"`
	Command string `json:"command"`
}

type PingParams struct {
	TaskID uint   `json:"ping_task_id"`
	Type   string `json:"ping_type"`
	Target string `json:"ping_target"`
}

// CarrierRouteTarget is a bounded, server-selected destination used for a
// carrier route probe. Agents never execute a command supplied by the target.
type CarrierRouteTarget struct {
	ID         string `json:"id"`
	Region     string `json:"region,omitempty"`
	Carrier    string `json:"carrier"`
	Host       string `json:"host"`
	BackupHost string `json:"backup_host,omitempty"`
	Port       int    `json:"port,omitempty"`
}

type CarrierRouteProbeParams struct {
	JobID          string               `json:"job_id"`
	Family         string               `json:"family"`
	Targets        []CarrierRouteTarget `json:"targets"`
	TimeoutMs      int                  `json:"timeout_ms"`
	MaxHops        int                  `json:"max_hops"`
	MaxConcurrency int                  `json:"max_concurrency"`
}

type CarrierRouteEntry struct {
	TargetID    string                 `json:"target_id"`
	Region      string                 `json:"region,omitempty"`
	Carrier     string                 `json:"carrier"`
	Family      string                 `json:"family"`
	Target      string                 `json:"target"`
	Route       string                 `json:"route,omitempty"`
	RoutePath   []string               `json:"route_path,omitempty"`
	Trace       []CarrierRouteTraceHop `json:"trace,omitempty"`
	Status      string                 `json:"status"`
	LatencyMs   *float64               `json:"latency_ms,omitempty"`
	LossPercent *float64               `json:"loss_percent,omitempty"`
	Sent        int                    `json:"sent,omitempty"`
	Received    int                    `json:"received,omitempty"`
	CheckedAt   time.Time              `json:"checked_at"`
	Error       string                 `json:"error,omitempty"`
}

// CarrierRouteTraceHop is safe to expose through the public API. Address is
// already masked by Agent; Core never receives the original hop address.
type CarrierRouteTraceHop struct {
	Hop      int      `json:"hop"`
	Address  string   `json:"address,omitempty"`
	ASN      string   `json:"asn,omitempty"`
	Network  string   `json:"network,omitempty"`
	RTTMs    *float64 `json:"rtt_ms,omitempty"`
	TimedOut bool     `json:"timed_out,omitempty"`
}

type CarrierRouteProbeResult struct {
	JobID      string              `json:"job_id"`
	Family     string              `json:"family"`
	Results    []CarrierRouteEntry `json:"results"`
	StartedAt  time.Time           `json:"started_at"`
	FinishedAt time.Time           `json:"finished_at"`
	Error      string              `json:"error,omitempty"`
}

type MessageParams struct {
	Type    string `json:"type"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type EventParams struct {
	Type string `json:"type"`
	Data any    `json:"data,omitempty"`
}

type TerminalRequestParams struct {
	RequestID string `json:"request_id"`
}

// FileOperation is metadata-only. File contents travel through the dedicated
// HTTP transfer endpoint rather than through JSON-RPC.
type FileOperation struct {
	UUID      string         `json:"uuid"`
	RequestID string         `json:"request_id"`
	Op        string         `json:"op"`
	Args      map[string]any `json:"args,omitempty"`
}

type FileResult struct {
	UUID      string          `json:"uuid"`
	RequestID string          `json:"request_id"`
	OK        bool            `json:"ok"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     string          `json:"error,omitempty"`
}

func Success(id any, result any) Response {
	return Response{JSONRPC: Version, ID: id, Result: result}
}

func Error(id any, code int, message string, data any) Response {
	return Response{JSONRPC: Version, ID: id, Error: &RPCError{Code: code, Message: message, Data: data}}
}
