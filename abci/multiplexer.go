package abci

import (
	"context"
	"errors"
	"fmt"
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

	lastHeight     int64
	lastAppVersion uint64
	started        bool

	latestApp     servertypes.ABCI
	activeVersion Version

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
	currentHeight int64,
) (proxy.ClientCreator, func() error, error) {
	var noOpCleanUp = func() error {
		return nil
	}

	if err := versions.Validate(); err != nil {
		return nil, noOpCleanUp, fmt.Errorf("invalid versions: %w", err)
	}

	wrapper := &Multiplexer{
		logger:     logger,
		latestApp:  latestApp,
		versions:   versions.Sorted(),
		lastHeight: currentHeight,
	}

	// prepare correct version
	currentVersion, err := versions.GetForAppVersion(wrapper.lastAppVersion)
	if err != nil && errors.Is(err, ErrNoVersionFound) {
		return proxy.NewLocalClientCreator(wrapper), noOpCleanUp, nil // no version found, assume latest
	} else if err != nil {
		return nil, noOpCleanUp, fmt.Errorf("failed to get app for version %d: %w", wrapper.lastAppVersion, err)
	}

	// start the correct version
	if currentVersion.Appd == nil {
		return nil, noOpCleanUp, fmt.Errorf("appd is nil for height %d", currentHeight)
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

	conn, err := grpc.Dial( //nolint:staticcheck: we want to check the connection.
		tmAddress,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
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
		// there is no version specified for the given height or application version
		return m.latestApp, nil
	}

	// use the latest app if the height is beyond all defined versions
	if m.versions.ShouldUseLatestApp(m.lastAppVersion) {
		// TODO: start the latest app here if not already started
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
			m.logger.Info("Stopping app for version", "app_version", m.activeVersion.AppVersion)
			if err := m.activeVersion.Appd.Stop(); err != nil {
				return nil, fmt.Errorf("failed to stop app for version %d: %w", m.activeVersion.AppVersion, err)
			}
			m.started = false
			m.activeVersion = Version{}
		}

		if currentVersion.Appd.Pid() == appd.AppdStopped {
			if currentVersion.PreHandler != "" {
				preCmd := currentVersion.Appd.CreateExecCommand(currentVersion.PreHandler)
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

	switch currentVersion.ABCIVersion {
	case ABCIClientVersion1:
		return NewRemoteABCIClientV1(m.conn), nil
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
		m.logger.Info("Stopping app for version", "app_version", m.activeVersion.AppVersion)
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

	// update the last height and app version
	m.lastHeight = req.Height
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
		return nil, fmt.Errorf("failed to get app for version %d: %w", m.lastAppVersion, err)
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
	m.lastHeight = req.Height
	app, err := m.getApp()
	if err != nil {
		return nil, fmt.Errorf("failed to get app for version %d: %w", m.lastAppVersion, err)
	}
	return app.PrepareProposal(req)
}

func (m *Multiplexer) ProcessProposal(_ context.Context, req *abci.RequestProcessProposal) (*abci.ResponseProcessProposal, error) {
	m.lastHeight = req.Height
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
	m.lastHeight = req.Height
	app, err := m.getApp()
	if err != nil {
		return nil, fmt.Errorf("failed to get app for version %d: %w", m.lastAppVersion, err)
	}
	return app.VerifyVoteExtension(req)
}
