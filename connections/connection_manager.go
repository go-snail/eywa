package connections

import (
	"fmt"
	"github.com/vivowares/octopus/Godeps/_workspace/src/github.com/gorilla/websocket"
	"github.com/vivowares/octopus/Godeps/_workspace/src/github.com/spaolacci/murmur3"
	. "github.com/vivowares/octopus/configs"
	. "github.com/vivowares/octopus/utils"
	"strconv"
	"sync"
	"time"
)

var WSCM *WebSocketConnectionManager

func InitializeWSCM() error {
	wscm, err := NewWebSocketConnectionManager()
	WSCM = wscm
	return err
}

func NewWebSocketConnectionManager() (*WebSocketConnectionManager, error) {
	wscm := &WebSocketConnectionManager{}
	switch Config().Connections.Registry {
	case "memory":
		wscm.Registry = &InMemoryRegistry{}
	default:
		wscm.Registry = &InMemoryRegistry{}
	}
	if err := wscm.Registry.Ping(); err != nil {
		return nil, err
	}

	wscm.shards = make([]*shard, Config().Connections.NShards)
	for i := 0; i < Config().Connections.NShards; i++ {
		wscm.shards[i] = &shard{
			wscm:  wscm,
			conns: make(map[string]*Connection, Config().Connections.InitShardSize),
		}
	}

	return wscm, nil
}

type WebSocketConnectionManager struct {
	shards   []*shard
	Registry Registry
}

func (wscm *WebSocketConnectionManager) Close() error {
	var wg sync.WaitGroup
	wg.Add(len(wscm.shards))
	for _, sh := range wscm.shards {
		go func(s *shard) {
			s.Close()
			wg.Done()
		}(sh)
	}
	wg.Wait()
	return wscm.Registry.Close()
}

func (wscm *WebSocketConnectionManager) NewConnection(id string, ws wsConn, h MessageHandler, meta map[string]interface{}) (*Connection, error) {
	hasher := murmur3.New32()
	hasher.Write([]byte(id))
	shard := wscm.shards[hasher.Sum32()%uint32(len(wscm.shards))]

	t := time.Now()
	conn := &Connection{
		shard:        shard,
		ws:           ws,
		identifier:   id,
		createdAt:    t,
		lastPingedAt: t,
		h:            h,
		Metadata:     meta,

		wch: make(chan *MessageReq, Config().Connections.RequestQueueSize),
		msgChans: &syncRespChanMap{
			m: make(map[string]chan *MessageResp),
		},
		closewch: make(chan bool, 1),
		rch:      make(chan struct{}),
	}

	ws.SetPingHandler(func(payload string) error {
		conn.lastPingedAt = time.Now()
		conn.shard.updateRegistry(conn)
		Logger.Debug(fmt.Sprintf("connection: %s pinged", id))
		return ws.WriteControl(
			websocket.PongMessage,
			[]byte(strconv.FormatInt(time.Now().UnixNano()/int64(time.Millisecond), 10)),
			time.Now().Add(Config().Connections.Timeouts.Write))
	})

	conn.Start()
	if err := shard.register(conn); err != nil {
		conn.Close()
		conn.Wait()
		return nil, err
	}

	return conn, nil
}

func (wscm *WebSocketConnectionManager) FindConnection(id string) (*Connection, bool) {
	hasher := murmur3.New32()
	hasher.Write([]byte(id))
	shard := wscm.shards[hasher.Sum32()%uint32(len(wscm.shards))]
	return shard.findConnection(id)
}

func (wscm *WebSocketConnectionManager) Count() int {
	sum := 0
	for _, sh := range wscm.shards {
		sum += sh.Count()
	}
	return sum
}
