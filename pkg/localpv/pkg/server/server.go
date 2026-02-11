package server

import (
	"fmt"
	"net"
	"os"
	"strings"

	csi "github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc"
	"k8s.io/klog/v2"
)

type Options struct {
	Endpoint string
}

type Server struct {
	endpoint string
	grpc     *grpc.Server
	lis      net.Listener
}

func New(opts Options) (*Server, error) {
	if opts.Endpoint == "" {
		return nil, fmt.Errorf("endpoint is required")
	}
	return &Server{endpoint: opts.Endpoint}, nil
}

func (s *Server) Start(d interface {
	csi.IdentityServer
	csi.ControllerServer
	csi.NodeServer
}) error {
	network, addr, err := parseEndpoint(s.endpoint)
	if err != nil {
		return err
	}
	if network == "unix" {
		_ = os.Remove(addr)
	}

	l, err := net.Listen(network, addr)
	if err != nil {
		return fmt.Errorf("listen on %s %q: %w", network, addr, err)
	}
	s.lis = l
	s.grpc = grpc.NewServer()
	csi.RegisterIdentityServer(s.grpc, d)
	csi.RegisterControllerServer(s.grpc, d)
	csi.RegisterNodeServer(s.grpc, d)

	go func() {
		klog.Infof("localpv csi: serving at %s", s.endpoint)
		if err := s.grpc.Serve(l); err != nil {
			klog.Errorf("grpc serve: %v", err)
		}
	}()
	return nil
}

func (s *Server) Stop() {
	if s.grpc != nil {
		s.grpc.GracefulStop()
	}
	if s.lis != nil {
		_ = s.lis.Close()
	}
}

func parseEndpoint(ep string) (string, string, error) {
	switch {
	case strings.HasPrefix(ep, "unix://"):
		return "unix", strings.TrimPrefix(ep, "unix://"), nil
	case strings.HasPrefix(ep, "tcp://"):
		return "tcp", strings.TrimPrefix(ep, "tcp://"), nil
	default:
		return "", "", fmt.Errorf("unsupported endpoint scheme (use unix:// or tcp://): %q", ep)
	}
}
