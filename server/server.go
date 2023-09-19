package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"sync"

	grpc_net_conn "github.com/binarycraft007/go-grpc-net-conn"
	"github.com/binarycraft007/toh/server/acl"
	"github.com/binarycraft007/toh/server/admin"
	"github.com/binarycraft007/toh/spec"
	pb "github.com/binarycraft007/toh/spec/api/v1"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	grpcMetadata "google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/protobuf/proto"
)

type TohServer struct {
	adminAPI         *admin.AdminAPI
	options          Options
	acl              *acl.ACL
	trafficEventChan chan *TrafficEvent
	bufPool          *sync.Pool
}

type prodigyService struct {
	pb.UnimplementedProdigyServiceServer
}

type Options struct {
	ACL      string
	Buf      uint64
	AdminKey string
}

var tohServer *TohServer = nil

func NewTohServer(options Options) (*TohServer, error) {
	acl, err := acl.NewACL(options.ACL, options.AdminKey)
	if err != nil {
		return nil, err
	}
	return &TohServer{
		options:          options,
		acl:              acl,
		adminAPI:         &admin.AdminAPI{ACL: acl},
		trafficEventChan: make(chan *TrafficEvent, 2048),
		bufPool: &sync.Pool{New: func() any {
			buf := make([]byte, int(math.Max(float64(options.Buf), 1472)))
			return &buf
		}},
	}, nil
}

func (s *TohServer) Run() {
	tohServer = s

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	logrus.Infof("prodigyserver: starting on port %s", port)
	listener, err := net.Listen("tcp", ":"+port)
	if err != nil {
		logrus.Fatalf("net.Listen: %v", err)
	}

	svc := new(prodigyService)
	server := grpc.NewServer()
	pb.RegisterProdigyServiceServer(server, svc)
	if err = server.Serve(listener); err != nil {
		logrus.Fatalf("%v", err)
	}
}

func (prodigyService) StreamMessages(stream pb.ProdigyService_StreamMessagesServer) error {
	metadata, ok := grpcMetadata.FromIncomingContext(stream.Context())
	if !ok {
		return errors.New("No grpc metadata found")
	}
	key := metadata.Get(spec.HeaderHandshakeKey)[0]
	network := metadata.Get(spec.HeaderHandshakeNet)[0]
	addr := metadata.Get(spec.HeaderHandshakeAddr)[0]
	grpcPeer, ok := peer.FromContext(stream.Context())
	if !ok {
		return errors.New("failed to get client IP address")
	}
	clientTCPAddr, ok := grpcPeer.Addr.(*net.TCPAddr)
	if !ok {
		return errors.New("failed to get client IP address")
	}
	clientIP := clientTCPAddr.IP.String()
	if err := tohServer.acl.Check(key, network, addr); err != nil {
		logrus.Infof("%s(%s) -> %s://%s: %s", clientIP, key, network, addr, err.Error())
		return err
	}

	dialer := net.Dialer{}
	netConn, err := dialer.DialContext(context.Background(), network, addr)
	if err != nil {
		logrus.Infof("%s(%s) -> %s://%s: %s", clientIP, key, network, addr, err)
		return err
	}
	defer netConn.Close()

	var FieldFunc = func(msg proto.Message) *[]byte {
		return &msg.(*pb.Message).Data
	}

	conn := &grpc_net_conn.Conn{
		Stream:   stream,
		Request:  &pb.Message{},
		Response: &pb.Message{},
		Encode:   grpc_net_conn.SimpleEncoder(FieldFunc),
		Decode:   grpc_net_conn.SimpleDecoder(FieldFunc),
	}
	defer conn.Close()

	if err = stream.Send(&pb.Message{Data: []byte(netConn.RemoteAddr().String())}); err != nil {
		return err
	}

	errc := make(chan error, 2)
	go func() {
		_, err := io.Copy(netConn, conn)
		if err != nil {
			err = fmt.Errorf("Error copying to remote: %v", err)
		}
		errc <- err
	}()
	go func() {
		_, err := io.Copy(conn, netConn)
		if err != nil {
			err = fmt.Errorf("Error copying to client: %v", err)
		}
		errc <- err
	}()

	return <-errc
}
