package nova

import (
	"context"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"cosmossdk.io/log"
	"github.com/01builders/nova/abci"
	cmtabci "github.com/cometbft/cometbft/abci/types"
	cmtcfg "github.com/cometbft/cometbft/config"
	"github.com/cometbft/cometbft/node"
	"github.com/cometbft/cometbft/p2p"
	pvm "github.com/cometbft/cometbft/privval"
	"github.com/cometbft/cometbft/proxy"
	"github.com/cometbft/cometbft/rpc/client/local"
	cmttypes "github.com/cometbft/cometbft/types"
	dbm "github.com/cosmos/cosmos-db"
	"github.com/hashicorp/go-metrics"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/codec"
	"github.com/cosmos/cosmos-sdk/server"
	"github.com/cosmos/cosmos-sdk/server/api"
	serverconfig "github.com/cosmos/cosmos-sdk/server/config"
	servergrpc "github.com/cosmos/cosmos-sdk/server/grpc"
	servercmtlog "github.com/cosmos/cosmos-sdk/server/log"
	"github.com/cosmos/cosmos-sdk/server/types"
	"github.com/cosmos/cosmos-sdk/telemetry"
	"github.com/cosmos/cosmos-sdk/version"
	genutiltypes "github.com/cosmos/cosmos-sdk/x/genutil/types"
)

const (
	flagAddress    = "address"
	flagTransport  = "transport"
	flagTraceStore = "trace-store"
	flagGRPCOnly   = "grpc-only"
)

// StartCommandHandler is the type that must implement nova to match Cosmos SDK start logic.
type StartCommandHandler = func(svrCtx *server.Context, clientCtx client.Context, appCreator types.AppCreator, withCmt bool, opts server.StartCmdOptions) error

// New creates a command start handler to use in the Cosmos SDK server start options.
func New(versions abci.Versions) StartCommandHandler {
	return func(
		svrCtx *server.Context,
		clientCtx client.Context,
		appCreator types.AppCreator,
		_ bool,
		_ server.StartCmdOptions,
	) error {
		return start(versions, svrCtx, clientCtx, appCreator)
	}
}

func start(versions abci.Versions, svrCtx *server.Context, clientCtx client.Context, appCreator types.AppCreator) error {
	svrCfg, err := getAndValidateConfig(svrCtx)
	if err != nil {
		return err
	}

	app, appCleanupFn, err := startApp(svrCtx, appCreator)
	if err != nil {
		return err
	}
	defer appCleanupFn()

	metrics, err := startTelemetry(svrCfg)
	if err != nil {
		return err
	}

	emitServerInfoMetrics()

	return startInProcess(versions, svrCtx, svrCfg, clientCtx, app, metrics)
}

func startInProcess(versions abci.Versions, svrCtx *server.Context, svrCfg serverconfig.Config, clientCtx client.Context, app types.Application,
	metrics *telemetry.Metrics,
) error {
	cmtCfg := svrCtx.Config
	gRPCOnly := svrCtx.Viper.GetBool(flagGRPCOnly)

	g, ctx := getCtx(svrCtx, true)

	if gRPCOnly {
		// TODO: Generalize logic so that gRPC only is really in startStandAlone
		svrCtx.Logger.Info("starting node in gRPC only mode; CometBFT is disabled")
		svrCfg.GRPC.Enable = true
	} else {
		svrCtx.Logger.Info("starting node with ABCI CometBFT in-process")
		tmNode, cleanupFn, err := startCmtNode(versions, ctx, cmtCfg, app, svrCtx)
		if err != nil {
			return err
		}
		defer cleanupFn()

		// Add the tx service to the gRPC router. We only need to register this
		// service if API or gRPC is enabled, and avoid doing so in the general
		// case, because it spawns a new local CometBFT RPC client.
		if svrCfg.API.Enable || svrCfg.GRPC.Enable {
			// Re-assign for making the client available below do not use := to avoid
			// shadowing the clientCtx variable.
			clientCtx = clientCtx.WithClient(local.New(tmNode))

			app.RegisterTxService(clientCtx)
			app.RegisterTendermintService(clientCtx)
			app.RegisterNodeService(clientCtx, svrCfg)
		}
	}

	grpcSrv, clientCtx, err := startGrpcServer(ctx, g, svrCfg.GRPC, clientCtx, svrCtx, app)
	if err != nil {
		return err
	}

	err = startAPIServer(ctx, g, svrCfg, clientCtx, svrCtx, app, cmtCfg.RootDir, grpcSrv, metrics)
	if err != nil {
		return err
	}

	// wait for signal capture and gracefully return
	// we are guaranteed to be waiting for the "ListenForQuitSignals" goroutine.
	return g.Wait()
}

func startCmtNode(
	versions abci.Versions,
	ctx context.Context,
	cfg *cmtcfg.Config,
	app types.Application,
	svrCtx *server.Context,
) (tmNode *node.Node, cleanupFn func(), err error) {
	nodeKey, err := p2p.LoadOrGenNodeKey(cfg.NodeKeyFile())
	if err != nil {
		return nil, cleanupFn, err
	}

	cmtApp, err := abci.NewMultiplexer(svrCtx.Logger.With("multiplexer"), app, versions, svrCtx.Viper, svrCtx.Config.RootDir)
	if err != nil {
		return nil, cleanupFn, err
	}

	// Register cleanup handler for remote apps
	setupRemoteAppCleanup(cmtApp, svrCtx)

	tmNode, err = node.NewNodeWithContext(
		ctx,
		cfg,
		pvm.LoadOrGenFilePV(cfg.PrivValidatorKeyFile(), cfg.PrivValidatorStateFile()),
		nodeKey,
		// we use local client creator because the latest app shouldn't use grpc
		proxy.NewLocalClientCreator(cmtApp),
		getGenDocProvider(cfg),
		cmtcfg.DefaultDBProvider,
		node.DefaultMetricsProvider(cfg.Instrumentation),
		servercmtlog.CometLoggerWrapper{Logger: svrCtx.Logger},
	)
	if err != nil {
		return tmNode, cleanupFn, err
	}

	if err := tmNode.Start(); err != nil {
		return tmNode, cleanupFn, err
	}

	cleanupFn = func() {
		if tmNode != nil && tmNode.IsRunning() {
			_ = tmNode.Stop()
		}

		// also ensure we stop any remote apps
		if multiplexer, ok := cmtApp.(*abci.Multiplexer); ok {
			_ = multiplexer.Cleanup()
		}
	}

	return tmNode, cleanupFn, nil
}

// setupRemoteAppCleanup ensures that remote app processes are terminated when the main process receives termination signals
func setupRemoteAppCleanup(cmtApp cmtabci.Application, svrCtx *server.Context) {
	if multiplexer, ok := cmtApp.(*abci.Multiplexer); ok {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

		go func() {
			sig := <-sigCh
			svrCtx.Logger.Info("Received signal, stopping remote apps...", "signal", sig)

			if err := multiplexer.Cleanup(); err != nil {
				svrCtx.Logger.Error("Error stopping remote apps", "err", err)
			} else {
				svrCtx.Logger.Info("Successfully stopped remote apps")
			}

			// Re-send the signal to allow the normal process termination
			signal.Reset(os.Interrupt, syscall.SIGTERM)
			syscall.Kill(os.Getpid(), sig.(syscall.Signal))
		}()
	}
}

func getAndValidateConfig(svrCtx *server.Context) (serverconfig.Config, error) {
	config, err := serverconfig.GetConfig(svrCtx.Viper)
	if err != nil {
		return config, err
	}

	if err := config.ValidateBasic(); err != nil {
		return config, err
	}
	return config, nil
}

// returns a function which returns the genesis doc from the genesis file.
func getGenDocProvider(cfg *cmtcfg.Config) func() (*cmttypes.GenesisDoc, error) {
	return func() (*cmttypes.GenesisDoc, error) {
		appGenesis, err := genutiltypes.AppGenesisFromFile(cfg.GenesisFile())
		if err != nil {
			return nil, err
		}

		return appGenesis.ToGenesisDoc()
	}
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

func startGrpcServer(
	ctx context.Context,
	g *errgroup.Group,
	config serverconfig.GRPCConfig,
	clientCtx client.Context,
	svrCtx *server.Context,
	app types.Application,
) (*grpc.Server, client.Context, error) {
	if !config.Enable {
		// return grpcServer as nil if gRPC is disabled
		return nil, clientCtx, nil
	}
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
	grpcClient, err := grpc.Dial( //nolint: staticcheck // ignore this line for this linter
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
	svrCtx.Logger.Debug("gRPC client assigned to client context", "target", config.Address)

	grpcSrv, err := servergrpc.NewGRPCServer(clientCtx, app, config)
	if err != nil {
		return nil, clientCtx, err
	}

	// Start the gRPC server in a goroutine. Note, the provided ctx will ensure
	// that the server is gracefully shut down.
	g.Go(func() error {
		return servergrpc.StartGRPCServer(ctx, svrCtx.Logger.With(log.ModuleKey, "grpc-server"), config, grpcSrv)
	})
	return grpcSrv, clientCtx, nil
}

func startAPIServer(
	ctx context.Context,
	g *errgroup.Group,
	svrCfg serverconfig.Config,
	clientCtx client.Context,
	svrCtx *server.Context,
	app types.Application,
	home string,
	grpcSrv *grpc.Server,
	metrics *telemetry.Metrics,
) error {
	if !svrCfg.API.Enable {
		return nil
	}

	clientCtx = clientCtx.WithHomeDir(home)

	apiSrv := api.New(clientCtx, svrCtx.Logger.With(log.ModuleKey, "api-server"), grpcSrv)
	app.RegisterAPIRoutes(apiSrv, svrCfg.API)

	if svrCfg.Telemetry.Enabled {
		apiSrv.SetTelemetry(metrics)
	}

	g.Go(func() error {
		return apiSrv.Start(ctx, svrCfg)
	})
	return nil
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

func startApp(svrCtx *server.Context, appCreator types.AppCreator) (app types.Application, cleanupFn func(), err error) {
	traceWriter, traceCleanupFn, err := setupTraceWriter(svrCtx)
	if err != nil {
		return app, traceCleanupFn, err
	}

	home := svrCtx.Config.RootDir
	db, err := openDB(home, server.GetAppDBBackend(svrCtx.Viper))
	if err != nil {
		return app, traceCleanupFn, err
	}

	app = appCreator(svrCtx.Logger, db, traceWriter, svrCtx.Viper)
	cleanupFn = func() {
		traceCleanupFn()
		if localErr := app.Close(); localErr != nil {
			svrCtx.Logger.Error(localErr.Error())
		}
	}
	return app, cleanupFn, nil
}

func openDB(rootDir string, backendType dbm.BackendType) (dbm.DB, error) {
	dataDir := filepath.Join(rootDir, "data")
	return dbm.NewDB("application", backendType, dataDir)
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
