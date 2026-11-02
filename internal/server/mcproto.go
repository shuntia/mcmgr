package server

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/Tnze/go-mc/bot"
	"github.com/Tnze/go-mc/chat"
	mcnet "github.com/Tnze/go-mc/net"
	pk "github.com/Tnze/go-mc/net/packet"
)

// The little of the Minecraft protocol the idle proxy speaks itself: reading
// the handshake every connection opens with, answering a server-list ping
// while the server is asleep, and turning a joining player away with a
// message. These packets have kept their shape since 1.7, so one
// implementation serves every client version. The wire format comes from
// go-mc.

// Handshake intents, the "next state" a client asks for.
const (
	intentStatus   = 1 // server-list ping
	intentLogin    = 2 // a player joining
	intentTransfer = 3 // a player sent over by another server (1.20.5+)
)

// legacyPing is the first byte of the pre-1.7 server-list ping, which is not
// length-prefixed and so cannot be read as a packet.
const legacyPing = 0xFE

// handshake is the first packet of every modern connection.
type handshake struct {
	Protocol pk.VarInt
	Address  pk.String
	Port     pk.UnsignedShort
	Intent   pk.VarInt

	// packet is the handshake as received, replayed to the server when the
	// connection is handed over.
	packet pk.Packet
}

// readHandshake reads the opening packet of a connection.
func readHandshake(c *mcnet.Conn) (handshake, error) {
	var h handshake
	if err := c.ReadPacket(&h.packet); err != nil {
		return h, err
	}
	if h.packet.ID != 0x00 {
		return h, fmt.Errorf("expected a handshake, got packet 0x%02x", h.packet.ID)
	}
	err := h.packet.Scan(&h.Protocol, &h.Address, &h.Port, &h.Intent)
	return h, err
}

// wrapClient frames a client connection whose first bytes may already sit in
// br, having been peeked at.
func wrapClient(conn net.Conn, br *bufio.Reader) *mcnet.Conn {
	c := mcnet.WrapConn(conn)
	c.Reader = br
	return c
}

// kickDuringLogin turns a joining player away with a message, which their
// client shows on the disconnect screen. In the login state the reason is a
// JSON text component on every version.
func kickDuringLogin(c *mcnet.Conn, message string) error {
	return c.WritePacket(pk.Marshal(0x00, chat.Text(message)))
}

// answerStatus answers a server-list ping on the server's behalf: the status
// request with doc, then the ping with its own payload, which is how the
// client measures latency.
func answerStatus(c *mcnet.Conn, doc []byte) error {
	for {
		var p pk.Packet
		if err := c.ReadPacket(&p); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		switch p.ID {
		case 0x00:
			if err := c.WritePacket(pk.Marshal(0x00, pk.String(doc))); err != nil {
				return err
			}
		case 0x01:
			return c.WritePacket(pk.Packet{ID: 0x01, Data: p.Data})
		default:
			return fmt.Errorf("unexpected status packet 0x%02x", p.ID)
		}
	}
}

// queryStatus performs a server-list ping against addr and returns the
// server's status document.
func queryStatus(addr string, timeout time.Duration) ([]byte, error) {
	doc, _, err := bot.PingAndListTimeout(addr, timeout)
	return doc, err
}

// serverStatus is the part of a status document mcmgr reads.
type serverStatus struct {
	Version struct {
		Name     string `json:"name"`
		Protocol int32  `json:"protocol"`
	} `json:"version"`
	Players struct {
		Max    int `json:"max"`
		Online int `json:"online"`
	} `json:"players"`
}
