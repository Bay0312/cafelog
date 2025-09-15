package broker

import (
	"log"
	"net"
)

type Broker struct {
	// TODO: topics, partitions, offset store, metrics, cfg
}

func New() *Broker { return &Broker{} }

func (b *Broker) ServeTCP(l net.Listener) error {
	log.Printf("TCP broker listening on %s", l.Addr())
	for {
		conn, err := l.Accept()
		if err != nil {
			return err
		}
		go func(c net.Conn) {
			defer c.Close()
			// TODO: read frames and dispatch
		}(conn)
	}
}
