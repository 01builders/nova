package abci

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"
	"sync"

	"github.com/spf13/viper"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"cosmossdk.io/log"
	abci "github.com/cometbft/cometbft/abci/types"
	"github.com/cometbft/cometbft/proxy"
	servertypes "github.com/cosmos/cosmos-sdk/server/types"

	"github.com/01builders/nova/appd"
)

type Multiplexer struct {
	logger log.Logger
	mu     sync.Mutex

	lastAppVersion uint64
	started        bool

	latestApp     servertypes.ABCI
	activeVersion Version
	chainID       string

	versions Versions
	conn     *grpc.ClientConn
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
	logger log.Logger,
	v *viper.Viper,
	latestApp servertypes.ABCI,
	versions Versions,
	chainID string,
	applicationVersion uint64,
) (proxy.ClientCreator, func() error, error) {
	var noOpCleanUp = func() error {
		return nil
	}

	if err := versions.Validate(); err != nil {
		return nil, noOpCleanUp, fmt.Errorf("invalid versions: %w", err)
	}

	wrapper := &Multiplexer{
		logger:         logger,
		latestApp:      latestApp,
		versions:       versions,
		chainID:        chainID,
		lastAppVersion: applicationVersion,
	}

	// prepare correct version
	currentVersion, err := versions.GetForAppVersion(wrapper.lastAppVersion)
	if err != nil && errors.Is(err, ErrNoVersionFound) {
		// no version found, assume latest
		return proxy.NewConnSyncLocalClientCreator(wrapper), noOpCleanUp, nil
	} else if err != nil {
		return nil, noOpCleanUp, fmt.Errorf("failed to get app for version %d: %w", wrapper.lastAppVersion, err)
	}

	// start the correct version
	if currentVersion.Appd == nil {
		return nil, noOpCleanUp, fmt.Errorf("appd is nil for version %d", wrapper.lastAppVersion)
	}

	if currentVersion.Appd.Pid() == appd.AppdStopped {
		programArgs := os.Args
		if len(os.Args) > 2 {
			programArgs = os.Args[2:] // remove 'appd start' args
		}

		if err := currentVersion.Appd.Start(currentVersion.GetStartArgs(programArgs)...); err != nil {
			return nil, noOpCleanUp, fmt.Errorf("failed to start app: %w", err)
		}

		if currentVersion.Appd.Pid() == appd.AppdStopped { // should never happen
			return nil, noOpCleanUp, fmt.Errorf("app failed to start")
		}

		wrapper.started = true
		wrapper.activeVersion = currentVersion
	}

	// prepare remote app client
	const flagTMAddress = "address"
	tmAddress := v.GetString(flagTMAddress)
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
		return nil, noOpCleanUp, fmt.Errorf("failed to prepare app connection: %w", err)
	}
	wrapper.conn = conn

	return proxy.NewConnSyncLocalClientCreator(wrapper), wrapper.Cleanup, nil
}

// getApp gets the appropriate app based on the latest application version.
func (m *Multiplexer) getApp() (servertypes.ABCI, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// get the appropriate version for the latest app version.
	currentVersion, err := m.versions.GetForAppVersion(m.lastAppVersion)
	if err != nil {
		m.logger.Info("No app found in multiplexer for app version; using latest app", "app_version", m.lastAppVersion, "err", err)
		panic("TOOD: start latest app here")
		return m.latestApp, nil
	}

	// check if we need to start the app or if we have a different app running
	if !m.started || currentVersion.AppVersion != m.activeVersion.AppVersion {
		if currentVersion.Appd == nil {
			return nil, fmt.Errorf("appd is nil for version %d", m.activeVersion.AppVersion)
		}

		// check if an app is already started
		// stop the app if it's running
		if m.activeVersion.Appd != nil && m.activeVersion.Appd.Pid() != appd.AppdStopped {
			m.logger.Info("Stopping app for version", "maximum_app_version", m.activeVersion.AppVersion)
			if err := m.activeVersion.Appd.Stop(); err != nil {
				return nil, fmt.Errorf("failed to stop app for version %d: %w", m.activeVersion.AppVersion, err)
			}
			m.started = false
			m.activeVersion = Version{}
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

// Cleanup allows proper multiplexer termination.
func (m *Multiplexer) Cleanup() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	var errs error

	// stop any running app
	if m.activeVersion.Appd != nil && m.activeVersion.Appd.Pid() != appd.AppdStopped {
		m.logger.Info("Stopping app for version", "maximum_app_version", m.activeVersion.AppVersion)
		if err := m.activeVersion.Appd.Stop(); err != nil {
			errs = errors.Join(errs, fmt.Errorf("failed to stop app for version %d: %w", m.activeVersion.AppVersion, err))
		}
		m.started = false
		m.activeVersion = Version{}
	}

	// close gRPC connection
	if m.conn != nil {
		if err := m.conn.Close(); err != nil {
			errs = errors.Join(errs, fmt.Errorf("failed to close gRPC connection: %w", err))
		}
		m.conn = nil
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
