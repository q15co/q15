package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/q15co/q15/libs/exec-contract/execpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// RuntimeInfo describes service-owned runtime metadata.
type RuntimeInfo struct {
	ServiceVersion      string
	ExecutorType        string
	WorkspaceDir        string
	MemoryDir           string
	SkillsDir           string
	ProxyEnabled        bool
	ProxyPolicyRevision string
}

// GRPCServer implements the execution service API.
type GRPCServer struct {
	execpb.UnimplementedExecutionServiceServer

	manager *Manager
	info    RuntimeInfo
}

// NewGRPCServer constructs a gRPC service implementation.
func NewGRPCServer(manager *Manager, info RuntimeInfo) (*GRPCServer, error) {
	if manager == nil {
		return nil, fmt.Errorf("manager is required")
	}
	info.ServiceVersion = strings.TrimSpace(info.ServiceVersion)
	if info.ServiceVersion == "" {
		info.ServiceVersion = "dev"
	}
	return &GRPCServer{
		manager: manager,
		info:    info,
	}, nil
}

// Register attaches the service to one gRPC server.
func (s *GRPCServer) Register(server grpc.ServiceRegistrar) {
	execpb.RegisterExecutionServiceServer(server, s)
}

// GetRuntimeInfo reports runtime metadata and capabilities.
func (s *GRPCServer) GetRuntimeInfo(
	context.Context,
	*execpb.GetRuntimeInfoRequest,
) (*execpb.GetRuntimeInfoResponse, error) {
	return &execpb.GetRuntimeInfoResponse{
		ServiceVersion:      s.info.ServiceVersion,
		ExecutorType:        s.info.ExecutorType,
		WorkspaceDir:        s.info.WorkspaceDir,
		MemoryDir:           s.info.MemoryDir,
		SkillsDir:           s.info.SkillsDir,
		ProxyEnabled:        s.info.ProxyEnabled,
		ProxyPolicyRevision: s.info.ProxyPolicyRevision,
		Capabilities: []*execpb.RuntimeCapability{
			{Name: "sessions", Enabled: true},
			{Name: "watch", Enabled: true},
			{Name: "stdin", Enabled: true},
			{Name: "terminate", Enabled: true},
		},
	}, nil
}

// StartSession creates and starts a tracked command session.
func (s *GRPCServer) StartSession(
	ctx context.Context,
	req *execpb.StartSessionRequest,
) (*execpb.StartSessionResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request is required")
	}
	session, err := s.manager.StartSession(
		ctx,
		req.GetCommand(),
		req.GetPackages(),
		req.GetWorkingDir(),
		req.GetKeepStdinOpen(),
	)
	if err != nil {
		return nil, mapError(err)
	}
	return &execpb.StartSessionResponse{Session: session}, nil
}

// GetSession returns the current session state.
func (s *GRPCServer) GetSession(
	_ context.Context,
	req *execpb.GetSessionRequest,
) (*execpb.GetSessionResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request is required")
	}
	session, err := s.manager.GetSession(req.GetSessionId())
	if err != nil {
		return nil, mapError(err)
	}
	return &execpb.GetSessionResponse{Session: session}, nil
}

// WatchSession streams session events from the requested cursor.
func (s *GRPCServer) WatchSession(
	req *execpb.WatchSessionRequest,
	stream grpc.ServerStreamingServer[execpb.WatchSessionResponse],
) error {
	if req == nil {
		return status.Error(codes.InvalidArgument, "request is required")
	}
	return mapError(s.manager.WatchSession(
		stream.Context(),
		req.GetSessionId(),
		req.GetAfterEventIndex(),
		func(event *execpb.SessionEvent) error {
			return stream.Send(&execpb.WatchSessionResponse{Event: event})
		},
	))
}

// WriteSessionStdin writes bytes into one running session.
func (s *GRPCServer) WriteSessionStdin(
	_ context.Context,
	req *execpb.WriteSessionStdinRequest,
) (*execpb.WriteSessionStdinResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request is required")
	}
	session, bytesWritten, err := s.manager.WriteSessionStdin(
		req.GetSessionId(),
		req.GetData(),
		req.GetCloseStdin(),
	)
	if err != nil {
		return nil, mapError(err)
	}
	return &execpb.WriteSessionStdinResponse{
		Session:      session,
		BytesWritten: bytesWritten,
	}, nil
}

// TerminateSession stops one running session.
func (s *GRPCServer) TerminateSession(
	_ context.Context,
	req *execpb.TerminateSessionRequest,
) (*execpb.TerminateSessionResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request is required")
	}
	session, err := s.manager.TerminateSession(req.GetSessionId(), req.GetForce())
	if err != nil {
		return nil, mapError(err)
	}
	return &execpb.TerminateSessionResponse{Session: session}, nil
}

func mapError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, errSessionNotFound):
		return status.Error(codes.NotFound, err.Error())
	case isInvalidArgumentError(err):
		return status.Error(codes.InvalidArgument, err.Error())
	default:
		return status.Error(codes.Internal, err.Error())
	}
}

func isInvalidArgumentError(err error) bool {
	msg := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(msg, "required") || strings.Contains(msg, "must be >=")
}
