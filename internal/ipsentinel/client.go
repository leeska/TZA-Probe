package ipsentinel

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"time"
)

const (
	DefaultSocketPath  = "/run/ipsentinel/control.sock"
	DefaultDialTimeout = 1 * time.Second
	DefaultCallTimeout = 5 * time.Second
)

// NewUUID returns a random RFC 4122 v4 UUID.
func NewUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return formatUUID(b)
}

func formatUUID(b [16]byte) string {
	return fmt.Sprintf("%s-%s-%s-%s-%s",
		hex.EncodeToString(b[0:4]),
		hex.EncodeToString(b[4:6]),
		hex.EncodeToString(b[6:8]),
		hex.EncodeToString(b[8:10]),
		hex.EncodeToString(b[10:16]),
	)
}

// RPCError is a business rejection returned by IPSentinel with HTTP 200 and ok:false.
type RPCError struct {
	Method string
	Err    Error
}

func (e *RPCError) Error() string {
	return fmt.Sprintf("ipsentinel %s error (%s): %s", e.Method, e.Err.Code, e.Err.Message)
}

// TransportError represents an IPC / HTTP / protocol boundary failure.
// A network disconnect or timeout means executor state is unknown, never a task failure.
type TransportError struct {
	HTTPStatus int
	Err        error
}

func (e *TransportError) Error() string {
	if e.HTTPStatus != 0 {
		return fmt.Sprintf("ipsentinel HTTP %d: %v", e.HTTPStatus, e.Err)
	}
	return fmt.Sprintf("ipsentinel transport error: %v", e.Err)
}

func (e *TransportError) Unwrap() error { return e.Err }

// IsTransportError reports whether err is a transport/IPC error (state unknown).
func IsTransportError(err error) bool {
	var te *TransportError
	return errors.As(err, &te)
}

// Client communicates with a local IPSentinel daemon over Unix domain socket HTTP/1.1.
type Client struct {
	SocketPath  string
	Target      Target
	DialTimeout time.Duration
	CallTimeout time.Duration
	httpClient  *http.Client
}

// NewClient creates a UDS client bound to socketPath and target.
func NewClient(socketPath string, target Target) *Client {
	if socketPath == "" {
		socketPath = DefaultSocketPath
	}
	c := &Client{
		SocketPath:  socketPath,
		Target:      target,
		DialTimeout: DefaultDialTimeout,
		CallTimeout: DefaultCallTimeout,
	}
	c.httpClient = &http.Client{
		Transport: &http.Transport{
			DisableCompression: true,
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				d := net.Dialer{Timeout: c.dialTimeout()}
				connection, err := d.DialContext(ctx, "unix", c.SocketPath)
				if err != nil {
					return nil, err
				}
				if err := c.handshake(connection); err != nil {
					connection.Close()
					return nil, err
				}
				return connection, nil
			},
			MaxConnsPerHost:        2,
			MaxIdleConns:           2,
			IdleConnTimeout:        30 * time.Second,
			MaxResponseHeaderBytes: 8192,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return c
}

func (c *Client) Close() { c.httpClient.CloseIdleConnections() }

func (c *Client) handshake(connection net.Conn) error {
	if err := connection.SetDeadline(time.Now().Add(c.callTimeout())); err != nil {
		return err
	}
	defer connection.SetDeadline(time.Time{})
	requestID := NewUUID()
	envelope := Request{Protocol: Protocol, RequestID: requestID, Method: MethodHello, Target: c.Target, Params: json.RawMessage(`{}`)}
	body, err := json.Marshal(envelope)
	if err != nil {
		return err
	}
	request, err := http.NewRequest(http.MethodPost, "http://unix/v1/call", bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	if err := request.Write(connection); err != nil {
		return err
	}
	response, err := http.ReadResponse(bufio.NewReader(io.LimitReader(connection, MaxResponseBytes+8192)), request)
	if err != nil {
		return errors.New("hello HTTP response invalid")
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, MaxResponseBytes+1))
	if err != nil || len(raw) > MaxResponseBytes || response.StatusCode != http.StatusOK {
		return errors.New("hello transport failed")
	}
	validated, err := ValidateResponse(raw, nil)
	if err != nil {
		return errors.New("hello protocol invalid")
	}
	if validated.RequestID != requestID || validated.Method != MethodHello {
		return errors.New("hello envelope mismatch")
	}
	if !validated.OK {
		return &RPCError{Method: MethodHello, Err: *validated.Error}
	}
	return nil
}

func (c *Client) dialTimeout() time.Duration {
	if c.DialTimeout > 0 {
		return c.DialTimeout
	}
	return DefaultDialTimeout
}

func (c *Client) callTimeout() time.Duration {
	if c.CallTimeout > 0 {
		return c.CallTimeout
	}
	return DefaultCallTimeout
}

// Call invokes one RPC method over POST /v1/call and strictly decodes the response.
func (c *Client) Call(ctx context.Context, reqID, method string, params any, out any) error {
	if reqID == "" {
		reqID = NewUUID()
	}
	if params == nil {
		params = map[string]any{}
	}
	rawParams, err := json.Marshal(params)
	if err != nil {
		return err
	}
	reqEnvelope := Request{
		Protocol:  Protocol,
		RequestID: reqID,
		Method:    method,
		Target:    c.Target,
		Params:    rawParams,
	}
	body, err := json.Marshal(reqEnvelope)
	if err != nil {
		return err
	}
	if len(body) > MaxRequestBytes {
		return &TransportError{Err: errors.New("request exceeds 64 KiB limit")}
	}
	if _, err := ValidateRequest(body, nil); err != nil {
		return err
	}
	callCtx, cancel := context.WithTimeout(ctx, c.callTimeout())
	defer cancel()

	httpReq, err := http.NewRequestWithContext(callCtx, http.MethodPost, "http://unix/v1/call", bytes.NewReader(body))
	if err != nil {
		return &TransportError{Err: err}
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return &TransportError{Err: err}
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponseBytes+1))
	if err != nil {
		return &TransportError{Err: err}
	}
	if len(respBytes) > MaxResponseBytes {
		return &TransportError{Err: errors.New("response exceeds 1 MiB limit")}
	}
	if resp.StatusCode != http.StatusOK {
		return &TransportError{HTTPStatus: resp.StatusCode, Err: fmt.Errorf("unexpected HTTP status %d", resp.StatusCode)}
	}
	mediaType, _, mediaErr := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if mediaErr != nil || mediaType != "application/json" || resp.Header.Get("Content-Encoding") != "" {
		return &TransportError{Err: errors.New("protocol error: invalid response media type")}
	}
	validated, err := ValidateResponse(respBytes, nil)
	if err != nil {
		return &TransportError{Err: fmt.Errorf("protocol error: %w", err)}
	}
	if validated.Protocol != Protocol || validated.RequestID != reqID || validated.Method != method {
		return &TransportError{Err: errors.New("protocol error: response envelope mismatch")}
	}
	if !validated.OK {
		return &RPCError{Method: method, Err: *validated.Error}
	}
	if out != nil {
		if err := DecodeStrict(validated.Result, out); err != nil {
			return &TransportError{Err: fmt.Errorf("protocol error decoding result: %w", err)}
		}
	}
	if method == MethodRunGet || method == MethodRunCancel || method == MethodRunSubmit {
		var requested struct {
			JobID string `json:"job_id"`
		}
		if err := json.Unmarshal(rawParams, &requested); err != nil {
			return err
		}
		var actual Run
		if method == MethodRunSubmit {
			var admission Admission
			if err := json.Unmarshal(validated.Result, &admission); err != nil {
				return err
			}
			actual = admission.Run
		} else if err := json.Unmarshal(validated.Result, &actual); err != nil {
			return err
		}
		if requested.JobID != actual.JobID {
			return &TransportError{Err: errors.New("protocol error: job mismatch")}
		}
	}
	return nil
}

func (c *Client) Hello(ctx context.Context, reqID string) (Hello, error) {
	var out Hello
	err := c.Call(ctx, reqID, MethodHello, map[string]any{}, &out)
	return out, err
}

func (c *Client) StatusGet(ctx context.Context, reqID string) (Status, error) {
	var out Status
	err := c.Call(ctx, reqID, MethodStatusGet, map[string]any{}, &out)
	return out, err
}

func (c *Client) ConfigValidate(ctx context.Context, reqID string, cfg Config) (Validation, error) {
	var out Validation
	err := c.Call(ctx, reqID, MethodConfigValidate, map[string]any{"config": cfg}, &out)
	return out, err
}

func (c *Client) ConfigApply(ctx context.Context, reqID string, fence Fence, expectedRevision int64, cfg Config) (ConfigReceipt, error) {
	var out ConfigReceipt
	params := map[string]any{
		"fence":             fence,
		"expected_revision": expectedRevision,
		"config":            cfg,
	}
	err := c.Call(ctx, reqID, MethodConfigApply, params, &out)
	return out, err
}

func (c *Client) RunSubmit(ctx context.Context, reqID string, job JobParams) (Admission, error) {
	var out Admission
	err := c.Call(ctx, reqID, MethodRunSubmit, job, &out)
	return out, err
}

func (c *Client) RunGet(ctx context.Context, reqID, jobID string) (Run, error) {
	var out Run
	err := c.Call(ctx, reqID, MethodRunGet, map[string]any{"job_id": jobID}, &out)
	return out, err
}

func (c *Client) RunCancel(ctx context.Context, reqID string, fence Fence, jobID string) (Run, error) {
	var out Run
	params := map[string]any{
		"fence":  fence,
		"job_id": jobID,
		"reason": "operator",
	}
	err := c.Call(ctx, reqID, MethodRunCancel, params, &out)
	return out, err
}

func (c *Client) ControlPause(ctx context.Context, reqID string, fence Fence) (Control, error) {
	var out Control
	params := map[string]any{
		"fence":  fence,
		"reason": "operator",
	}
	err := c.Call(ctx, reqID, MethodControlPause, params, &out)
	return out, err
}

func (c *Client) ControlResume(ctx context.Context, reqID string, fence Fence, expectedPauseGeneration int64) (Control, error) {
	var out Control
	params := map[string]any{
		"fence":                     fence,
		"expected_pause_generation": expectedPauseGeneration,
	}
	err := c.Call(ctx, reqID, MethodControlResume, params, &out)
	return out, err
}

func (c *Client) RecordsList(ctx context.Context, reqID string, afterSeq, limit int64) (RecordPage, error) {
	var out RecordPage
	params := map[string]any{
		"after_seq": afterSeq,
		"limit":     limit,
	}
	err := c.Call(ctx, reqID, MethodRecordsList, params, &out)
	return out, err
}

func (c *Client) RecordsAck(ctx context.Context, reqID string, fence Fence, throughSeq int64) (Ack, error) {
	var out Ack
	params := map[string]any{
		"fence":       fence,
		"through_seq": throughSeq,
	}
	err := c.Call(ctx, reqID, MethodRecordsAck, params, &out)
	return out, err
}
