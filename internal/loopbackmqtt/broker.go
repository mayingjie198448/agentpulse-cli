// Package loopbackmqtt is a one-shot MQTT 3.1.1 broker for a single
// 127.0.0.1 listener. It is not a system service and it does not read
// credentials. Username or password on CONNECT is refused.
package loopbackmqtt

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
)

// Broker serves MQTT on one loopback TCP listener.
type Broker struct {
	ln       net.Listener
	mu       sync.Mutex
	subs     map[string]map[*session]byte
	retained map[string][]byte
	done     chan struct{}
}

// Listen binds 127.0.0.1 only. An empty port uses an ephemeral port.
func Listen(port int) (*Broker, error) {
	if port < 0 || port > 65535 {
		return nil, fmt.Errorf("invalid port %d", port)
	}
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return nil, err
	}
	tcp, ok := ln.Addr().(*net.TCPAddr)
	if !ok || tcp.IP == nil || !tcp.IP.IsLoopback() {
		_ = ln.Close()
		return nil, fmt.Errorf("broker listener is not loopback: %s", ln.Addr())
	}
	b := &Broker{
		ln:       ln,
		subs:     make(map[string]map[*session]byte),
		retained: make(map[string][]byte),
		done:     make(chan struct{}),
	}
	go b.accept()
	return b, nil
}

// Addr is host:port, always on 127.0.0.1.
func (b *Broker) Addr() string { return b.ln.Addr().String() }

// URL is the tcp URL for --mqtt-url.
func (b *Broker) URL() string { return "tcp://" + b.Addr() }

// Close stops the listener and current connections.
func (b *Broker) Close() error {
	select {
	case <-b.done:
	default:
		close(b.done)
	}
	return b.ln.Close()
}

func (b *Broker) accept() {
	for {
		conn, err := b.ln.Accept()
		if err != nil {
			return
		}
		host, _, _ := net.SplitHostPort(conn.RemoteAddr().String())
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			_ = conn.Close()
			continue
		}
		go b.serve(conn)
	}
}

type session struct {
	conn net.Conn
	w    *bufio.Writer
	mu   sync.Mutex
}

func (b *Broker) serve(conn net.Conn) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	s := &session{conn: conn, w: bufio.NewWriter(conn)}
	defer b.remove(s)
	for {
		select {
		case <-b.done:
			return
		default:
		}
		hdr, body, err := readPacket(r)
		if err != nil {
			return
		}
		switch hdr >> 4 {
		case 1: // CONNECT
			if err := acceptConnect(body); err != nil {
				_ = s.writePacket(0x20, []byte{0x00, 0x04})
				return
			}
			if err := s.writePacket(0x20, []byte{0x00, 0x00}); err != nil {
				return
			}
		case 3: // PUBLISH
			if err := b.onPublish(s, hdr, body); err != nil {
				return
			}
		case 8: // SUBSCRIBE
			if err := b.onSubscribe(s, body); err != nil {
				return
			}
		case 12: // PINGREQ
			if err := s.writePacket(0xD0, nil); err != nil {
				return
			}
		case 14: // DISCONNECT
			return
		default:
			return
		}
	}
}

func (b *Broker) onPublish(s *session, hdr byte, body []byte) error {
	topic, rest, err := mqttString(body)
	if err != nil {
		return err
	}
	qos := (hdr >> 1) & 0x03
	retain := hdr&0x01 != 0
	if qos > 0 {
		if len(rest) < 2 {
			return io.ErrUnexpectedEOF
		}
		id := binary.BigEndian.Uint16(rest[:2])
		rest = rest[2:]
		if qos == 1 {
			ack := []byte{byte(id >> 8), byte(id)}
			if err := s.writePacket(0x40, ack); err != nil {
				return err
			}
		}
	}
	payload := append([]byte(nil), rest...)
	b.mu.Lock()
	if retain {
		if len(payload) == 0 {
			delete(b.retained, topic)
		} else {
			b.retained[topic] = append([]byte(nil), payload...)
		}
	}
	targets := make([]*session, 0)
	if set := b.subs[topic]; set != nil {
		for sub := range set {
			targets = append(targets, sub)
		}
	}
	b.mu.Unlock()
	for _, sub := range targets {
		_ = writePublish(sub, topic, payload, false)
	}
	return nil
}

func (b *Broker) onSubscribe(s *session, body []byte) error {
	if len(body) < 2 {
		return io.ErrUnexpectedEOF
	}
	id := binary.BigEndian.Uint16(body[:2])
	rest := body[2:]
	var codes []byte
	var retained []struct {
		topic   string
		payload []byte
	}
	for len(rest) > 0 {
		topic, next, err := mqttString(rest)
		if err != nil || len(next) < 1 {
			return io.ErrUnexpectedEOF
		}
		qos := next[0]
		rest = next[1:]
		if qos > 1 {
			codes = append(codes, 0x80)
			continue
		}
		b.mu.Lock()
		set := b.subs[topic]
		if set == nil {
			set = make(map[*session]byte)
			b.subs[topic] = set
		}
		set[s] = qos
		payload, has := b.retained[topic]
		if has {
			retained = append(retained, struct {
				topic   string
				payload []byte
			}{topic, append([]byte(nil), payload...)})
		}
		b.mu.Unlock()
		codes = append(codes, qos)
	}
	ack := []byte{byte(id >> 8), byte(id)}
	ack = append(ack, codes...)
	if err := s.writePacket(0x90, ack); err != nil {
		return err
	}
	for _, item := range retained {
		if err := writePublish(s, item.topic, item.payload, true); err != nil {
			return err
		}
	}
	return nil
}

func (b *Broker) remove(s *session) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for topic, set := range b.subs {
		delete(set, s)
		if len(set) == 0 {
			delete(b.subs, topic)
		}
	}
}

func acceptConnect(body []byte) error {
	name, rest, err := mqttString(body)
	if err != nil {
		return err
	}
	if name != "MQTT" || len(rest) < 4 || rest[0] != 4 {
		return errors.New("unsupported MQTT protocol")
	}
	flags := rest[1]
	if flags&0xC0 != 0 || flags&0x80 != 0 {
		return errors.New("credentials are refused")
	}
	return nil
}

func writePublish(s *session, topic string, payload []byte, retain bool) error {
	var body []byte
	body = appendMQTTString(body, topic)
	body = append(body, payload...)
	hdr := byte(0x30)
	if retain {
		hdr |= 0x01
	}
	return s.writePacket(hdr, body)
}

func (s *session) writePacket(hdr byte, body []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.w.WriteByte(hdr); err != nil {
		return err
	}
	if err := writeRemaining(s.w, len(body)); err != nil {
		return err
	}
	if _, err := s.w.Write(body); err != nil {
		return err
	}
	return s.w.Flush()
}

func readPacket(r *bufio.Reader) (byte, []byte, error) {
	hdr, err := r.ReadByte()
	if err != nil {
		return 0, nil, err
	}
	var remaining int
	mul := 1
	for i := 0; i < 4; i++ {
		b, err := r.ReadByte()
		if err != nil {
			return 0, nil, err
		}
		remaining += int(b&127) * mul
		if b&128 == 0 {
			break
		}
		mul *= 128
		if i == 3 {
			return 0, nil, errors.New("bad remaining length")
		}
	}
	body := make([]byte, remaining)
	if _, err := io.ReadFull(r, body); err != nil {
		return 0, nil, err
	}
	return hdr, body, nil
}

func mqttString(b []byte) (string, []byte, error) {
	if len(b) < 2 {
		return "", nil, io.ErrUnexpectedEOF
	}
	n := int(binary.BigEndian.Uint16(b[:2]))
	if len(b) < 2+n {
		return "", nil, io.ErrUnexpectedEOF
	}
	return string(b[2 : 2+n]), b[2+n:], nil
}

func appendMQTTString(dst []byte, s string) []byte {
	dst = append(dst, byte(len(s)>>8), byte(len(s)))
	return append(dst, s...)
}

func writeRemaining(w *bufio.Writer, n int) error {
	for {
		digit := byte(n % 128)
		n /= 128
		if n > 0 {
			digit |= 0x80
		}
		if err := w.WriteByte(digit); err != nil {
			return err
		}
		if n == 0 {
			return nil
		}
	}
}
