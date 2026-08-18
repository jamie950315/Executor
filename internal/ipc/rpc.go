package ipc

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"time"
)

const (
	defaultRPCWindow              = 30 * time.Second
	maxRPCMessage                 = 80 << 20
	windowsPipeSecurityDescriptor = "D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;GRGW;;;AU)"
)

type RPCHandler func(ctx context.Context, method string, params []byte) (any, error)

type RPCServer struct {
	endpoint string
	key      []byte
	verifier *Verifier
	handler  RPCHandler
}

type RPCClient struct {
	endpoint string
	key      []byte
	verifier *Verifier
}

type rpcResult struct {
	Value json.RawMessage `json:"value,omitempty"`
	Error string          `json:"error,omitempty"`
}

func NewRPCServer(endpoint string, key []byte, handler RPCHandler) *RPCServer {
	return &RPCServer{
		endpoint: endpoint,
		key:      append([]byte(nil), key...),
		verifier: NewVerifier(key, defaultRPCWindow),
		handler:  handler,
	}
}

func NewRPCClient(endpoint string, key []byte) *RPCClient {
	return &RPCClient{
		endpoint: endpoint,
		key:      append([]byte(nil), key...),
		verifier: NewVerifier(key, defaultRPCWindow),
	}
}

func (s *RPCServer) Serve(ctx context.Context) error {
	if s.endpoint == "" {
		return errors.New("IPC endpoint is required")
	}
	if s.handler == nil {
		return errors.New("IPC handler is required")
	}
	listener, err := listenEndpoint(s.endpoint)
	if err != nil {
		return err
	}
	defer listener.Close()

	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()

	for {
		connection, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		go s.handleConnection(ctx, connection)
	}
}

func (s *RPCServer) handleConnection(ctx context.Context, connection net.Conn) {
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(defaultRPCWindow))

	var request Message
	if err := decodeLimited(connection, &request); err != nil {
		return
	}
	if err := s.verifier.Verify(request, time.Now()); err != nil {
		_ = s.writeResponse(connection, request, nil, "unauthorized IPC request")
		return
	}

	requestCtx, cancelRequest := context.WithTimeout(ctx, defaultRPCWindow)
	disconnected := make(chan struct{})
	go func() {
		var extra [1]byte
		_, _ = connection.Read(extra[:])
		cancelRequest()
		close(disconnected)
	}()
	value, err := s.handler(requestCtx, request.Method, request.Params)
	cancelRequest()
	if err != nil {
		_ = s.writeResponse(connection, request, nil, err.Error())
		return
	}
	_ = s.writeResponse(connection, request, value, "")
	_ = connection.Close()
	<-disconnected
}

func (s *RPCServer) writeResponse(writer io.Writer, request Message, value any, errorMessage string) error {
	payload := rpcResult{Error: errorMessage}
	if errorMessage == "" {
		encoded, err := json.Marshal(value)
		if err != nil {
			payload.Error = "encode IPC response"
		} else {
			payload.Value = encoded
		}
	}
	params, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	response, err := newMessage(request.ID, "response:"+request.Method, params, s.key)
	if err != nil {
		return err
	}
	return json.NewEncoder(writer).Encode(response)
}

func (c *RPCClient) Call(ctx context.Context, method string, params any, result any) error {
	if c.endpoint == "" || method == "" {
		return errors.New("IPC endpoint and method are required")
	}
	rawParams, err := json.Marshal(params)
	if err != nil {
		return err
	}
	requestID, err := randomToken(16)
	if err != nil {
		return err
	}
	request, err := newMessage(requestID, method, rawParams, c.key)
	if err != nil {
		return err
	}

	connection, err := dialEndpoint(ctx, c.endpoint)
	if err != nil {
		return err
	}
	defer connection.Close()
	stopCancellationWatch := make(chan struct{})
	defer close(stopCancellationWatch)
	go func() {
		select {
		case <-ctx.Done():
			_ = connection.Close()
		case <-stopCancellationWatch:
		}
	}()
	if deadline, ok := ctx.Deadline(); ok {
		_ = connection.SetDeadline(deadline)
	} else {
		_ = connection.SetDeadline(time.Now().Add(defaultRPCWindow))
	}
	if err := json.NewEncoder(connection).Encode(request); err != nil {
		return err
	}

	var response Message
	if err := decodeLimited(connection, &response); err != nil {
		return err
	}
	if err := c.verifier.Verify(response, time.Now()); err != nil {
		return fmt.Errorf("verify IPC response: %w", err)
	}
	if response.ID != request.ID || response.Method != "response:"+method {
		return errors.New("IPC response identity mismatch")
	}
	var payload rpcResult
	if err := json.Unmarshal(response.Params, &payload); err != nil {
		return err
	}
	if payload.Error != "" {
		return errors.New(payload.Error)
	}
	if result == nil || len(payload.Value) == 0 || string(payload.Value) == "null" {
		return nil
	}
	return json.Unmarshal(payload.Value, result)
}

func newMessage(id, method string, params json.RawMessage, key []byte) (Message, error) {
	nonce, err := randomToken(24)
	if err != nil {
		return Message{}, err
	}
	message := Message{
		ID:        id,
		Timestamp: time.Now().Unix(),
		Nonce:     nonce,
		Method:    method,
		Params:    params,
	}
	if err := Sign(&message, key); err != nil {
		return Message{}, err
	}
	return message, nil
}

func randomToken(size int) (string, error) {
	buffer := make([]byte, size)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}

func decodeLimited(reader io.Reader, value any) error {
	decoder := json.NewDecoder(io.LimitReader(reader, maxRPCMessage+1))
	if err := decoder.Decode(value); err != nil {
		return err
	}
	return nil
}
