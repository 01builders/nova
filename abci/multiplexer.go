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

	lastHeight int64
	started    bool

	latestApp     servertypes.ABCI
	activeVersion Version

	versions Versions
	conn     *grpc.ClientConn
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

	var (
		currentVersion Version
		err            error
	)

	// prepare correct version
	if currentHeight == 0 {
		currentVersion, err = versions.GenesisVersion()
	} else {
		currentVersion, err = versions.GetForHeight(currentHeight)
	}
	if err != nil && errors.Is(err, ErrNoVersionFound) {
		return proxy.NewLocalClientCreator(wrapper), noOpCleanUp, nil // no version found, assume latest
	} else if err != nil {
		return nil, noOpCleanUp, fmt.Errorf("failed to get app for height %d: %w", currentHeight, err)
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

// getAppForHeight gets the appropriate app based on height
func (m *Multiplexer) getAppForHeight(height int64) (servertypes.ABCI, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// use the latest app if the height is beyond all defined versions
	if m.versions.ShouldLatestApp(height) {
		// TODO: start the latest app here if not already started
		return m.latestApp, nil
	}

	// get the appropriate version for this height
	currentVersion, err := m.versions.GetForHeight(height)
	if err != nil {
		return nil, fmt.Errorf("failed to get app for height %d: %w", height, err)
	}

	// check if we need to start the app or if we have a different app running
	if !m.started || currentVersion.UntilHeight != m.activeVersion.UntilHeight {
		if currentVersion.Appd == nil {
			return nil, fmt.Errorf("appd is nil for height %d", height)
		}

		// check if an app is already started
		// stop the app if it's running
		if m.activeVersion.Appd != nil && m.activeVersion.Appd.Pid() != appd.AppdStopped {
			m.logger.Info("Stopping app for height", "height", m.activeVersion.UntilHeight)
			if err := m.activeVersion.Appd.Stop(); err != nil {
				return nil, fmt.Errorf("failed to stop app for height %d: %w", m.activeVersion.UntilHeight, err)
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
				return nil, fmt.Errorf("failed to start app for height %d: %w", height, err)
			}

			if currentVersion.Appd.Pid() == appd.AppdStopped {
				return nil, fmt.Errorf("app for height %d failed to start", height)
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
		m.logger.Info("Stopping app for height", "height", m.activeVersion.UntilHeight)
		if err := m.activeVersion.Appd.Stop(); err != nil {
			errs = errors.Join(errs, fmt.Errorf("failed to stop app for height %d: %w", m.activeVersion.UntilHeight, err))
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
	app, err := m.getAppForHeight(m.lastHeight)
	if err != nil {
		return nil, fmt.Errorf("failed to get app for height %d: %w", m.lastHeight, err)
	}
	return app.ApplySnapshotChunk(req)
}

func (m *Multiplexer) CheckTx(_ context.Context, req *abci.RequestCheckTx) (*abci.ResponseCheckTx, error) {
	app, err := m.getAppForHeight(m.lastHeight)
	if err != nil {
		return nil, fmt.Errorf("failed to get app for height %d: %w", m.lastHeight, err)
	}
	return app.CheckTx(req)
}

func (m *Multiplexer) Commit(context.Context, *abci.RequestCommit) (*abci.ResponseCommit, error) {
	app, err := m.getAppForHeight(m.lastHeight)
	if err != nil {
		return nil, fmt.Errorf("failed to get app for height %d: %w", m.lastHeight, err)
	}
	return app.Commit()
}

func (m *Multiplexer) ExtendVote(ctx context.Context, req *abci.RequestExtendVote) (*abci.ResponseExtendVote, error) {
	m.lastHeight = req.Height
	app, err := m.getAppForHeight(req.Height)
	if err != nil {
		return nil, fmt.Errorf("failed to get app for height %d: %w", req.Height, err)
	}
	return app.ExtendVote(ctx, req)
}

func (m *Multiplexer) FinalizeBlock(_ context.Context, req *abci.RequestFinalizeBlock) (*abci.ResponseFinalizeBlock, error) {
	m.lastHeight = req.Height

	app, err := m.getAppForHeight(req.Height)
	if err != nil {
		return nil, fmt.Errorf("failed to get app for height %d: %w", req.Height, err)
	}

	return app.FinalizeBlock(req)
}

func (m *Multiplexer) Info(_ context.Context, req *abci.RequestInfo) (*abci.ResponseInfo, error) {
	app, err := m.getAppForHeight(m.lastHeight)
	if err != nil {
		return nil, fmt.Errorf("failed to get app for height %d: %w", m.lastHeight, err)
	}

	return app.Info(req)
}

func (m *Multiplexer) InitChain(_ context.Context, req *abci.RequestInitChain) (*abci.ResponseInitChain, error) {
	app, err := m.getAppForHeight(0)
	if err != nil {
		return nil, fmt.Errorf("failed to get app for genesis: %w", err)
	}
	return app.InitChain(req)
}

func (m *Multiplexer) ListSnapshots(_ context.Context, req *abci.RequestListSnapshots) (*abci.ResponseListSnapshots, error) {
	app, err := m.getAppForHeight(m.lastHeight)
	if err != nil {
		return nil, fmt.Errorf("failed to get app for height %d: %w", m.lastHeight, err)
	}
	return app.ListSnapshots(req)
}

func (m *Multiplexer) LoadSnapshotChunk(_ context.Context, req *abci.RequestLoadSnapshotChunk) (*abci.ResponseLoadSnapshotChunk, error) {
	app, err := m.getAppForHeight(int64(req.Height))
	if err != nil {
		return nil, fmt.Errorf("failed to get app for height %d: %w", req.Height, err)
	}
	return app.LoadSnapshotChunk(req)
}

func (m *Multiplexer) OfferSnapshot(_ context.Context, req *abci.RequestOfferSnapshot) (*abci.ResponseOfferSnapshot, error) {
	app, err := m.getAppForHeight(m.lastHeight)
	if err != nil {
		return nil, fmt.Errorf("failed to get app for height %d: %w", m.lastHeight, err)
	}
	return app.OfferSnapshot(req)
}

func (m *Multiplexer) PrepareProposal(_ context.Context, req *abci.RequestPrepareProposal) (*abci.ResponsePrepareProposal, error) {
	m.lastHeight = req.Height
	app, err := m.getAppForHeight(req.Height)
	if err != nil {
		return nil, fmt.Errorf("failed to get app for height %d: %w", req.Height, err)
	}
	return app.PrepareProposal(req)
}

func (m *Multiplexer) ProcessProposal(_ context.Context, req *abci.RequestProcessProposal) (*abci.ResponseProcessProposal, error) {
	m.lastHeight = req.Height
	app, err := m.getAppForHeight(req.Height)
	if err != nil {
		return nil, fmt.Errorf("failed to get app for height %d: %w", req.Height, err)
	}
	return app.ProcessProposal(req)
}

func (m *Multiplexer) Query(ctx context.Context, req *abci.RequestQuery) (*abci.ResponseQuery, error) {
	app, err := m.getAppForHeight(req.Height)
	if err != nil {
		return nil, fmt.Errorf("failed to get app for height %d: %w", req.Height, err)
	}
	return app.Query(ctx, req)
}

func (m *Multiplexer) VerifyVoteExtension(_ context.Context, req *abci.RequestVerifyVoteExtension) (*abci.ResponseVerifyVoteExtension, error) {
	m.lastHeight = req.Height
	app, err := m.getAppForHeight(req.Height)
	if err != nil {
		return nil, fmt.Errorf("failed to get app for height %d: %w", req.Height, err)
	}
	return app.VerifyVoteExtension(req)
}
