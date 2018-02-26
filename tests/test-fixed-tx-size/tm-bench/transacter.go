package main

import (
	"crypto/md5"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"sync"
	"time"
	"github.com/gorilla/websocket"
	"github.com/pkg/errors"

	rpctypes "github.com/tendermint/tendermint/rpc/lib/types"
	"github.com/tendermint/tmlibs/log"
	"github.com/BurntSushi/toml"
)

const (
	sendTimeout = 10 * time.Second
	// see https://github.com/tendermint/go-rpc/blob/develop/server/handlers.go#L313
	pingPeriod = (30 * 9 / 10) * time.Second


)
//Read Default Transaction size from config file
//Config file structure
type tomlConfig struct {
        Title string
        Transaction txInfo

}

type txInfo struct {
        Size int
}
//end Config structure

func ReadTxfromConfig() int{

         var conf tomlConfig

        if _, err := toml.DecodeFile("config.toml", &conf); err != nil {
          fmt.Println(err)
        }
        return conf.Transaction.Size

}
type transacter struct {
	Target      string
	Rate        int
	Connections int

	conns   []*websocket.Conn
	wg      sync.WaitGroup
	stopped bool

	logger log.Logger
}

func newTransacter(target string, connections int, rate int) *transacter {
	return &transacter{
		Target:      target,
		Rate:        rate,
		Connections: connections,
		conns:       make([]*websocket.Conn, connections),
		logger:      log.NewNopLogger(),
	}
}

// SetLogger lets you set your own logger
func (t *transacter) SetLogger(l log.Logger) {
	t.logger = l
}


func random(min, max int) int {
    rand.Seed(time.Now().UnixNano())
    return rand.Intn(max - min) + min
}

// Start opens N = `t.Connections` connections to the target and creates read
// and write goroutines for each connection.
func (t *transacter) Start(endpoints []string) error {
	t.stopped = false

	rand.Seed(time.Now().Unix())

	for i := 0; i < t.Connections; i++ {
//		c, _, err := connect(t.Target)
//The below line for multiple endpoints connect connection wise
  c, _, err := connect(endpoints[i])



	if err != nil {
			return err
		}
		t.conns[i] = c
	}


	t.wg.Add(2 * t.Connections)


	for i := 0; i < t.Connections; i++ {

 t.sendLoop(i)

go t.receiveLoop(i)
	}

	return nil
}

// Stop closes the connections.
func (t *transacter) Stop() {
	t.stopped = true
	t.wg.Wait()
	for _, c := range t.conns {
		c.Close()
	}
}

// receiveLoop reads messages from the connection (empty in case of
// `broadcast_tx_async`).
func (t *transacter) receiveLoop(connIndex int) {
	c := t.conns[connIndex]
	defer t.wg.Done()
	for {
		_, _, err := c.ReadMessage()
		if err != nil {

			if !websocket.IsCloseError(err, websocket.CloseNormalClosure) {
				t.logger.Error("failed to read response", "err", err)
			}
			return
		}
		if t.stopped {
			return
		}
	}
}

// sendLoop generates transactions at a given rate.
func (t *transacter) sendLoop(connIndex int)  {

	c := t.conns[connIndex]
	c.SetPingHandler(func(message string) error {
		err := c.WriteControl(websocket.PongMessage, []byte(message), time.Now().Add(sendTimeout))
		if err == websocket.ErrCloseSent {
			return nil
		} else if e, ok := err.(net.Error); ok && e.Temporary() {
			return nil
		}
		return err
	})

	logger := t.logger.With("addr", c.RemoteAddr())

	var txNumber = 0

	pingsTicker := time.NewTicker(pingPeriod)
	txsTicker := time.NewTicker(1 * time.Second)
	defer func() {
		pingsTicker.Stop()
		txsTicker.Stop()
	t.wg.Done()
	}()

	// hash of the host name is a part of each tx
	var hostnameHash [md5.Size]byte
	hostname, err := os.Hostname()
	if err != nil {
		hostname = "127.0.0.1"
	}
	hostnameHash = md5.Sum([]byte(hostname))
	 newTxSize:=ReadTxfromConfig()

		select {
		case <-txsTicker.C:
			startTime := time.Now()


			for i := 0; i < t.Rate; i++ {

			tx := generateTx(connIndex, txNumber, hostnameHash, newTxSize)


			paramsJson, err := json.Marshal(map[string]interface{}{"tx": hex.EncodeToString(tx)})
				if err != nil {
					fmt.Printf("failed to encode params: %v\n", err)
					os.Exit(1)
				}
				rawParamsJson := json.RawMessage(paramsJson)

				c.SetWriteDeadline(time.Now().Add(sendTimeout))
				err = c.WriteJSON(rpctypes.RPCRequest{
					JSONRPC: "2.0",
					ID:      "tm-bench",
					Method:  "broadcast_tx_async",
					Params:  &rawParamsJson,
				})

				if err != nil {
					fmt.Printf("%v. Try reducing the connections count and increasing the rate.\n", errors.Wrap(err, "txs send failed"))
					os.Exit(1)
				}

				txNumber++

			}

			timeToSend := time.Now().Sub(startTime)
			time.Sleep(time.Second - timeToSend)
			logger.Info(fmt.Sprintf("sent %d transactions", t.Rate), "took", timeToSend)
		case <-pingsTicker.C:
			// go-rpc server closes the connection in the absence of pings
			c.SetWriteDeadline(time.Now().Add(sendTimeout))
			if err := c.WriteMessage(websocket.PingMessage, []byte{}); err != nil {
				logger.Error("failed to write ping message", "err", err)
			}
		}

		if t.stopped {
			// To cleanly close a connection, a client should send a close
			// frame and wait for the server to close the connection.
			c.SetWriteDeadline(time.Now().Add(sendTimeout))
			err := c.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
			if err != nil {
				logger.Error("failed to write close message", "err", err)
			}


			return
	}

}

func connect(host string) (*websocket.Conn, *http.Response, error) {
	u := url.URL{Scheme: "ws", Host: host, Path: "/websocket"}
	return websocket.DefaultDialer.Dial(u.String(), nil)
}

func generateTx(connIndex int, txNumber int, hostnameHash [md5.Size]byte,size int) []byte {

        tx := make([]byte, size)

	fmt.Printf("tx length : %d \n ", len(tx))

	binary.PutUvarint(tx[:8], uint64(connIndex))
	binary.PutUvarint(tx[8:16], uint64(txNumber))
	copy(tx[16:32], hostnameHash[:16])
	binary.PutUvarint(tx[32:40], uint64(time.Now().Unix()))

	// 40-* random data
	if _, err := rand.Read(tx[40:]); err != nil {
		panic(errors.Wrap(err, "failed to read random bytes"))
	}

	return tx
}

