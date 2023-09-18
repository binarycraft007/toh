package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"

	grpc_net_conn "github.com/binarycraft007/go-grpc-net-conn"
	"github.com/binarycraft007/toh/server/acl"
	"github.com/binarycraft007/toh/server/admin"
	"github.com/binarycraft007/toh/spec"
	pb "github.com/binarycraft007/toh/spec/api/v1"
	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
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
	go s.runTrafficEventConsumeLoop()
	go s.runShutdownListener()
	// s.adminAPI.Register()

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
	//go func() {
	//	lbc, rbc := tohServer.pipe(conn, netConn)
	//	tohServer.trafficEventChan <- &TrafficEvent{
	//		In:       lbc,
	//		Out:      rbc,
	//		Key:      key,
	//		Network:  network,
	//		ClientIP: clientIP,
	//		//RemoteAddr: addr,
	//	}
	//}()
	//return nil
}

func (s TohServer) HandleUpgradeWebSocket(w http.ResponseWriter, r *http.Request) {
	key := r.Header.Get(spec.HeaderHandshakeKey)
	network := r.Header.Get(spec.HeaderHandshakeNet)
	addr := r.Header.Get(spec.HeaderHandshakeAddr)
	clientIP := spec.RealIP(r)

	if err := s.acl.Check(key, network, addr); err != nil {
		w.WriteHeader(http.StatusUnauthorized)
		logrus.Infof("%s(%s) -> %s://%s: %s", clientIP, key, network, addr, err.Error())
		return
	}

	dialer := net.Dialer{}
	netConn, err := dialer.DialContext(context.Background(), network, addr)
	if err != nil {
		logrus.Debugf("%s(%s) -> %s://%s: %s", clientIP, key, network, addr, err)
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(err.Error()))
		return
	}

	upgradeHeader := http.Header{}
	upgradeHeader.Add(spec.HeaderEstablishAddr, netConn.RemoteAddr().String())
	conn, _, _, err := ws.HTTPUpgrader{Header: upgradeHeader}.Upgrade(r, w)
	if err != nil {
		logrus.Error(err)
		return
	}

	go func() {
		lbc, rbc := s.pipe(spec.NewConn(&wsConn{conn: conn}, conn.RemoteAddr()), netConn)
		s.trafficEventChan <- &TrafficEvent{
			In:       lbc,
			Out:      rbc,
			Key:      key,
			Network:  network,
			ClientIP: clientIP,
			//RemoteAddr: addr,
		}
	}()
}

func (s *TohServer) pipe(grpcConn net.Conn, netConn net.Conn) (lbc, rbc int64) {
	if grpcConn == nil || netConn == nil {
		return
	}
	wg := sync.WaitGroup{}
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer netConn.Close()
		buf := s.bufPool.Get().(*[]byte)
		defer s.bufPool.Put(buf)
		lbc, _ = io.CopyBuffer(netConn, grpcConn, *buf)
		logrus.Debugf("ws conn closed, close remote conn(%s) now", netConn.RemoteAddr().String())
	}()
	defer wg.Wait()
	defer grpcConn.Close()
	buf := s.bufPool.Get().(*[]byte)
	defer s.bufPool.Put(buf)
	rbc, _ = io.CopyBuffer(grpcConn, netConn, *buf)
	logrus.Debugf("remote conn(%s) closed, close ws conn now", netConn.RemoteAddr().String())
	return
}

func (s *TohServer) runShutdownListener() {
	sigs := make(chan os.Signal, 1)
	defer close(sigs)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	<-sigs
	s.acl.Shutdown()
	os.Exit(0)
}

type wsConn struct {
	conn net.Conn
}

func (c *wsConn) Read(ctx context.Context) (b []byte, err error) {
	if dl, ok := ctx.Deadline(); ok {
		c.conn.SetReadDeadline(dl)
	}
	return wsutil.ReadClientBinary(c.conn)
}
func (c *wsConn) Write(ctx context.Context, p []byte) error {
	if dl, ok := ctx.Deadline(); ok {
		c.conn.SetWriteDeadline(dl)
	}
	return wsutil.WriteServerBinary(c.conn, p)
}

func (c *wsConn) LocalAddr() net.Addr {
	return c.conn.LocalAddr()
}

func (c *wsConn) Close(code int, reason string) error {
	ws.WriteFrame(c.conn, ws.NewCloseFrame(ws.NewCloseFrameBody(ws.StatusCode(code), reason)))
	return c.conn.Close()
}
