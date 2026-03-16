package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/q15co/q15/libs/proxy-contract/proxypb"
	"google.golang.org/grpc"
)

// RuntimeInfo describes service-owned runtime metadata exposed over gRPC.
type RuntimeInfo struct {
	ServiceVersion       string
	AdvertiseProxyURL    string
	NoProxy              string
	SetLowercaseProxyEnv bool
	CACertPEM            []byte
	EnvValues            map[string]string
	PolicyRevision       string
}

// GRPCServer implements the proxy admin API.
type GRPCServer struct {
	proxypb.UnimplementedProxyServiceServer

	info RuntimeInfo
}

// NewGRPCServer constructs a proxy admin service implementation.
func NewGRPCServer(info RuntimeInfo) (*GRPCServer, error) {
	info.ServiceVersion = strings.TrimSpace(info.ServiceVersion)
	if info.ServiceVersion == "" {
		info.ServiceVersion = "dev"
	}
	info.AdvertiseProxyURL = strings.TrimSpace(info.AdvertiseProxyURL)
	if info.AdvertiseProxyURL == "" {
		return nil, fmt.Errorf("advertise proxy URL is required")
	}
	info.EnvValues = cloneStringMap(info.EnvValues)
	info.CACertPEM = append([]byte(nil), info.CACertPEM...)
	return &GRPCServer{info: info}, nil
}

// Register attaches the service to one gRPC server.
func (s *GRPCServer) Register(server grpc.ServiceRegistrar) {
	proxypb.RegisterProxyServiceServer(server, s)
}

// GetRuntimeInfo reports runtime metadata and derived proxy values.
func (s *GRPCServer) GetRuntimeInfo(
	context.Context,
	*proxypb.GetRuntimeInfoRequest,
) (*proxypb.GetRuntimeInfoResponse, error) {
	return &proxypb.GetRuntimeInfoResponse{
		ServiceVersion:       s.info.ServiceVersion,
		AdvertiseProxyUrl:    s.info.AdvertiseProxyURL,
		NoProxy:              s.info.NoProxy,
		SetLowercaseProxyEnv: s.info.SetLowercaseProxyEnv,
		CaCertPem:            append([]byte(nil), s.info.CACertPEM...),
		EnvValues:            cloneStringMap(s.info.EnvValues),
		PolicyRevision:       s.info.PolicyRevision,
		Capabilities: []*proxypb.RuntimeCapability{
			{Name: "mitm", Enabled: true},
			{Name: "env_placeholders", Enabled: true},
			{Name: "persistent_ca", Enabled: true},
		},
	}, nil
}

func cloneStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]string, len(values))
	for k, v := range values {
		out[k] = v
	}
	return out
}
