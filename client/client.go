package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
	"github.com/miekg/dns"
	"github.com/rkonfj/toh/server/api"
	"github.com/rkonfj/toh/spec"
	"github.com/sirupsen/logrus"
)

type TohClient struct {
	options         Options
	connIdleTimeout time.Duration
	httpClient      *http.Client
	serverIPs       []net.IP
	serverPort      string
	dnsClient       *dns.Client
}

type Options struct {
	Server, Key string
	Keepalive   time.Duration
	Headers     http.Header
}

func NewTohClient(options Options) (*TohClient, error) {
	if _, err := url.ParseRequestURI(options.Server); err != nil {
		return nil, fmt.Errorf("invalid server addr, %s", err.Error())
	}
	c := &TohClient{
		options:         options,
		dnsClient:       &dns.Client{},
		connIdleTimeout: 75 * time.Second,
	}
	c.httpClient = &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (conn net.Conn, err error) {
				if len(c.serverIPs) == 0 {
					var host string
					host, c.serverPort, err = net.SplitHostPort(addr)
					if err != nil {
						return
					}
					c.serverIPs, err = c.directLookupIP(host, dns.TypeA)
					if err == spec.ErrDNSTypeANotFound {
						c.serverIPs, err = c.directLookupIP(host, dns.TypeAAAA)
					}
					if err != nil {
						err = spec.ErrDNSRecordNotFound
						return
					}
				}
				return (&net.Dialer{}).DialContext(ctx, network,
					net.JoinHostPort(c.serverIPs[rand.Intn(len(c.serverIPs))].String(), c.serverPort))
			},
		},
	}
	return c, nil
}

func (c *TohClient) DNSExchange(dnServer string, query *dns.Msg) (resp *dns.Msg, err error) {
	return c.dnsExchange(dnServer, query, false)
}

// LookupIP lookup ipv4 and ipv6
func (c *TohClient) LookupIP(host string) (ips []net.IP, err error) {
	var wg sync.WaitGroup
	wg.Add(1)
	var e4, e6 error
	var ip6 []net.IP
	go func() {
		defer wg.Done()
		_ips, e6 := c.LookupIP6(host)
		if e6 == nil {
			ip6 = append(ip6, _ips...)
		}
	}()
	_ips, e4 := c.lookupIP(host, dns.TypeA, false)
	if e4 == nil {
		ips = append(ips, _ips...)
	}
	wg.Wait()
	ips = append(ips, ip6...)
	if e4 != nil && e6 != nil {
		err = fmt.Errorf("%s %s", e4.Error(), e6.Error())
	}
	return
}

// LookupIP4 lookup only ipv4
func (c *TohClient) LookupIP4(host string) (ips []net.IP, err error) {
	return c.lookupIP(host, dns.TypeA, false)
}

// LookupIP4 lookup only ipv6
func (c *TohClient) LookupIP6(host string) (ips []net.IP, err error) {
	return c.lookupIP(host, dns.TypeAAAA, false)
}

func (c *TohClient) DialTCP(ctx context.Context, addr string) (net.Conn, error) {
	return c.DialContext(ctx, "tcp", addr)
}

func (c *TohClient) DialUDP(ctx context.Context, addr string) (net.Conn, error) {
	return c.DialContext(ctx, "udp", addr)
}

func (c *TohClient) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	switch network {
	case "tcp", "tcp4", "tcp6", "udp", "udp4", "udp6":
		conn, addr, err := c.dial(ctx, network, address)
		if err != nil {
			return nil, err
		}
		return spec.NewWSStreamConn(conn, addr), nil
	default:
		return nil, errors.New("unsupport network")
	}
}
func (c *TohClient) Stats() (s *api.Stats, err error) {
	u, _ := url.ParseRequestURI(c.options.Server)
	scheme := u.Scheme
	if u.Scheme == "ws" {
		scheme = "http"
	} else if u.Scheme == "wss" {
		scheme = "https"
	}
	apiUrl := fmt.Sprintf("%s://%s/stats", scheme, u.Host)
	req, err := http.NewRequest(http.MethodGet, apiUrl, nil)
	req.Header.Add(spec.HeaderHandshakeKey, c.options.Key)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return
	}
	s = &api.Stats{}
	defer resp.Body.Close()

	err = json.NewDecoder(resp.Body).Decode(s)
	return
}

func (c *TohClient) dnsExchange(dnServer string, query *dns.Msg, direct bool) (resp *dns.Msg, err error) {
	dnsLookupCtx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	if direct {
		resp, _, err = c.dnsClient.ExchangeContext(dnsLookupCtx, query, dnServer)
	} else {
		conn, _err := c.DialUDP(dnsLookupCtx, dnServer)
		if _err != nil {
			err = fmt.Errorf("dial error: %s", _err.Error())
			return
		}

		defer conn.Close()
		resp, _, err = c.dnsClient.ExchangeWithConn(query, &dns.Conn{Conn: &spec.PacketConnWrapper{Conn: conn}})
	}
	return
}

func (c *TohClient) lookupIP(host string, t uint16, direct bool) (ips []net.IP, err error) {
	ip := net.ParseIP(host)
	if ip != nil {
		ips = append(ips, ip)
		return
	}
	query := &dns.Msg{}
	query.SetQuestion(dns.Fqdn(host), t)
	var resp *dns.Msg
	for _, dnServer := range []string{"8.8.8.8:53", "223.5.5.5:53"} {
		resp, err = c.dnsExchange(dnServer, query, direct)
		if err == nil {
			break
		}
	}
	if err != nil {
		return
	}
	for _, a := range resp.Answer {
		if a.Header().Rrtype == dns.TypeA {
			ips = append(ips, a.(*dns.A).A)
		}
		if a.Header().Rrtype == dns.TypeAAAA {
			ips = append(ips, a.(*dns.AAAA).AAAA)
		}
	}
	if len(ips) == 0 {
		if t == dns.TypeA {
			err = spec.ErrDNSTypeANotFound
		} else if t == dns.TypeAAAA {
			err = spec.ErrDNSTypeAAAANotFound
		} else {
			err = fmt.Errorf("resolve %s : no type %s was found", host, dns.Type(t))
		}
	}
	return
}

func (c *TohClient) directLookupIP(host string, t uint16) (ips []net.IP, err error) {
	return c.lookupIP(host, t, true)
}

func (c *TohClient) dial(ctx context.Context, network, addr string) (
	wsConn *wsConn, remoteAddr net.Addr, err error) {
	handshake := http.Header{}
	handshake.Add(spec.HeaderHandshakeKey, c.options.Key)
	handshake.Add(spec.HeaderHandshakeNet, network)
	handshake.Add(spec.HeaderHandshakeAddr, addr)
	for k, v := range c.options.Headers {
		for _, item := range v {
			handshake.Add(k, item)
		}
	}

	t1 := time.Now()

	wsConn, respHeader, err := dialWS(ctx, c.options.Server, handshake)
	if err != nil {
		return
	}
	logrus.Debugf("%s://%s established successfully, toh latency %s", network, addr, time.Since(t1))

	go c.newPingLoop(wsConn)
	estAddr := respHeader.Get(spec.HeaderEstablishAddr)
	if len(estAddr) == 0 {
		estAddr = "0.0.0.0:0"
	}
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

func (c *TohClient) newPingLoop(wsConn *wsConn) {
	if c.options.Keepalive == 0 {
		return
	}
	for {
		time.Sleep(c.options.Keepalive)
		if time.Since(wsConn.lastActiveTime) > c.connIdleTimeout {
			logrus.Debug("ping: exited. connection reached the max idle time ", c.connIdleTimeout)
			break
		}
		err := wsConn.Ping()
		if err != nil {
			logrus.Debug("ping: ", err)
			break
		}
	}
}

type wsConn struct {
	conn           net.Conn
	lastActiveTime time.Time
}

func dialWS(ctx context.Context, urlstr string, header http.Header) (
	wsc *wsConn, respHeader http.Header, err error) {
	respHeader = http.Header{}
	var statusCode int
	dialer := ws.Dialer{
		Header: ws.HandshakeHeaderHTTP(header),
		OnHeader: func(key, value []byte) (err error) {
			respHeader.Add(string(key), string(value))
			return
		},
		OnStatusError: func(status int, reason []byte, resp io.Reader) {
			statusCode = status
		},
	}
	u, err := url.Parse(urlstr)
	if err != nil {
		return
	}
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	}
	conn, _, _, err := dialer.Dial(context.Background(), u.String())
	if err != nil {
		return
	}
	if statusCode == http.StatusUnauthorized {
		err = spec.ErrAuth
		return
	}
	if statusCode > 0 {
		err = errors.New(http.StatusText(statusCode))
		return
	}
	wsc = &wsConn{
		conn:           conn,
		lastActiveTime: time.Now(),
	}
	return
}

func (c *wsConn) Read(ctx context.Context) (b []byte, err error) {
	c.lastActiveTime = time.Now()
	if dl, ok := ctx.Deadline(); ok {
		c.conn.SetReadDeadline(dl)
	}
	return wsutil.ReadServerBinary(c.conn)
}
func (c *wsConn) Write(ctx context.Context, p []byte) error {
	c.lastActiveTime = time.Now()
	if dl, ok := ctx.Deadline(); ok {
		c.conn.SetWriteDeadline(dl)
	}
	return wsutil.WriteClientBinary(c.conn, p)
}

func (c *wsConn) LocalAddr() net.Addr {
	return c.conn.LocalAddr()
}

func (c *wsConn) Close(code int, reason string) error {
	ws.WriteFrame(c.conn, ws.NewCloseFrame(ws.NewCloseFrameBody(ws.StatusCode(code), reason)))
	return c.conn.Close()
}

func (c *wsConn) Ping() error {
	return wsutil.WriteClientMessage(c.conn, ws.OpPing, ws.NewPingFrame([]byte{}).Payload)
}
