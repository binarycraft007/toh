package socks5

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/netip"

	"github.com/binarycraft007/toh/spec"
	"github.com/sirupsen/logrus"
)

type Options struct {
	Listen         string
	AdvertiseIP    string
	AdvertisePort  uint16
	TCPDialContext func(ctx context.Context, addr string) (
		conn net.Conn, err error)
	UDPDialContext func(ctx context.Context, addr string) (
		conn net.Conn, err error)
	TrafficEventConsumer func(e *spec.TrafficEvent)
	HTTPHandlers         map[string]Handler
}

type Socks5Server struct {
	opts            Options
	pipeEngine      *spec.PipeEngine
	httpProxyServer *HTTPProxyServer
}

func NewSocks5Server(opts Options) (s *Socks5Server, err error) {
	ipPort, err := net.ResolveTCPAddr("tcp", opts.Listen)
	if err != nil {
		return
	}
	if opts.AdvertiseIP == "" {
		if ipPort.IP == nil || ipPort.IP.Equal(net.IPv4zero) {
			opts.AdvertiseIP = "127.0.0.1"
		} else {
			opts.AdvertiseIP = ipPort.IP.String()
		}
	}
	if opts.AdvertisePort == 0 {
		opts.AdvertisePort = uint16(ipPort.Port)
	}
	pipeEngine := spec.NewPipeEngine()
	pipeEngine.AddEventConsumer(opts.TrafficEventConsumer)
	s = &Socks5Server{
		opts:            opts,
		pipeEngine:      pipeEngine,
		httpProxyServer: &HTTPProxyServer{opts: opts, pipeEngine: pipeEngine},
	}
	return
}

func (s *Socks5Server) Run() error {
	l, err := net.Listen("tcp", s.opts.Listen)
	if err != nil {
		return err
	}
	defer l.Close()
	udpL, err := net.ListenPacket("udp", s.opts.Listen)
	if err != nil {
		return err
	}
	defer udpL.Close()

	logrus.Infof("listen on %s for socks5+http now", s.opts.Listen)

	go s.startUDPListenLoop(udpL)

	for {
		conn, err := l.Accept()
		if err != nil {
			return err
		}
		go func() error {
			ctx := context.WithValue(context.Background(),
				spec.AppAddr, conn.RemoteAddr().String())
			netConn := s.handshake(ctx, conn)
			defer func() {
				conn.Close()
				logrus.Infof("close connection: %v", conn)
			}()
			if netConn != nil {
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
			} else {
				for {
					buf := make([]byte, 32*1024)
					if _, err = conn.Read(buf); err != nil {
						if err == io.EOF || err == net.ErrClosed {
							break
						}
						return err
					}
				}
			}
			return nil
		}()
	}
}

func (s *Socks5Server) handshake(ctx context.Context, conn net.Conn) (
	netConn net.Conn) {
	log := logrus.WithField(spec.AppAddr.String(), ctx.Value(spec.AppAddr))
	buf := make([]byte, 1024)
	closeConn := true
	defer func() {
		if closeConn {
			conn.Close()
		}
	}()

	// auth
	n, err := conn.Read(buf[:2])
	if err != nil || n != 2 {
		log.Debug("invalid auth packet format @1")
		return
	}

	if buf[0] != 5 {
		if buf[0] >= 65 {
			closeConn = false
			b := make([]byte, 2)
			copy(b, buf[:2])
			go s.httpProxyServer.handle(b, conn)
			return
		}
		log.Debug("unsupport socks version, closed")
		return
	}

	nMethods := buf[1]

	n, err = conn.Read(buf[:nMethods])
	if err != nil || n != int(nMethods) {
		log.Debug("invalid auth packet format @2")
		return
	}

	n, err = conn.Write([]byte{0x05, 0x00})
	if err != nil || n != 2 {
		log.Debug("invalid auth packet format @3")
		return
	}

	// handle command
	n, err = conn.Read(buf[:4])
	if err != nil || n != 4 || buf[0] != 5 {
		log.Debug("handle command error @1")
		return
	}

	cmd, atyp := buf[1], buf[3]

	var addr string
	switch atyp {
	case 1:
		n, err = conn.Read(buf[:4])
		if err != nil || n != 4 {
			log.Error("socks5: can not get IPv4 addr")
			return
		}
		addr = net.IP(buf[:4]).To4().String()
	case 3:
		n, err = conn.Read(buf[:1])
		if err != nil || n != 1 {
			log.Debug("handle command error @3")
			return
		}
		addrLen := buf[0]
		n, err = conn.Read(buf[:addrLen])
		if err != nil || n != int(addrLen) {
			log.Debug("handle command error @4")
			return
		}
		addr = string(buf[:addrLen])
	case 4:
		n, err = conn.Read(buf[:16])
		if err != nil || n != 16 {
			log.Error("socks5: can not get IPv6 addr")
			return
		}
		addr = net.IP(buf[:16]).String()
	default:
		log.Error("invalid ATYP ", atyp)
		return
	}

	n, err = conn.Read(buf[:2])
	if err != nil || n != 2 {
		log.Debug("handle command error @read-port")
		return
	}
	port := spec.BytesToUint16(buf[:2])

	remoteAddr := net.JoinHostPort(addr, fmt.Sprintf("%d", port))
	switch cmd {
	// 1. CONNECT
	case 1:
		netConn, err = s.opts.TCPDialContext(ctx, remoteAddr)
		if err != nil {
			log.Errorf("socks5 establishing tcp://%s error: %s",
				remoteAddr, err)
			// Host Unreachable https://datatracker.ietf.org/doc/html/rfc1928#section-6
			conn.Write([]byte{0x05, 0x04, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00})
			return
		}

		if netConn.LocalAddr() != nil {
			addrPort := netip.MustParseAddrPort(netConn.LocalAddr().String())
			if addrPort.Addr().Is6() {
				ip := addrPort.Addr().As16()
				conn.Write([]byte{5, 0, 0, 4})
				conn.Write(ip[:])
			} else {
				ip := addrPort.Addr().As4()
				conn.Write([]byte{5, 0, 0, 1})
				conn.Write(ip[:])
			}
			conn.Write(spec.Uint16ToBytes(addrPort.Port()))
		} else {
			conn.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0})
		}
		closeConn = false
		return
	// 2. UDP ASSOCIATE
	case 3:
		advertiseIP := net.ParseIP(s.opts.AdvertiseIP)
		if advertiseIP.IsLoopback() {
			ipPort, _ := net.ResolveTCPAddr("tcp", conn.LocalAddr().String())
			advertiseIP = ipPort.IP
		}
		if advertiseIP.To4() == nil {
			conn.Write([]byte{5, 0, 0, 4})
			conn.Write(advertiseIP.To16())
		} else {
			conn.Write([]byte{5, 0, 0, 1})
			conn.Write(advertiseIP.To4())
		}
		conn.Write(spec.Uint16ToBytes(s.opts.AdvertisePort))
		closeConn = false
		return
	// 3. do not support BIND now
	default:
		log.Error("do not support BIND now")
		// Command Not Supported https://datatracker.ietf.org/doc/html/rfc1928#section-6
		conn.Write([]byte{0x05, 0x07, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00})
	}
	return
}
