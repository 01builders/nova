package abci

import (
	"context"
	"errors"
	"fmt"
	"github.com/01builders/nova/internal"
	cmtcfg "github.com/cometbft/cometbft/config"
	"github.com/cometbft/cometbft/node"
	"github.com/cometbft/cometbft/p2p"
	pvm "github.com/cometbft/cometbft/privval"
	"github.com/cometbft/cometbft/proxy"
	"github.com/cometbft/cometbft/rpc/client/local"
	db "github.com/cosmos/cosmos-db"
	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/codec"
	"github.com/cosmos/cosmos-sdk/server"
	"github.com/cosmos/cosmos-sdk/server/api"
	serverconfig "github.com/cosmos/cosmos-sdk/server/config"
	servergrpc "github.com/cosmos/cosmos-sdk/server/grpc"
	servercmtlog "github.com/cosmos/cosmos-sdk/server/log"
	"github.com/cosmos/cosmos-sdk/telemetry"
	"github.com/cosmos/cosmos-sdk/version"
	"github.com/hashicorp/go-metrics"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc/credentials/insecure"
	"io"
	"math"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"cosmossdk.io/log"
	servertypes "github.com/cosmos/cosmos-sdk/server/types"
	"google.golang.org/grpc"

	"github.com/01builders/nova/appd"
)

const (
	flagTraceStore = "trace-store"
	flagGRPCOnly   = "grpc-only"
)

type Multiplexer struct {
	logger log.Logger
	mu     sync.Mutex

	svrCtx *server.Context
	svrCfg serverconfig.Config
	cmtCfg *cmtcfg.Config

	lastAppVersion uint64
	started        bool

	appCreator    servertypes.AppCreator
	latestApp     servertypes.ABCI
	activeVersion Version
	chainID       string
	tmNode        *node.Node

	versions      Versions
	conn          *grpc.ClientConn
	cleanupFns    []func() error
	clientContext client.Context
}

// NewVersions returns a list of versions sorted by app version.
func NewVersions(v ...Version) (Versions, error) {
	versions := Versions(v)
	if err := versions.Validate(); err != nil {
		return nil, err
	}
	return versions.Sorted(), nil
}

// NewMultiplexer creates a new ABCI wrapper for multiplexing
func NewMultiplexer(svrCtx *server.Context, svrCfg serverconfig.Config, clientCtx client.Context, appCreator servertypes.AppCreator, versions Versions, chainID string, applicationVersion uint64) (*Multiplexer, error) {
	if err := versions.Validate(); err != nil {
		return nil, fmt.Errorf("invalid versions: %w", err)
	}

	mp := &Multiplexer{
		svrCtx:         svrCtx,
		svrCfg:         svrCfg,
		clientContext:  clientCtx,
		appCreator:     appCreator,
		logger:         svrCtx.Logger.With("multiplexer"),
		latestApp:      nil, // app will be initialized if required by the multiplexer.
		versions:       versions,
		chainID:        chainID,
		lastAppVersion: applicationVersion,
		cleanupFns:     make([]func() error, 0),
	}

	return mp, nil
}

// isGrpcOnly checks if the GRPC-only mode is enabled using the configuration flag.
func (m *Multiplexer) isGrpcOnly() bool {
	return m.svrCtx.Viper.GetBool(flagGRPCOnly)
}

// registerCleanupFn enables the registration of additional cleanup functions that get called during Cleanup
func (m *Multiplexer) registerCleanupFn(cleanUpFn func() error) {
	m.cleanupFns = append(m.cleanupFns, cleanUpFn)
}

func (m *Multiplexer) Start() error {
	g, ctx := getCtx(m.svrCtx, true)

	emitServerInfoMetrics()

	// startApp starts the underlying application, either native or embedded.
	if err := m.startApp(); err != nil {
		return err
	}

	if !m.isGrpcOnly() {
		m.logger.Info("starting comet node")
		if m.tmNode == nil { // TODO: filthy hack
			if err := m.startCmtNode(ctx); err != nil {
				return err
			}
			m.logger.Info("comet node started successfully")
		}
	} else {
		m.logger.Info("starting node in gRPC only mode; CometBFT is disabled")
		m.svrCfg.GRPC.Enable = true // TODO: this shouldn't be required?
	}

	app, ok := m.latestApp.(servertypes.Application)
	if !ok {
		m.logger.Info("using embedded app")
	}

	// startGRPC the grpc server in the case of a native app. If using an embedded app
	// it will use that instead.
	if m.svrCfg.GRPC.Enable {
		if m.svrCfg.API.Enable || m.svrCfg.GRPC.Enable {
			// Re-assign for making the client available below do not use := to avoid
			// shadowing the clientCtx variable.
			m.clientContext = m.clientContext.WithClient(local.New(m.tmNode))
			app.RegisterTxService(m.clientContext)
			app.RegisterTendermintService(m.clientContext)
			app.RegisterNodeService(m.clientContext, m.svrCfg)
		}

		grpcServer, clientContext, err := m.startGRPCServer(ctx, g, m.svrCfg.GRPC, m.clientContext)
		if err != nil {
			return err
		}
		m.clientContext = clientContext // update client context with grpc

		// startAPIServer starts the api server for a native app. If using an embedded app
		// it will use that instead.
		if m.svrCfg.API.Enable {
			metrics, err := startTelemetry(m.svrCfg)
			if err != nil {
				return err
			}

			if err := m.startAPIServer(ctx, g, m.cmtCfg.RootDir, grpcServer, metrics); err != nil {
				return err
			}
		}

	}

	return g.Wait()
}

// startApp starts either the native app, or an embedded app.
func (m *Multiplexer) startApp() error {
	// prepare correct version
	currentVersion, err := m.versions.GetForAppVersion(m.lastAppVersion)
	if err != nil && errors.Is(err, ErrNoVersionFound) {
		// no version found, assume latest
		m.logger.Info("starting native app", "app_version", m.lastAppVersion)
		if err := m.startNativeApp(); err != nil {
			return fmt.Errorf("failed to start native app: %w", err)
		}
		return nil
	} else if err != nil {
		return fmt.Errorf("failed to get app for version %d: %w", m.lastAppVersion, err)
	}

	// start the correct version
	if currentVersion.Appd == nil {
		return fmt.Errorf("appd is nil for version %d", m.lastAppVersion)
	}

	if currentVersion.Appd.Pid() == appd.AppdStopped {
		programArgs := os.Args
		if len(os.Args) > 2 {
			programArgs = os.Args[2:] // remove 'appd start' args
		}

		// start an embedded app.
		m.logger.Info("starting embedded app", "app_version", currentVersion.AppVersion, "args", currentVersion.GetStartArgs(programArgs))
		if err := currentVersion.Appd.Start(currentVersion.GetStartArgs(programArgs)...); err != nil {
			return fmt.Errorf("failed to start app: %w", err)
		}

		if currentVersion.Appd.Pid() == appd.AppdStopped { // should never happen
			return fmt.Errorf("app failed to start")
		}

		m.started = true
		m.activeVersion = currentVersion
	}

	return m.initRemoteGrpcConn()
}

// initRemoteGrpcConn initializes a gRPC connection to the remote application client and configures transport credentials.
func (m *Multiplexer) initRemoteGrpcConn() error {
	// prepare remote app client
	const flagTMAddress = "address"
	tmAddress := m.svrCtx.Viper.GetString(flagTMAddress)
	if tmAddress == "" {
		tmAddress = "127.0.0.1:26658"
	}

	// remove tcp:// prefix if present
	tmAddress = strings.TrimPrefix(tmAddress, "tcp://")

	conn, err := grpc.NewClient(
		tmAddress,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallSendMsgSize(math.MaxInt),
			grpc.MaxCallRecvMsgSize(math.MaxInt),
		),
	)
	if err != nil {
		return fmt.Errorf("failed to prepare app connection: %w", err)
	}

	m.logger.Info("initialized remote app client", "address", tmAddress)
	m.conn = conn
	return nil
}

// startGRPCServer initializes and starts a gRPC server if enabled in the configuration, returning the server and updated context.
func (m *Multiplexer) startGRPCServer(ctx context.Context, g *errgroup.Group, config serverconfig.GRPCConfig, clientCtx client.Context) (*grpc.Server, client.Context, error) {
	_, _, err := net.SplitHostPort(config.Address)
	if err != nil {
		return nil, clientCtx, err
	}

	maxSendMsgSize := config.MaxSendMsgSize
	if maxSendMsgSize == 0 {
		maxSendMsgSize = serverconfig.DefaultGRPCMaxSendMsgSize
	}

	maxRecvMsgSize := config.MaxRecvMsgSize
	if maxRecvMsgSize == 0 {
		maxRecvMsgSize = serverconfig.DefaultGRPCMaxRecvMsgSize
	}

	// if gRPC is enabled, configure gRPC client for gRPC gateway
	grpcClient, err := grpc.NewClient(
		config.Address,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(
			grpc.ForceCodec(codec.NewProtoCodec(clientCtx.InterfaceRegistry).GRPCCodec()),
			grpc.MaxCallRecvMsgSize(maxRecvMsgSize),
			grpc.MaxCallSendMsgSize(maxSendMsgSize),
		),
	)
	if err != nil {
		return nil, clientCtx, err
	}

	clientCtx = clientCtx.WithGRPCClient(grpcClient)
	m.logger.Debug("gRPC client assigned to client context", "target", config.Address)

	app, ok := m.latestApp.(servertypes.Application)
	if !ok {
		// should never happen as we only want to start a new grpc server if we're using the latest app.
		return nil, clientCtx, fmt.Errorf("latest app is not an application")
	}

	grpcSrv, err := servergrpc.NewGRPCServer(clientCtx, app, config)
	if err != nil {
		return nil, clientCtx, err
	}

	// Start the gRPC server in a goroutine. Note, the provided ctx will ensure
	// that the server is gracefully shut down.
	g.Go(func() error {
		return servergrpc.StartGRPCServer(ctx, m.svrCtx.Logger.With(log.ModuleKey, "grpc-server"), config, grpcSrv)
	})

	m.conn = grpcClient
	return grpcSrv, clientCtx, nil
}

func (m *Multiplexer) startAPIServer(ctx context.Context, g *errgroup.Group, home string, grpcSrv *grpc.Server, metrics *telemetry.Metrics) error {

	app, ok := m.latestApp.(servertypes.Application)
	if !ok {
		// should never happen
		return fmt.Errorf("latest app is not a valid application type")
	}

	m.clientContext = m.clientContext.WithHomeDir(home)

	apiSrv := api.New(m.clientContext, m.svrCtx.Logger.With(log.ModuleKey, "api-server"), grpcSrv)
	app.RegisterAPIRoutes(apiSrv, m.svrCfg.API)

	if m.svrCfg.Telemetry.Enabled {
		apiSrv.SetTelemetry(metrics)
	}

	g.Go(func() error {
		return apiSrv.Start(ctx, m.svrCfg)
	})
	return nil
}

// startNativeApp starts a native app.
func (m *Multiplexer) startNativeApp() error {
	traceWriter, traceCleanupFn, err := setupTraceWriter(m.svrCtx)
	if err != nil {
		return err
	}
	m.registerCleanupFn(func() error {
		traceCleanupFn()
		return nil
	})

	home := m.svrCtx.Config.RootDir
	db, err := openDB(home, server.GetAppDBBackend(m.svrCtx.Viper))
	if err != nil {
		return err
	}

	app := m.appCreator(m.logger, db, traceWriter, m.svrCtx.Viper)
	m.latestApp = app
	m.started = true

	m.registerCleanupFn(func() error {
		return app.Close()
	})

	return nil
}

func setupTraceWriter(svrCtx *server.Context) (traceWriter io.WriteCloser, cleanup func(), err error) {
	// clean up the traceWriter when the server is shutting down
	cleanup = func() {}

	traceWriterFile := svrCtx.Viper.GetString(flagTraceStore)
	traceWriter, err = openTraceWriter(traceWriterFile)
	if err != nil {
		return traceWriter, cleanup, err
	}

	// if flagTraceStore is not used then traceWriter is nil
	if traceWriter != nil {
		cleanup = func() {
			if err = traceWriter.Close(); err != nil {
				svrCtx.Logger.Error("failed to close trace writer", "err", err)
			}
		}
	}

	return traceWriter, cleanup, nil
}

func openDB(rootDir string, backendType db.BackendType) (db.DB, error) {
	dataDir := filepath.Join(rootDir, "data")
	return db.NewDB("application", backendType, dataDir)
}

func openTraceWriter(traceWriterFile string) (w io.WriteCloser, err error) {
	if traceWriterFile == "" {
		return
	}
	return os.OpenFile(
		traceWriterFile,
		os.O_WRONLY|os.O_APPEND|os.O_CREATE,
		0o666,
	)
}

// getApp gets the appropriate app based on the latest application version.
func (m *Multiplexer) getApp() (servertypes.ABCI, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// get the appropriate version for the latest app version.
	currentVersion, err := m.versions.GetForAppVersion(m.lastAppVersion)
	if err != nil {
		// if we are switching from an embedded binary to a native one, we need to ensure that we stop it
		// before we start the native app.
		if err := m.StopActiveVersion(); err != nil {
			return nil, fmt.Errorf("failed to stop active version: %w", err)
		}

		m.logger.Info("No app found in multiplexer for app version; using latest app", "app_version", m.lastAppVersion, "err", err)
		if !m.started {
			m.logger.Info("starting latest app")
			if err := m.Start(); err != nil {
				return nil, fmt.Errorf("failed to start latest app: %w", err)
			}
		}
		return m.latestApp, nil
	}

	// check if we need to start the app or if we have a different app running
	if !m.started || currentVersion.AppVersion != m.activeVersion.AppVersion {
		if err := m.startEmbeddedApp(currentVersion); err != nil {
			return nil, fmt.Errorf("failed to start embedded app: %w", err)
		}
	}

	m.logger.Info("Using ABCI remote connection", "maximum_app_version", currentVersion.AppVersion, "abci_version", currentVersion.ABCIVersion.String(), "chain_id", m.chainID)

	switch currentVersion.ABCIVersion {
	case ABCIClientVersion1:
		return NewRemoteABCIClientV1(m.conn, m.chainID), nil
	case ABCIClientVersion2:
		return NewRemoteABCIClientV2(m.conn), nil
	}

	return nil, fmt.Errorf("unknown ABCI client version %d", currentVersion.ABCIVersion)
}

// startEmbeddedApp starts an embedded version of the app.
func (m *Multiplexer) startEmbeddedApp(version Version) error {
	m.logger.Info("Starting embedded app", "app_version", version.AppVersion, "abci_version", version.ABCIVersion.String())
	if version.Appd == nil {
		return fmt.Errorf("appd is nil for version %d", m.activeVersion.AppVersion)
	}

	// stop the existing app version if one is currently running.
	if err := m.StopActiveVersion(); err != nil {
		return fmt.Errorf("failed to stop active version: %w", err)
	}

	if version.Appd.Pid() == appd.AppdStopped {
		for _, preHandler := range version.PreHandlers {
			preCmd := version.Appd.CreateExecCommand(preHandler)
			if err := preCmd.Run(); err != nil {
				m.logger.Info("Warning: PreHandler failed", "err", err)
				// Continue anyway as the pre-handler might be optional
			}
		}

		// start the new app
		programArgs := os.Args
		if len(os.Args) > 2 {
			programArgs = os.Args[2:] // Remove 'appd start' args
		}

		m.logger.Info("Starting app for version", "app_version", version.AppVersion, "args", programArgs)
		if err := version.Appd.Start(version.GetStartArgs(programArgs)...); err != nil {
			return fmt.Errorf("failed to start app for version %d: %w", m.lastAppVersion, err)
		}

		if version.Appd.Pid() == appd.AppdStopped {
			return fmt.Errorf("app for version %d failed to start", m.latestApp)
		}

		m.activeVersion = version
		m.started = true
	}
	return nil
}

// embeddedVersionRunning returns true if there is an active version specified which is not stopped.
func (m *Multiplexer) embeddedVersionRunning() bool {
	return m.activeVersion.Appd != nil && m.activeVersion.Appd.Pid() != appd.AppdStopped
}

// StopActiveVersion stops any embedded app versions if they are currently running.
func (m *Multiplexer) StopActiveVersion() error {
	if m.embeddedVersionRunning() {
		m.logger.Info("Stopping app for version", "active_app_version", m.activeVersion.AppVersion)
		if err := m.activeVersion.Appd.Stop(); err != nil {
			return fmt.Errorf("failed to stop app for version %d: %w", m.activeVersion.AppVersion, err)
		}
		m.started = false
		m.activeVersion = Version{}
	}
	return nil
}

// Cleanup allows proper multiplexer termination.
func (m *Multiplexer) Cleanup() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.logger.Info("cleaning up multiplexer")

	var errs error

	// stop any running app
	if err := m.StopActiveVersion(); err != nil {
		errs = errors.Join(errs, fmt.Errorf("failed to stop active version: %w", err))
	}

	// close gRPC connection
	if m.conn != nil {
		if err := m.conn.Close(); err != nil {
			errs = errors.Join(errs, fmt.Errorf("failed to close gRPC connection: %w", err))
		}
		m.conn = nil
	}

	for _, fn := range m.cleanupFns {
		if err := fn(); err != nil {
			errs = errors.Join(errs, fmt.Errorf("failed to run cleanup function: %w", err))
		}
	}

	return errs
}

// startCmtNode initializes and starts a CometBFT node, sets up cleanup tasks, and assigns it to the Multiplexer instance.
func (m *Multiplexer) startCmtNode(ctx context.Context) error {
	cfg := m.svrCtx.Config
	nodeKey, err := p2p.LoadOrGenNodeKey(cfg.NodeKeyFile())
	if err != nil {
		return err
	}

	// no latest app set means an embedded app is being used.
	if m.latestApp == nil {
		m.logger.Debug("registering remote app cleanup")
		m.setupRemoteAppCleanup(m.Cleanup)
	}

	tmNode, err := node.NewNodeWithContext(
		ctx,
		cfg,
		pvm.LoadOrGenFilePV(cfg.PrivValidatorKeyFile(), cfg.PrivValidatorStateFile()),
		nodeKey,
		proxy.NewConnSyncLocalClientCreator(m),
		internal.GetGenDocProvider(cfg),
		cmtcfg.DefaultDBProvider,
		node.DefaultMetricsProvider(cfg.Instrumentation),
		servercmtlog.CometLoggerWrapper{Logger: m.logger},
	)

	if err != nil {
		return err
	}

	if err := tmNode.Start(); err != nil {
		return err
	}

	m.registerCleanupFn(func() error {
		if tmNode != nil && tmNode.IsRunning() {
			return tmNode.Stop()
		}
		return nil
	})

	m.tmNode = tmNode
	return nil
}

// setupRemoteAppCleanup ensures that remote app processes are terminated when the main process receives termination signals
func (m *Multiplexer) setupRemoteAppCleanup(cleanupFn func() error) {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	go func() {
		sig := <-sigCh
		m.logger.Info("Received signal, stopping remote apps...", "signal", sig)

		if err := cleanupFn(); err != nil {
			m.logger.Error("Error stopping remote apps", "err", err)
		} else {
			m.logger.Info("Successfully stopped remote apps")
		}

		// Re-send the signal to allow the normal process termination
		signal.Reset(os.Interrupt, syscall.SIGTERM)
		syscall.Kill(os.Getpid(), sig.(syscall.Signal))
	}()
}

func startTelemetry(cfg serverconfig.Config) (*telemetry.Metrics, error) {
	return telemetry.New(cfg.Telemetry)
}

// emitServerInfoMetrics emits server info related metrics using application telemetry.
func emitServerInfoMetrics() {
	var ls []metrics.Label

	versionInfo := version.NewInfo()
	if len(versionInfo.GoVersion) > 0 {
		ls = append(ls, telemetry.NewLabel("go", versionInfo.GoVersion))
	}
	if len(versionInfo.CosmosSdkVersion) > 0 {
		ls = append(ls, telemetry.NewLabel("version", versionInfo.CosmosSdkVersion))
	}

	if len(ls) == 0 {
		return
	}

	telemetry.SetGaugeWithLabels([]string{"server", "info"}, 1, ls)
}

func getCtx(svrCtx *server.Context, block bool) (*errgroup.Group, context.Context) {
	ctx, cancelFn := context.WithCancel(context.Background())
	g, ctx := errgroup.WithContext(ctx)
	// listen for quit signals so the calling parent process can gracefully exit
	server.ListenForQuitSignals(g, block, cancelFn, svrCtx.Logger)
	return g, ctx
}
