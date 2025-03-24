package nova

import (
	"fmt"
	"github.com/01builders/nova/abci"
	dbm "github.com/cometbft/cometbft-db"
	cmtcfg "github.com/cometbft/cometbft/config"
	"github.com/cometbft/cometbft/node"
	"github.com/cometbft/cometbft/state"
	cmttypes "github.com/cometbft/cometbft/types"
	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/server"
	serverconfig "github.com/cosmos/cosmos-sdk/server/config"
	"github.com/cosmos/cosmos-sdk/server/types"
	genutiltypes "github.com/cosmos/cosmos-sdk/x/genutil/types"
)

const (
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
		withCmt bool,
		_ server.StartCmdOptions,
	) error {
		if !withCmt {
			svrCtx.Logger.Info("App cannot be started without CometBFT when using the multiplexer.")
			return nil
		}

		return start(versions, svrCtx, clientCtx, appCreator)
	}
}

func start(versions abci.Versions, svrCtx *server.Context, clientCtx client.Context, appCreator types.AppCreator) error {
	svrCfg, err := getAndValidateConfig(svrCtx)
	if err != nil {
		return err
	}

	state, err := getState(svrCtx.Config)
	if err != nil {
		return fmt.Errorf("failed to get current app state: %w", err)
	}

	appVersion := state.Version.Consensus.App

	// Check if we should use latest app or not
	usesLatestApp := versions.ShouldUseLatestApp(appVersion)
	svrCtx.Logger.Info("determining app version to use",
		"app_version", appVersion,
		"chain_id", state.ChainID,
		"uses_latest_app", usesLatestApp)

	mp, err := abci.NewMultiplexer(svrCtx, svrCfg, clientCtx, appCreator, versions, state.ChainID, appVersion)
	if err != nil {
		return err
	}

	defer func() {
		if err := mp.Cleanup(); err != nil {
			svrCtx.Logger.Error("failed to cleanup multiplexer", "err", err)
		}
	}()

	// Start will either start the latest app natively, or an embedded app if one is specified.
	if err := mp.Start(); err != nil {
		return fmt.Errorf("failed to start app: %w", err)
	}

	return nil
}

// getState opens the db and fetches the existing state.
func getState(cfg *cmtcfg.Config) (state.State, error) {
	db, err := openDBM(cfg)
	if err != nil {
		return state.State{}, err
	}
	defer db.Close()

	s, _, err := node.LoadStateFromDBOrGenesisDocProvider(db, getGenDocProvider(cfg))
	if err != nil {
		return state.State{}, err
	}

	return s, nil
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

func openDBM(cfg *cmtcfg.Config) (dbm.DB, error) {
	return dbm.NewDB("state", dbm.BackendType(cfg.DBBackend), cfg.DBDir())
}
