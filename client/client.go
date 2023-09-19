package client

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"time"

	grpc_net_conn "github.com/binarycraft007/go-grpc-net-conn"
	"github.com/binarycraft007/toh/spec"
	pb "github.com/binarycraft007/toh/spec/api/v1"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	grpcMetadata "google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"
)

type TohClient struct {
	options         Options
	connIdleTimeout time.Duration
	serverPort      string
	RemoteConn      *grpc.ClientConn
}

type Options struct {
	ServerName string
	RemoteAddr string
	RemotePort int
	Password   string
	Keepalive  time.Duration
	Headers    http.Header
}

func NewTohClient(options Options) (*TohClient, error) {
	c := &TohClient{
		options:         options,
		connIdleTimeout: 75 * time.Second,
	}
	var err error
	port := strconv.Itoa(options.RemotePort)
	c.RemoteConn, err = c.DialGrpc(options.ServerName, net.JoinHostPort(options.RemoteAddr, port))
	if err != nil {
		return nil, err
	}
	return c, nil
}

func (c *TohClient) DialTCP(ctx context.Context, addr string) (net.Conn, error) {
	return c.DialContext(ctx, "tcp", addr)
}

func (c *TohClient) DialUDP(ctx context.Context, addr string) (net.Conn, error) {
	return c.DialContext(ctx, "udp", addr)
}

func (c *TohClient) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	switch network {
	case "tcp", "tcp4", "tcp6":
		conn, _, err := c.dial(ctx, network, address)
		if err != nil {
			return nil, err
		}
		return conn, nil
	case "udp", "udp4", "udp6":
		conn, _, err := c.dial(ctx, network, address)
		if err != nil {
			return nil, err
		}
		return conn, nil
	default:
		return nil, errors.New("unsupport network " + network)
	}
}

func (c *TohClient) DialGrpc(name, address string) (*grpc.ClientConn, error) {
	var transportCredentials credentials.TransportCredentials
	systemCertPool, err := x509.SystemCertPool()
	if err != nil {
		return nil, err
	}
	transportCredentials = credentials.NewTLS(&tls.Config{
		RootCAs:    systemCertPool,
		ServerName: name,
	})

	return grpc.Dial(
		address,
		grpc.WithTransportCredentials(transportCredentials),
		grpc.WithAuthority(name),
	)
}

func (c *TohClient) dial(ctx context.Context, network, addr string) (
	conn net.Conn, remoteAddr net.Addr, err error) {
	client := pb.NewProdigyServiceClient(c.RemoteConn)
	grpcCtx := grpcMetadata.AppendToOutgoingContext(
		context.Background(),
		spec.HeaderHandshakeKey, c.options.Password,
		spec.HeaderHandshakeNet, network,
		spec.HeaderHandshakeAddr, addr,
	)
	stream, err := client.StreamMessages(grpcCtx)
	if err != nil {
		return
	}

	var FieldFunc = func(msg proto.Message) *[]byte {
		return &msg.(*pb.Message).Data
	}

	conn = &grpc_net_conn.Conn{
		Stream:   stream,
		Request:  &pb.Message{},
		Response: &pb.Message{},
		Encode:   grpc_net_conn.SimpleEncoder(FieldFunc),
		Decode:   grpc_net_conn.SimpleDecoder(FieldFunc),
	}

	var message *pb.Message
	if message, err = stream.Recv(); err != nil {
		err = fmt.Errorf("failed to extablish remote conn: %v", err)
		return
	}
	estAddr := string(message.Data)
	if len(estAddr) == 0 {
		estAddr = "0.0.0.0:0"
	}
	logrus.Infof("estAddr: %s", estAddr)
	host, _port, err := net.SplitHostPort(estAddr)
	port, _ := strconv.Atoi(_port)
	switch network {
	case "tcp", "tcp4", "tcp6":
		remoteAddr = &net.TCPAddr{
			IP:   net.ParseIP(host),
			Port: port,
		}
	case "udp", "udp4", "udp6":
		remoteAddr = &net.UDPAddr{
			IP:   net.ParseIP(host),
			Port: port,
		}
	default:
		err = spec.ErrUnsupportNetwork
	}
	return
}
