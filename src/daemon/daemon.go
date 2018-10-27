/*
Package daemon controls the networking layer of the skycoin daemon
*/
package daemon

import (
	"errors"
	"fmt"
	"reflect"
	"sync"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/skycoin/skycoin/src/cipher"
	"github.com/skycoin/skycoin/src/coin"
	"github.com/skycoin/skycoin/src/daemon/gnet"
	"github.com/skycoin/skycoin/src/daemon/pex"
	"github.com/skycoin/skycoin/src/util/elapse"
	"github.com/skycoin/skycoin/src/util/iputil"
	"github.com/skycoin/skycoin/src/util/logging"
	"github.com/skycoin/skycoin/src/visor"
	"github.com/skycoin/skycoin/src/visor/dbutil"
)

var (
	// ErrDisconnectVersionNotSupported version is below minimum supported version
	ErrDisconnectVersionNotSupported gnet.DisconnectReason = errors.New("Version is below minimum supported version")
	// ErrDisconnectIntroductionTimeout timeout
	ErrDisconnectIntroductionTimeout gnet.DisconnectReason = errors.New("Version timeout")
	// ErrDisconnectVersionSendFailed version send failed
	ErrDisconnectVersionSendFailed gnet.DisconnectReason = errors.New("Version send failed")
	// ErrDisconnectIsBlacklisted is blacklisted
	ErrDisconnectIsBlacklisted gnet.DisconnectReason = errors.New("Blacklisted")
	// ErrDisconnectSelf self connnect
	ErrDisconnectSelf gnet.DisconnectReason = errors.New("Self connect")
	// ErrDisconnectConnectedTwice connect twice
	ErrDisconnectConnectedTwice gnet.DisconnectReason = errors.New("Already connected")
	// ErrDisconnectIdle idle
	ErrDisconnectIdle gnet.DisconnectReason = errors.New("Idle")
	// ErrDisconnectNoIntroduction no introduction
	ErrDisconnectNoIntroduction gnet.DisconnectReason = errors.New("First message was not an Introduction")
	// ErrDisconnectIPLimitReached ip limit reached
	ErrDisconnectIPLimitReached gnet.DisconnectReason = errors.New("Maximum number of connections for this IP was reached")
	// ErrDisconnectIncomprehensibleError this is returned when a seemingly impossible error is encountered
	// e.g. net.Conn.Addr() returns an invalid ip:port
	ErrDisconnectIncomprehensibleError gnet.DisconnectReason = errors.New("Incomprehensible error")
	// ErrDisconnectMaxOutgoingConnectionsReached is returned when connection pool size is greater than the maximum allowed
	ErrDisconnectMaxOutgoingConnectionsReached gnet.DisconnectReason = errors.New("Maximum outgoing connections was reached")
	// ErrDisconnectBlockchainPubkeyNotMatched is returned when the blockchain pubkey in introduction does not match
	ErrDisconnectBlockchainPubkeyNotMatched gnet.DisconnectReason = errors.New("Blockchain pubkey in Introduction message is not matched ")
	// ErrDisconnectInvalidExtraData is returned when extra field can't be parsed as specific data type.
	// e.g. ExtraData length in IntroductionMessage is not the same as cipher.PubKey
	ErrDisconnectInvalidExtraData gnet.DisconnectReason = errors.New("Invalid extra data")

	// ErrOutgoingConnectionsDisabled is returned if outgoing connections are disabled
	ErrOutgoingConnectionsDisabled = errors.New("Outgoing connections are disabled")

	logger = logging.MustGetLogger("daemon")
)

const (
	daemonRunDurationThreshold = time.Millisecond * 200
)

// Config subsystem configurations
type Config struct {
	Daemon   DaemonConfig
	Messages MessagesConfig
	Pool     PoolConfig
	Pex      pex.Config
	Gateway  GatewayConfig
	Visor    visor.Config
}

// NewConfig returns a Config with defaults set
func NewConfig() Config {
	return Config{
		Daemon:   NewDaemonConfig(),
		Pool:     NewPoolConfig(),
		Pex:      pex.NewConfig(),
		Gateway:  NewGatewayConfig(),
		Messages: NewMessagesConfig(),
		Visor:    visor.NewVisorConfig(),
	}
}

// preprocess preprocess for config
func (cfg *Config) preprocess() Config {
	config := *cfg
	if config.Daemon.LocalhostOnly {
		if config.Daemon.Address == "" {
			local, err := iputil.LocalhostIP()
			if err != nil {
				logger.Panicf("Failed to obtain localhost IP: %v", err)
			}
			config.Daemon.Address = local
		} else {
			if !iputil.IsLocalhost(config.Daemon.Address) {
				logger.Panicf("Invalid address for localhost-only: %s", config.Daemon.Address)
			}
		}
		config.Pex.AllowLocalhost = true
	}
	config.Pool.port = config.Daemon.Port
	config.Pool.address = config.Daemon.Address

	if config.Daemon.DisableNetworking {
		logger.Info("Networking is disabled")
		config.Pex.Disabled = true
		config.Daemon.DisableIncomingConnections = true
		config.Daemon.DisableOutgoingConnections = true
	} else {
		if config.Daemon.DisableIncomingConnections {
			logger.Info("Incoming connections are disabled.")
		}
		if config.Daemon.DisableOutgoingConnections {
			logger.Info("Outgoing connections are disabled.")
		}
	}

	return config
}

// DaemonConfig configuration for the Daemon
type DaemonConfig struct { // nolint: golint
	// Protocol version. TODO -- manage version better
	ProtocolVersion int32
	// Minimum accepted protocol version
	MinProtocolVersion int32
	// IP Address to serve on. Leave empty for automatic assignment
	Address string
	// BlockchainPubkey blockchain pubkey string
	BlockchainPubkey cipher.PubKey
	// TCP/UDP port for connections
	Port int
	// Directory where application data is stored
	DataDirectory string
	// How often to check and initiate an outgoing connection if needed
	OutgoingRate time.Duration
	// How often to re-attempt to fill any missing private (aka required)
	// connections
	PrivateRate time.Duration
	// Number of outgoing connections to maintain
	OutgoingMax int
	// Maximum number of connections to try at once
	PendingMax int
	// How long to wait for a version packet
	IntroductionWait time.Duration
	// How often to check for peers that have decided to stop communicating
	CullInvalidRate time.Duration
	// How often to update the database with transaction announcement timestamps
	FlushAnnouncedTxnsRate time.Duration
	// How many connections are allowed from the same base IP
	IPCountsMax int
	// Disable all networking activity
	DisableNetworking bool
	// Don't make outgoing connections
	DisableOutgoingConnections bool
	// Don't allow incoming connections
	DisableIncomingConnections bool
	// Run on localhost and only connect to localhost peers
	LocalhostOnly bool
	// Log ping and pong messages
	LogPings bool
	// How often to request blocks from peers
	BlocksRequestRate time.Duration
	// How often to announce our blocks to peers
	BlocksAnnounceRate time.Duration
	// How many blocks to respond with to a GetBlocksMessage
	BlocksResponseCount uint64
	// Max announce txns hash number
	MaxTxnAnnounceNum int
	// How often new blocks are created by the signing node, in seconds
	BlockCreationInterval uint64
	// How often to check the unconfirmed pool for transactions that become valid
	UnconfirmedRefreshRate time.Duration
	// How often to remove transactions that become permanently invalid from the unconfirmed pool
	UnconfirmedRemoveInvalidRate time.Duration
	// Default "trusted" peers
	DefaultConnections []string
}

// NewDaemonConfig creates daemon config
func NewDaemonConfig() DaemonConfig {
	return DaemonConfig{
		ProtocolVersion:              2,
		MinProtocolVersion:           2,
		Address:                      "",
		Port:                         6677,
		OutgoingRate:                 time.Second * 5,
		PrivateRate:                  time.Second * 5,
		OutgoingMax:                  8,
		PendingMax:                   8,
		IntroductionWait:             time.Second * 30,
		CullInvalidRate:              time.Second * 3,
		FlushAnnouncedTxnsRate:       time.Second * 3,
		IPCountsMax:                  3,
		DisableNetworking:            false,
		DisableOutgoingConnections:   false,
		DisableIncomingConnections:   false,
		LocalhostOnly:                false,
		LogPings:                     true,
		BlocksRequestRate:            time.Second * 60,
		BlocksAnnounceRate:           time.Second * 60,
		BlocksResponseCount:          20,
		MaxTxnAnnounceNum:            16,
		BlockCreationInterval:        10,
		UnconfirmedRefreshRate:       time.Minute,
		UnconfirmedRemoveInvalidRate: time.Minute,
	}
}

//go:generate go install
//go:generate mockery -name daemoner -case underscore -inpkg -testonly

// daemoner Daemon interface
type daemoner interface {
	SendMessage(addr string, msg gnet.Message) error
	BroadcastMessage(msg gnet.Message) error
	Disconnect(addr string, r gnet.DisconnectReason) error
	IsDefaultConnection(addr string) bool
	IsMaxDefaultConnectionsReached() (bool, error)
	PexConfig() pex.Config
	RandomExchangeable(n int) pex.Peers
	AddPeer(addr string) error
	AddPeers(addrs []string) int
	SetHasIncomingPort(addr string) error
	IncreaseRetryTimes(addr string)
	ResetRetryTimes(addr string)
	RecordPeerHeight(addr string, height uint64)
	GetSignedBlocksSince(seq, count uint64) ([]coin.SignedBlock, error)
	HeadBkSeq() (uint64, bool, error)
	ExecuteSignedBlock(b coin.SignedBlock) error
	GetUnconfirmedUnknown(txns []cipher.SHA256) ([]cipher.SHA256, error)
	GetUnconfirmedKnown(txns []cipher.SHA256) (coin.Transactions, error)
	InjectTransaction(txn coin.Transaction) (bool, *visor.ErrTxnViolatesSoftConstraint, error)
	Mirror() uint32
	DaemonConfig() DaemonConfig
	BlockchainPubkey() cipher.PubKey
	RequestBlocksFromAddr(addr string) error
	AnnounceAllTxns() error

	recordMessageEvent(m asyncMessage, c *gnet.MessageContext) error
	connectionIntroduced(addr string, m *IntroductionMessage) (*connection, error)
}

// Daemon stateful properties of the daemon
type Daemon struct {
	// Daemon configuration
	Config DaemonConfig

	// Components
	Messages *Messages
	pool     *Pool
	pex      *pex.Pex
	Gateway  *Gateway
	visor    *visor.Visor

	// Cache of announced transactions that are flushed to the database periodically
	announcedTxns *announcedTxnsCache
	// Cache of connection metadata
	connections *Connections
	// Client connection callbacks
	onConnectEvent chan ConnectEvent
	// Client disconnection callbacks
	onDisconnectEvent chan DisconnectEvent
	// Connection failure events
	connectionErrors chan ConnectionError
	// Message handling queue
	messageEvents chan messageEvent
	// quit channel
	quit chan struct{}
	// done channel
	done chan struct{}
}

// NewDaemon returns a Daemon with primitives allocated
func NewDaemon(config Config, db *dbutil.DB) (*Daemon, error) {
	config = config.preprocess()
	vs, err := visor.NewVisor(config.Visor, db)
	if err != nil {
		return nil, err
	}

	pex, err := pex.New(config.Pex)
	if err != nil {
		return nil, err
	}

	d := &Daemon{
		Config:   config.Daemon,
		Messages: NewMessages(config.Messages),
		pex:      pex,
		visor:    vs,

		announcedTxns: newAnnouncedTxnsCache(),
		connections:   NewConnections(),

		// TODO -- if there are performance problems from blocking chans,
		// Its because we are connecting to more things than OutgoingMax
		// if we have private peers
		onConnectEvent:    make(chan ConnectEvent, config.Pool.MaxConnections*2),
		onDisconnectEvent: make(chan DisconnectEvent, config.Pool.MaxConnections*2),
		connectionErrors:  make(chan ConnectionError, config.Pool.MaxConnections*2),
		messageEvents:     make(chan messageEvent, config.Pool.EventChannelSize),
		quit:              make(chan struct{}),
		done:              make(chan struct{}),
	}

	d.Gateway = NewGateway(config.Gateway, d)
	d.Messages.Config.Register()
	d.pool = NewPool(config.Pool, d)

	return d, nil
}

// ConnectEvent generated when a client connects
type ConnectEvent struct {
	Addr      string
	Solicited bool
}

// DisconnectEvent generated when a connection terminated
type DisconnectEvent struct {
	Addr   string
	Reason gnet.DisconnectReason
}

// ConnectionError represent a failure to connect/dial a connection, with context
type ConnectionError struct {
	Addr  string
	Error error
}

// messageEvent encapsulates a deserialized message from the network
type messageEvent struct {
	Message asyncMessage
	Context *gnet.MessageContext
}

// Shutdown Terminates all subsystems safely.  To stop the Daemon run loop, send a value
// over the quit channel provided to Init.  The Daemon run loop must be stopped
// before calling this function.
func (dm *Daemon) Shutdown() {
	defer logger.Info("Daemon shutdown complete")

	// close daemon run loop first to avoid creating new connection after
	// the connection pool is shutdown.
	logger.Info("Stopping the daemon run loop")
	close(dm.quit)

	logger.Info("Shutting down Pool")
	dm.pool.Shutdown()

	logger.Info("Shutting down Gateway")
	dm.Gateway.Shutdown()

	logger.Info("Shutting down Pex")
	dm.pex.Shutdown()

	<-dm.done
}

// Init prepares daemon before Run()
func (dm *Daemon) Init() error {
	if err := dm.visor.Init(); err != nil {
		logger.WithError(err).Error("visor.Visor.Init failed")
		return err
	}

	return nil
}

// Run main loop for peer/connection management
func (dm *Daemon) Run() error {
	defer logger.Info("Daemon closed")
	defer close(dm.done)

	errC := make(chan error, 5)
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := dm.pex.Run(); err != nil {
			logger.WithError(err).Error("daemon.Pex.Run failed")
			errC <- err
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		if dm.Config.DisableIncomingConnections {
			if err := dm.pool.RunOffline(); err != nil {
				logger.WithError(err).Error("daemon.Pool.RunOffline failed")
				errC <- err
			}
		} else {
			if err := dm.pool.Run(); err != nil {
				logger.WithError(err).Error("daemon.Pool.Run failed")
				errC <- err
			}
		}
	}()

	blockInterval := time.Duration(dm.Config.BlockCreationInterval)
	blockCreationTicker := time.NewTicker(time.Second * blockInterval)
	if !dm.visor.Config.IsMaster {
		blockCreationTicker.Stop()
	}

	unconfirmedRefreshTicker := time.NewTicker(dm.Config.UnconfirmedRefreshRate)
	defer unconfirmedRefreshTicker.Stop()
	unconfirmedRemoveInvalidTicker := time.NewTicker(dm.Config.UnconfirmedRemoveInvalidRate)
	defer unconfirmedRemoveInvalidTicker.Stop()
	blocksRequestTicker := time.NewTicker(dm.Config.BlocksRequestRate)
	defer blocksRequestTicker.Stop()
	blocksAnnounceTicker := time.NewTicker(dm.Config.BlocksAnnounceRate)
	defer blocksAnnounceTicker.Stop()

	privateConnectionsTicker := time.NewTicker(dm.Config.PrivateRate)
	defer privateConnectionsTicker.Stop()
	cullInvalidTicker := time.NewTicker(dm.Config.CullInvalidRate)
	defer cullInvalidTicker.Stop()
	outgoingConnectionsTicker := time.NewTicker(dm.Config.OutgoingRate)
	defer outgoingConnectionsTicker.Stop()
	requestPeersTicker := time.NewTicker(dm.pex.Config.RequestRate)
	defer requestPeersTicker.Stop()
	clearStaleConnectionsTicker := time.NewTicker(dm.pool.Config.ClearStaleRate)
	defer clearStaleConnectionsTicker.Stop()
	idleCheckTicker := time.NewTicker(dm.pool.Config.IdleCheckRate)
	defer idleCheckTicker.Stop()

	flushAnnouncedTxnsTicker := time.NewTicker(dm.Config.FlushAnnouncedTxnsRate)
	defer flushAnnouncedTxnsTicker.Stop()

	// Connect to trusted peers
	if !dm.Config.DisableOutgoingConnections {
		wg.Add(1)
		go func() {
			defer wg.Done()
			dm.connectToTrustPeer()
		}()
	}

	var setupErr error
	elapser := elapse.NewElapser(daemonRunDurationThreshold, logger)

	// Process SendResults in a separate goroutine, otherwise SendResults
	// will fill up much faster than can be processed by the daemon run loop
	// dm.handleMessageSendResult must take care not to perform any operation
	// that would violate thread safety, since it is not serialized by the daemon run loop
	wg.Add(1)
	go func() {
		defer wg.Done()
		elapser := elapse.NewElapser(daemonRunDurationThreshold, logger)
	loop:
		for {
			elapser.CheckForDone()
			select {
			case <-dm.quit:
				break loop

			case r := <-dm.pool.Pool.SendResults:
				// Process message sending results
				elapser.Register("dm.Pool.Pool.SendResults")
				if dm.Config.DisableNetworking {
					logger.Error("There should be nothing in SendResults")
					return
				}
				dm.handleMessageSendResult(r)
			}
		}
	}()

loop:
	for {
		elapser.CheckForDone()
		select {
		case <-dm.quit:
			break loop

		case <-cullInvalidTicker.C:
			// Remove connections that failed to complete the handshake
			elapser.Register("cullInvalidTicker")
			if !dm.Config.DisableNetworking {
				dm.cullInvalidConnections()
			}

		case <-requestPeersTicker.C:
			// Request peers via PEX
			elapser.Register("requestPeersTicker")
			if dm.pex.Config.Disabled {
				continue
			}

			if dm.pex.IsFull() {
				continue
			}

			m := NewGetPeersMessage()
			if err := dm.pool.Pool.BroadcastMessage(m); err != nil {
				logger.Error(err)
			}

		case <-clearStaleConnectionsTicker.C:
			// Remove connections that haven't said anything in a while
			elapser.Register("clearStaleConnectionsTicker")
			if !dm.Config.DisableNetworking {
				dm.pool.clearStaleConnections()
			}

		case <-idleCheckTicker.C:
			// Sends pings as needed
			elapser.Register("idleCheckTicker")
			if !dm.Config.DisableNetworking {
				dm.pool.sendPings()
			}

		case <-outgoingConnectionsTicker.C:
			// Fill up our outgoing connections
			elapser.Register("outgoingConnectionsTicker")
			trustPeerNum := len(dm.pex.Trusted())
			if !dm.Config.DisableOutgoingConnections &&
				dm.connections.OutgoingLen() < (dm.Config.OutgoingMax+trustPeerNum) &&
				dm.connections.PendingLen() < dm.Config.PendingMax {
				dm.connectToRandomPeer()
			}

		case <-privateConnectionsTicker.C:
			// Always try to stay connected to our private peers
			// TODO (also, connect to all of them on start)
			elapser.Register("privateConnectionsTicker")
			if !dm.Config.DisableOutgoingConnections {
				dm.makePrivateConnections()
			}

		case r := <-dm.onConnectEvent:
			// Process callbacks for when a client connects. No disconnect chan
			// is needed because the callback is triggered by HandleDisconnectEvent
			// which is already select{}ed here
			elapser.Register("dm.onConnectEvent")
			if dm.Config.DisableNetworking {
				logger.Error("There should be no connect events")
				return nil
			}
			dm.onConnect(r)

		case de := <-dm.onDisconnectEvent:
			elapser.Register("dm.onDisconnectEvent")
			if dm.Config.DisableNetworking {
				logger.Error("There should be no disconnect events")
				return nil
			}
			dm.onDisconnect(de)

		case r := <-dm.connectionErrors:
			// Handle connection errors
			elapser.Register("dm.connectionErrors")
			if dm.Config.DisableNetworking {
				logger.Error("There should be no connection errors")
				return nil
			}
			dm.handleConnectionError(r)

		case <-flushAnnouncedTxnsTicker.C:
			elapser.Register("flushAnnouncedTxnsTicker")
			txns := dm.announcedTxns.flush()

			if err := dm.visor.SetTransactionsAnnounced(txns); err != nil {
				logger.WithError(err).Error("Failed to set unconfirmed txn announce time")
				return err
			}

		case m := <-dm.messageEvents:
			// Message handlers
			elapser.Register("dm.messageEvents")
			if dm.Config.DisableNetworking {
				logger.Error("There should be no message events")
				return nil
			}
			dm.processMessageEvent(m)

		case req := <-dm.Gateway.requests:
			// Process any pending RPC requests
			elapser.Register("dm.Gateway.requests")
			if err := req.Func(); err != nil {
				logger.WithError(err).Error()
			}

		case <-blockCreationTicker.C:
			// Create blocks, if master chain
			elapser.Register("blockCreationTicker.C")
			if dm.visor.Config.IsMaster {
				sb, err := dm.CreateAndPublishBlock()
				if err != nil {
					logger.Errorf("Failed to create and publish block: %v", err)
					continue
				}

				// Not a critical error, but we want it visible in logs
				head := sb.Block.Head
				logger.Critical().Infof("Created and published a new block, version=%d seq=%d time=%d", head.Version, head.BkSeq, head.Time)
			}

		case <-unconfirmedRefreshTicker.C:
			elapser.Register("unconfirmedRefreshTicker")
			// Get the transactions that turn to valid
			validTxns, err := dm.visor.RefreshUnconfirmed()
			if err != nil {
				logger.Errorf("dm.Visor.RefreshUnconfirmed failed: %v", err)
				continue
			}
			// Announce these transactions
			if err := dm.AnnounceTxns(validTxns); err != nil {
				logger.WithError(err).Warning("AnnounceTxns failed")
			}

		case <-unconfirmedRemoveInvalidTicker.C:
			elapser.Register("unconfirmedRemoveInvalidTicker")
			// Remove transactions that become invalid (violating hard constraints)
			removedTxns, err := dm.visor.RemoveInvalidUnconfirmed()
			if err != nil {
				logger.Errorf("dm.Visor.RemoveInvalidUnconfirmed failed: %v", err)
				continue
			}
			if len(removedTxns) > 0 {
				logger.Infof("Remove %d txns from pool that began violating hard constraints", len(removedTxns))
			}

		case <-blocksRequestTicker.C:
			elapser.Register("blocksRequestTicker")
			if err := dm.RequestBlocks(); err != nil {
				logger.WithError(err).Warning("RequestBlocks failed")
			}

		case <-blocksAnnounceTicker.C:
			elapser.Register("blocksAnnounceTicker")
			if err := dm.AnnounceBlocks(); err != nil {
				logger.WithError(err).Warning("AnnounceBlocks failed")
			}

		case setupErr = <-errC:
			logger.WithError(setupErr).Error("read from errc")
			break loop
		}
	}

	if setupErr != nil {
		return setupErr
	}

	wg.Wait()

	return nil
}

// Connects to a given peer. Returns an error if no connection attempt was
// made. If the connection attempt itself fails, the error is sent to
// the connectionErrors channel.
func (dm *Daemon) connectToPeer(p pex.Peer) error {
	if dm.Config.DisableOutgoingConnections {
		return errors.New("Outgoing connections disabled")
	}

	a, _, err := iputil.SplitAddr(p.Addr)
	if err != nil {
		logger.Warningf("PEX gave us an invalid peer: %v", err)
		return errors.New("Invalid peer")
	}
	if dm.Config.LocalhostOnly && !iputil.IsLocalhost(a) {
		return errors.New("Not localhost")
	}

	if c := dm.connections.get(p.Addr); c != nil {
		return errors.New("Already connected to this peer")
	}

	cnt := dm.connections.IPCount(a)
	if !dm.Config.LocalhostOnly && cnt != 0 {
		return errors.New("Already connected to a peer with this base IP")
	}

	logger.WithField("addr", p.Addr).Debug("Establishing outgoing conneciton")

	if _, err := dm.connections.pending(p.Addr); err != nil {
		logger.Critical().WithError(err).WithField("addr", p.Addr).Error("dm.connections.pending failed")
		return err
	}

	go func() {
		if err := dm.pool.Pool.Connect(p.Addr); err != nil {
			dm.connectionErrors <- ConnectionError{p.Addr, err}
		}
	}()
	return nil
}

// Connects to all private peers
func (dm *Daemon) makePrivateConnections() {
	if dm.Config.DisableOutgoingConnections {
		return
	}

	peers := dm.pex.Private()
	for _, p := range peers {
		logger.Infof("Private peer attempt: %s", p.Addr)
		if err := dm.connectToPeer(p); err != nil {
			logger.Debugf("Did not connect to private peer: %v", err)
		}
	}
}

func (dm *Daemon) connectToTrustPeer() {
	if dm.Config.DisableOutgoingConnections {
		return
	}

	logger.Info("Connect to trusted peers")
	// Make connections to all trusted peers
	peers := dm.pex.TrustedPublic()
	for _, p := range peers {
		if err := dm.connectToPeer(p); err != nil {
			logger.WithError(err).WithField("addr", p.Addr).Warning("connect to trusted peer failed")
		}
	}
}

// Attempts to connect to a random peer. If it fails, the peer is removed.
func (dm *Daemon) connectToRandomPeer() {
	if dm.Config.DisableOutgoingConnections {
		return
	}

	// Make a connection to a random (public) peer
	peers := dm.pex.RandomPublicUntrusted(dm.Config.OutgoingMax)
	for _, p := range peers {
		if err := dm.connectToPeer(p); err != nil {
			logger.WithError(err).WithField("addr", p.Addr).Warning("connectToPeer failed")
		}
	}

	if len(peers) == 0 {
		dm.pex.ResetAllRetryTimes()
	}
}

// handleConnectionError removes the pending connection from connections and
// updates the retry times in pex
func (dm *Daemon) handleConnectionError(c ConnectionError) {
	logger.WithField("addr", c.Addr).WithError(c.Error).Debug("handleConnectionError")
	if err := dm.connections.remove(c.Addr); err != nil {
		logger.Critical().WithField("addr", c.Addr).WithError(err).Error("connections.remove")
	}

	// TODO - On failure to connect, use exponential backoff, not peer list
	dm.pex.IncreaseRetryTimes(c.Addr)
}

// Removes connections who haven't sent a version after connecting
func (dm *Daemon) cullInvalidConnections() {
	now := time.Now().UTC()
	for _, c := range dm.connections.all() {
		if c.State != ConnectionStateConnected {
			continue
		}

		if c.ConnectedAt.Add(dm.Config.IntroductionWait).Before(now) {
			logger.WithField("addr", c.Addr).Infof("Disconnecting peer for not sending a version")
			if err := dm.Disconnect(c.Addr, ErrDisconnectIntroductionTimeout); err != nil {
				logger.WithError(err).WithField("addr", c.Addr).Error("Disconnect")
			}
		}
	}
}

func (dm *Daemon) isTrustedPeer(addr string) bool {
	peer, ok := dm.pex.GetPeer(addr)
	if !ok {
		return false
	}

	return peer.Trusted
}

// recordMessageEvent records an asyncMessage to the messageEvent chan.  Do not access
// messageEvent directly.
func (dm *Daemon) recordMessageEvent(m asyncMessage, c *gnet.MessageContext) error {
	dm.messageEvents <- messageEvent{m, c}
	return nil
}

// needsIntro check if the connection needs introduction message
func (dm *Daemon) needsIntro(addr string) bool {
	c := dm.connections.get(addr)
	if c == nil {
		logger.WithField("addr", addr).Warning("needsIntro did not find a matching connection")
	}
	return c != nil && !c.HasIntroduced()
}

// Processes a queued asyncMessage.
func (dm *Daemon) processMessageEvent(e messageEvent) {
	// The first message received must be an Introduction
	// We have to check at process time and not record time because
	// Introduction message does not update ExpectingIntroductions until its
	// process() is called
	if dm.needsIntro(e.Context.Addr) {
		_, isIntro := e.Message.(*IntroductionMessage)
		if !isIntro {
			logger.WithFields(logrus.Fields{
				"addr":        e.Context.Addr,
				"messageType": fmt.Sprintf("%T", e.Message),
			}).Info("needsIntro but message is not IntroductionMessage")
			if err := dm.pool.Pool.Disconnect(e.Context.Addr, ErrDisconnectNoIntroduction); err != nil {
				logger.WithError(err).WithField("addr", e.Context.Addr).Error("Disconnect")
			}
		}
	}
	e.Message.process(dm)
}

// Called when a ConnectEvent is processed off the onConnectEvent channel
func (dm *Daemon) onConnect(e ConnectEvent) {
	direction := "incoming"
	if e.Solicited {
		direction = "outgoing"
	}
	logger.WithFields(logrus.Fields{
		"addr":      e.Addr,
		"direction": direction,
	}).Info("Connected to peer")

	exist, err := dm.pool.Pool.IsConnExist(e.Addr)
	if err != nil {
		logger.Critical().WithError(err).Error("IsConnExist failed")
		return
	}

	if !exist {
		logger.Warning("While processing an onConnect event, no pool connection was found")
		return
	}

	if dm.ipCountMaxed(e.Addr) {
		logger.WithField("addr", e.Addr).Info("Max connections for this address reached, disconnecting")
		if err := dm.pool.Pool.Disconnect(e.Addr, ErrDisconnectIPLimitReached); err != nil {
			logger.WithError(err).WithField("addr", e.Addr).Error("Disconnect")
		}
		return
	}

	if e.Solicited {
		// Disconnect if the max outgoing connections is reached
		n, err := dm.pool.Pool.OutgoingConnectionsNum()
		if err != nil {
			logger.Critical().WithError(err).Error("OutgoingConnectionsNum failed")
			return
		}

		if n > dm.Config.OutgoingMax {
			logger.WithField("addr", e.Addr).Warning("Max outgoing connections is reached, disconnecting")
			if err := dm.pool.Pool.Disconnect(e.Addr, ErrDisconnectMaxOutgoingConnectionsReached); err != nil {
				logger.WithError(err).WithField("addr", e.Addr).Error("Disconnect")
			}
			return
		}
	}

	// Update the connections state machine
	c, err := dm.connections.connected(e.Addr)
	if err != nil {
		logger.Critical().WithError(err).WithField("addr", e.Addr).Error("connections.Connected failed")
		if err := dm.pool.Pool.Disconnect(e.Addr, ErrDisconnectIncomprehensibleError); err != nil {
			logger.WithError(err).WithField("addr", e.Addr).Error("Disconnect")
		}
		return
	}

	// The connection should already be known as outgoing/solicited due to an earlier connections.pending call.
	// If they do not match, there is e.Addr flaw in the concept or implementation of the state machine.
	if c.Outgoing != e.Solicited {
		logger.Critical().Warning("Connection.Outgoing does not match ConnectEvent.Solicited state")
	}

	logger.WithFields(logrus.Fields{
		"addr":   e.Addr,
		"mirror": dm.Messages.Mirror,
	}).Debug("Sending introduction message")

	m := NewIntroductionMessage(dm.Messages.Mirror, dm.Config.ProtocolVersion, dm.pool.Pool.Config.Port, dm.Config.BlockchainPubkey)
	if err := dm.pool.Pool.SendMessage(e.Addr, m); err != nil {
		logger.WithField("addr", e.Addr).WithError(err).Error("Send IntroductionMessage failed")
	}
}

func (dm *Daemon) onDisconnect(e DisconnectEvent) {
	logger.WithFields(logrus.Fields{
		"addr":   e.Addr,
		"reason": e.Reason,
	}).Info("onDisconnect")

	if err := dm.connections.remove(e.Addr); err != nil {
		logger.WithError(err).WithField("addr", e.Addr).Error("connections.Remove failed")
	}

	// If the peer did not send an introduction in time, it is not a valid peer and remove it from the peer list
	if e.Reason == ErrDisconnectIntroductionTimeout {
		if !dm.isTrustedPeer(e.Addr) {
			dm.pex.RemovePeer(e.Addr)
		}
	}
}

// Triggered when an gnet.Connection terminates
func (dm *Daemon) onGnetDisconnect(addr string, reason gnet.DisconnectReason) {
	e := DisconnectEvent{
		Addr:   addr,
		Reason: reason,
	}
	select {
	case dm.onDisconnectEvent <- e:
	default:
		logger.Warning("onDisconnectEvent channel is full")
	}
}

// Triggered when an gnet.Connection is connected
func (dm *Daemon) onGnetConnect(c *gnet.Connection, solicited bool) {
	dm.onConnectEvent <- ConnectEvent{
		Addr:      c.Addr(),
		Solicited: solicited,
	}
}

// Returns whether the ipCount maximum has been reached.
// Always false when using LocalhostOnly config.
func (dm *Daemon) ipCountMaxed(addr string) bool {
	ip, _, err := iputil.SplitAddr(addr)
	if err != nil {
		logger.Critical().WithField("addr", addr).Error("ipCountMaxed called with invalid addr")
		return true
	}

	return !dm.Config.LocalhostOnly && dm.connections.IPCount(ip) >= dm.Config.IPCountsMax
}

// When an async message send finishes, its result is handled by this.
// This method must take care to perform only thread-safe actions, since it is called
// outside of the daemon run loop
func (dm *Daemon) handleMessageSendResult(r gnet.SendResult) {
	if r.Error != nil {
		logger.Warningf("Failed to send %s to %s: %v", reflect.TypeOf(r.Message), r.Addr, r.Error)
		return
	}
	switch r.Message.(type) {
	case SendingTxnsMessage:
		dm.announcedTxns.add(r.Message.(SendingTxnsMessage).GetFiltered())
	default:
	}
}

// RequestBlocks Sends a GetBlocksMessage to all connections
func (dm *Daemon) RequestBlocks() error {
	if dm.Config.DisableOutgoingConnections {
		return nil
	}

	headSeq, ok, err := dm.HeadBkSeq()
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("Cannot request blocks, there is no head block")
	}

	m := NewGetBlocksMessage(headSeq, dm.Config.BlocksResponseCount)

	err = dm.pool.Pool.BroadcastMessage(m)
	if err != nil {
		logger.Debugf("Broadcast GetBlocksMessage failed: %v", err)
	}

	return err
}

// AnnounceBlocks sends an AnnounceBlocksMessage to all connections
func (dm *Daemon) AnnounceBlocks() error {
	if dm.Config.DisableOutgoingConnections {
		return nil
	}

	headSeq, ok, err := dm.HeadBkSeq()
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("Cannot announce blocks, there is no head block")
	}

	m := NewAnnounceBlocksMessage(headSeq)

	err = dm.pool.Pool.BroadcastMessage(m)
	if err != nil {
		logger.Debugf("Broadcast AnnounceBlocksMessage failed: %v", err)
	}

	return err
}

// AnnounceAllTxns announces local unconfirmed transactions
func (dm *Daemon) AnnounceAllTxns() error {
	if dm.Config.DisableOutgoingConnections {
		return nil
	}

	// Get local unconfirmed transaction hashes.
	hashes, err := dm.visor.GetAllValidUnconfirmedTxHashes()
	if err != nil {
		return err
	}

	// Divide hashes into multiple sets of max size
	hashesSet := divideHashes(hashes, dm.Config.MaxTxnAnnounceNum)

	for _, hs := range hashesSet {
		m := NewAnnounceTxnsMessage(hs)
		if err = dm.pool.Pool.BroadcastMessage(m); err != nil {
			break
		}
	}

	if err != nil {
		logger.Debugf("Broadcast AnnounceTxnsMessage failed, err: %v", err)
	}

	return err
}

func divideHashes(hashes []cipher.SHA256, n int) [][]cipher.SHA256 {
	if len(hashes) == 0 {
		return [][]cipher.SHA256{}
	}

	var j int
	var hashesArray [][]cipher.SHA256

	if len(hashes) > n {
		for i := range hashes {
			if len(hashes[j:i]) == n {
				hs := make([]cipher.SHA256, n)
				copy(hs, hashes[j:i])
				hashesArray = append(hashesArray, hs)
				j = i
			}
		}
	}

	hs := make([]cipher.SHA256, len(hashes)-j)
	copy(hs, hashes[j:])
	hashesArray = append(hashesArray, hs)
	return hashesArray
}

// AnnounceTxns announces given transaction hashes.
func (dm *Daemon) AnnounceTxns(txns []cipher.SHA256) error {
	if dm.Config.DisableOutgoingConnections {
		return nil
	}

	if len(txns) == 0 {
		return nil
	}

	m := NewAnnounceTxnsMessage(txns)

	err := dm.pool.Pool.BroadcastMessage(m)
	if err != nil {
		logger.Debugf("Broadcast AnnounceTxnsMessage failed: %v", err)
	}

	return err
}

// RequestBlocksFromAddr sends a GetBlocksMessage to one connected address
func (dm *Daemon) RequestBlocksFromAddr(addr string) error {
	if dm.Config.DisableOutgoingConnections {
		return errors.New("Outgoing connections disabled")
	}

	headSeq, ok, err := dm.visor.HeadBkSeq()
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("Cannot request blocks from addr, there is no head block")
	}

	m := NewGetBlocksMessage(headSeq, dm.Config.BlocksResponseCount)

	return dm.pool.Pool.SendMessage(addr, m)
}

// ResendUnconfirmedTxns resends all unconfirmed transactions and returns the hashes that were successfully rebroadcast.
// It does not return an error if broadcasting fails.
func (dm *Daemon) ResendUnconfirmedTxns() ([]cipher.SHA256, error) {
	if dm.Config.DisableOutgoingConnections {
		return nil, nil
	}

	txns, err := dm.visor.GetAllUnconfirmedTransactions()
	if err != nil {
		return nil, err
	}

	var txids []cipher.SHA256
	for i := range txns {
		logger.Debugf("Rebroadcast txn %s", txns[i].Hash().Hex())
		if err := dm.BroadcastTransaction(txns[i].Transaction); err == nil {
			txids = append(txids, txns[i].Transaction.Hash())
		}
	}

	return txids, nil
}

// BroadcastTransaction broadcasts a single transaction to all peers.
func (dm *Daemon) BroadcastTransaction(t coin.Transaction) error {
	if dm.Config.DisableOutgoingConnections {
		return ErrOutgoingConnectionsDisabled
	}

	l, err := dm.pool.Pool.Size()
	if err != nil {
		return err
	}

	logger.Debugf("BroadcastTransaction to %d conns", l)

	m := NewGiveTxnsMessage(coin.Transactions{t})
	if err := dm.pool.Pool.BroadcastMessage(m); err != nil {
		logger.WithError(err).Error("BroadcastTransaction Pool.BroadcastMessage failed")
		return err
	}

	return nil
}

// CreateAndPublishBlock creates a block from unconfirmed transactions and sends it to the network.
// Will panic if not running as a master chain.
// Will not create a block if outgoing connections are disabled.
// If the block was created but the broadcast failed, the error will be non-nil but the
// SignedBlock value will not be empty.
// TODO -- refactor this method -- it should either always create a block and maybe broadcast it,
// or use a database transaction to rollback block publishing if broadcast failed (however, this will cause a slow DB write)
func (dm *Daemon) CreateAndPublishBlock() (*coin.SignedBlock, error) {
	if dm.Config.DisableOutgoingConnections {
		return nil, errors.New("Outgoing connections disabled")
	}

	sb, err := dm.visor.CreateAndExecuteBlock()
	if err != nil {
		return nil, err
	}

	err = dm.broadcastBlock(sb)

	return &sb, err
}

// Sends a signed block to all connections.
func (dm *Daemon) broadcastBlock(sb coin.SignedBlock) error {
	if dm.Config.DisableOutgoingConnections {
		return nil
	}

	m := NewGiveBlocksMessage([]coin.SignedBlock{sb})
	return dm.pool.Pool.BroadcastMessage(m)
}

// Mirror returns the message mirror
func (dm *Daemon) Mirror() uint32 {
	return dm.Messages.Mirror
}

// DaemonConfig returns the daemon config
func (dm *Daemon) DaemonConfig() DaemonConfig {
	return dm.Config
}

// BlockchainPubkey returns the blockchain pubkey
func (dm *Daemon) BlockchainPubkey() cipher.PubKey {
	return dm.Config.BlockchainPubkey
}

// connectionIntroduced removes the peer from expect introduction pool
func (dm *Daemon) connectionIntroduced(addr string, m *IntroductionMessage) (*connection, error) {
	return dm.connections.introduced(addr, m)
}

// Implements pooler interface

// SendMessage sends a Message to a Connection and pushes the result onto the
// SendResults channel.
func (dm *Daemon) SendMessage(addr string, msg gnet.Message) error {
	return dm.pool.Pool.SendMessage(addr, msg)
}

// BroadcastMessage sends a Message to all connections in the Pool.
func (dm *Daemon) BroadcastMessage(msg gnet.Message) error {
	return dm.pool.Pool.BroadcastMessage(msg)
}

// Disconnect removes a connection from the pool by address, and passes a Disconnection to
// the DisconnectCallback
func (dm *Daemon) Disconnect(addr string, r gnet.DisconnectReason) error {
	return dm.pool.Pool.Disconnect(addr, r)
}

// IsDefaultConnection returns if the addr is a default connection
func (dm *Daemon) IsDefaultConnection(addr string) bool {
	return dm.pool.Pool.IsDefaultConnection(addr)
}

// IsMaxDefaultConnectionsReached returns whether the max default connection number was reached.
func (dm *Daemon) IsMaxDefaultConnectionsReached() (bool, error) {
	return dm.pool.Pool.IsMaxDefaultConnectionsReached()
}

// Implements pexer interface

// RandomExchangeable returns N random exchangeable peers
func (dm *Daemon) RandomExchangeable(n int) pex.Peers {
	return dm.pex.RandomExchangeable(n)
}

// PexConfig returns the pex config
func (dm *Daemon) PexConfig() pex.Config {
	return dm.pex.Config
}

// AddPeer adds peer to the pex
func (dm *Daemon) AddPeer(addr string) error {
	return dm.pex.AddPeer(addr)
}

// AddPeers adds peers to the pex
func (dm *Daemon) AddPeers(addrs []string) int {
	return dm.pex.AddPeers(addrs)
}

// SetHasIncomingPort sets the peer public peer
func (dm *Daemon) SetHasIncomingPort(addr string) error {
	return dm.pex.SetHasIncomingPort(addr, true)
}

// IncreaseRetryTimes increases the retry times of given peer
func (dm *Daemon) IncreaseRetryTimes(addr string) {
	dm.pex.IncreaseRetryTimes(addr)
}

// ResetRetryTimes reset the retry times of given peer
func (dm *Daemon) ResetRetryTimes(addr string) {
	dm.pex.ResetRetryTimes(addr)
}

// Implements chain height store

// RecordPeerHeight records the height of specific peer
func (dm *Daemon) RecordPeerHeight(addr string, height uint64) {
	if err := dm.connections.SetHeight(addr, height); err != nil {
		logger.Critical().WithError(err).WithField("addr", addr).Error("connections.SetHeight failed")
	}
}

// Implements visorer interface

// GetSignedBlocksSince returns N signed blocks since given seq
func (dm *Daemon) GetSignedBlocksSince(seq, count uint64) ([]coin.SignedBlock, error) {
	return dm.visor.GetSignedBlocksSince(seq, count)
}

// HeadBkSeq returns the head block sequence
func (dm *Daemon) HeadBkSeq() (uint64, bool, error) {
	return dm.visor.HeadBkSeq()
}

// ExecuteSignedBlock executes the signed block
func (dm *Daemon) ExecuteSignedBlock(b coin.SignedBlock) error {
	return dm.visor.ExecuteSignedBlock(b)
}

// GetUnconfirmedUnknown returns unconfirmed txn hashes with known ones removed
func (dm *Daemon) GetUnconfirmedUnknown(txns []cipher.SHA256) ([]cipher.SHA256, error) {
	return dm.visor.GetUnconfirmedUnknown(txns)
}

// GetUnconfirmedKnown returns unconfirmed txn hashes with known ones removed
func (dm *Daemon) GetUnconfirmedKnown(txns []cipher.SHA256) (coin.Transactions, error) {
	return dm.visor.GetUnconfirmedKnown(txns)
}

// InjectTransaction records a coin.Transaction to the UnconfirmedTxnPool if the txn is not
// already in the blockchain.
// The bool return value is whether or not the transaction was already in the pool.
// If the transaction violates hard constraints, it is rejected, and error will not be nil.
// If the transaction only violates soft constraints, it is still injected, and the soft constraint violation is returned.
func (dm *Daemon) InjectTransaction(txn coin.Transaction) (bool, *visor.ErrTxnViolatesSoftConstraint, error) {
	return dm.visor.InjectTransaction(txn)
}
