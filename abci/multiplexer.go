package abci

import (
	"context"
	"errors"
	"fmt"
	db "github.com/cosmos/cosmos-db"
	"github.com/cosmos/cosmos-sdk/server"
	"google.golang.org/grpc/credentials/insecure"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"cosmossdk.io/log"
	abci "github.com/cometbft/cometbft/abci/types"
	servertypes "github.com/cosmos/cosmos-sdk/server/types"
	"google.golang.org/grpc"

	"github.com/01builders/nova/appd"
)

const (
	flagTraceStore = "trace-store"
)

type Multiplexer struct {
	logger log.Logger
	mu     sync.Mutex

	svrCtx *server.Context

	lastAppVersion uint64
	started        bool

	appCreator    servertypes.AppCreator
	latestApp     servertypes.ABCI
	activeVersion Version
	chainID       string

	versions   Versions
	conn       *grpc.ClientConn
	cleanupFns []func() error
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
func NewMultiplexer(
	svrCtx *server.Context,
	appCreator servertypes.AppCreator,
	versions Versions,
	chainID string,
	applicationVersion uint64,
) (*Multiplexer, error) {
	if err := versions.Validate(); err != nil {
		return nil, fmt.Errorf("invalid versions: %w", err)
	}

	mp := &Multiplexer{
		svrCtx:         svrCtx,
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

// registerCleanupFn enables the registration of additional cleanup functions that get called during Cleanup
func (m *Multiplexer) registerCleanupFn(cleanUpFn func() error) {
	m.cleanupFns = append(m.cleanupFns, cleanUpFn)
}

// StartApp starts either the native app, or an embedded app.
func (m *Multiplexer) StartApp() error {
	// prepare correct version
	currentVersion, err := m.versions.GetForAppVersion(m.lastAppVersion)
	if err != nil && errors.Is(err, ErrNoVersionFound) {
		// no version found, assume latest
		if err := m.startNativeApp(); err != nil {
			return fmt.Errorf("failed to start native app: %w", err)
		}
		return nil
	} else if err != nil {
		return fmt.Errorf("failed to get app for version %d: %w", m.lastAppVersion, err)
	}

	// we are starting an embedded app instead of a native app.

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
	m.conn = conn
	return nil
}

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
		if m.latestApp == nil {
			m.logger.Info("Starting latest app")
			if err := m.StartApp(); err != nil {
				return nil, fmt.Errorf("failed to start latest app: %w", err)
			}
		}
		return m.latestApp, nil
	}

	// check if we need to start the app or if we have a different app running
	if !m.started || currentVersion.AppVersion != m.activeVersion.AppVersion {
		if currentVersion.Appd == nil {
			return nil, fmt.Errorf("appd is nil for version %d", m.activeVersion.AppVersion)
		}

		// stop the existing app version if one is currently running.
		if err := m.StopActiveVersion(); err != nil {
			return nil, fmt.Errorf("failed to stop active version: %w", err)
		}

		if currentVersion.Appd.Pid() == appd.AppdStopped {
			for _, preHandler := range currentVersion.PreHandlers {
				preCmd := currentVersion.Appd.CreateExecCommand(preHandler)
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

			m.logger.Info("Starting app for version", "app_version", currentVersion.AppVersion, "args", programArgs)
			if err := currentVersion.Appd.Start(currentVersion.GetStartArgs(programArgs)...); err != nil {
				return nil, fmt.Errorf("failed to start app for version %d: %w", m.lastAppVersion, err)
			}

			if currentVersion.Appd.Pid() == appd.AppdStopped {
				return nil, fmt.Errorf("app for version %d failed to start", m.latestApp)
			}

			m.activeVersion = currentVersion
			m.started = true
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

	var errs error

	// stop any running app
	// TODO: kill native app also?
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

func (m *Multiplexer) ApplySnapshotChunk(_ context.Context, req *abci.RequestApplySnapshotChunk) (*abci.ResponseApplySnapshotChunk, error) {
	app, err := m.getApp()
	if err != nil {
		return nil, fmt.Errorf("failed to get app for version %d: %w", m.lastAppVersion, err)
	}
	return app.ApplySnapshotChunk(req)
}

func (m *Multiplexer) CheckTx(_ context.Context, req *abci.RequestCheckTx) (*abci.ResponseCheckTx, error) {
	app, err := m.getApp()
	if err != nil {
		return nil, fmt.Errorf("failed to get app for version %d: %w", m.lastAppVersion, err)
	}
	return app.CheckTx(req)
}

func (m *Multiplexer) Commit(context.Context, *abci.RequestCommit) (*abci.ResponseCommit, error) {
	app, err := m.getApp()
	if err != nil {
		return nil, fmt.Errorf("failed to get app for version %d: %w", m.lastAppVersion, err)
	}
	return app.Commit()
}

func (m *Multiplexer) ExtendVote(ctx context.Context, req *abci.RequestExtendVote) (*abci.ResponseExtendVote, error) {
	app, err := m.getApp()
	if err != nil {
		return nil, fmt.Errorf("failed to get app for version %d: %w", m.lastAppVersion, err)
	}
	return app.ExtendVote(ctx, req)
}

func (m *Multiplexer) FinalizeBlock(_ context.Context, req *abci.RequestFinalizeBlock) (*abci.ResponseFinalizeBlock, error) {
	app, err := m.getApp()
	if err != nil {
		return nil, fmt.Errorf("failed to get app for version %d: %w", m.lastAppVersion, err)
	}

	resp, err := app.FinalizeBlock(req)
	if err != nil {
		return nil, fmt.Errorf("failed to finalize block: %w", err)
	}

	// update the app version
	m.lastAppVersion = resp.ConsensusParamUpdates.GetVersion().App

	return resp, err
}

func (m *Multiplexer) Info(_ context.Context, req *abci.RequestInfo) (*abci.ResponseInfo, error) {
	app, err := m.getApp()
	if err != nil {
		return nil, fmt.Errorf("failed to get app for version %d: %w", m.lastAppVersion, err)
	}

	return app.Info(req)
}

func (m *Multiplexer) InitChain(_ context.Context, req *abci.RequestInitChain) (*abci.ResponseInitChain, error) {
	app, err := m.getApp()
	if err != nil {
		return nil, fmt.Errorf("failed to get app for genesis: %w", err)
	}
	return app.InitChain(req)
}

func (m *Multiplexer) ListSnapshots(_ context.Context, req *abci.RequestListSnapshots) (*abci.ResponseListSnapshots, error) {
	app, err := m.getApp()
	if err != nil {
		return nil, fmt.Errorf("failed to get app for version %d: %w", m.lastAppVersion, err)
	}
	return app.ListSnapshots(req)
}

func (m *Multiplexer) LoadSnapshotChunk(_ context.Context, req *abci.RequestLoadSnapshotChunk) (*abci.ResponseLoadSnapshotChunk, error) {
	app, err := m.getApp()
	if err != nil {
		return nil, fmt.Errorf("failed to get app for version %d: %w", m.lastAppVersion, err)
	}
	return app.LoadSnapshotChunk(req)
}

func (m *Multiplexer) OfferSnapshot(_ context.Context, req *abci.RequestOfferSnapshot) (*abci.ResponseOfferSnapshot, error) {
	app, err := m.getApp()
	if err != nil {
		return nil, fmt.Errorf("failed to get app for version %d: %w", m.lastAppVersion, err)
	}
	return app.OfferSnapshot(req)
}

func (m *Multiplexer) PrepareProposal(_ context.Context, req *abci.RequestPrepareProposal) (*abci.ResponsePrepareProposal, error) {
	app, err := m.getApp()
	if err != nil {
		return nil, fmt.Errorf("failed to get app for version %d: %w", m.lastAppVersion, err)
	}
	return app.PrepareProposal(req)
}

func (m *Multiplexer) ProcessProposal(_ context.Context, req *abci.RequestProcessProposal) (*abci.ResponseProcessProposal, error) {
	app, err := m.getApp()
	if err != nil {
		return nil, fmt.Errorf("failed to get app for version %d: %w", m.lastAppVersion, err)
	}
	return app.ProcessProposal(req)
}

func (m *Multiplexer) Query(ctx context.Context, req *abci.RequestQuery) (*abci.ResponseQuery, error) {
	app, err := m.getApp()
	if err != nil {
		return nil, fmt.Errorf("failed to get app for version %d: %w", m.lastAppVersion, err)
	}
	return app.Query(ctx, req)
}

func (m *Multiplexer) VerifyVoteExtension(_ context.Context, req *abci.RequestVerifyVoteExtension) (*abci.ResponseVerifyVoteExtension, error) {
	app, err := m.getApp()
	if err != nil {
		return nil, fmt.Errorf("failed to get app for version %d: %w", m.lastAppVersion, err)
	}
	return app.VerifyVoteExtension(req)
}
