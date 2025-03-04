package abci

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"

	"github.com/spf13/viper"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"cosmossdk.io/store/rootmulti"
	abci "github.com/cometbft/cometbft/abci/types"
	dbm "github.com/cosmos/cosmos-db"
	"github.com/cosmos/cosmos-sdk/server"
	servertypes "github.com/cosmos/cosmos-sdk/server/types"

	"github.com/01builders/nova/appd"
)

const flagGRPCAddress = "grpc.address"

type multiplexer struct {
	mu sync.Mutex

	currentHeight, lastHeight int64
	started                   bool

	latestApp     servertypes.ABCI
	activeVersion Version

	versions Versions
	conn     *grpc.ClientConn
}

// NewMultiplexer creates a new ABCI wrapper for multiplexing
func NewMultiplexer(latestApp servertypes.ABCI, versions Versions, v *viper.Viper, home string) (abci.Application, error) {
	wrapper := &multiplexer{
		latestApp: latestApp,
		versions:  versions,
	}

	var (
		currentVersion Version
		err            error
	)

	// check height from disk
	wrapper.lastHeight, err = wrapper.getLatestHeight(home, v) // if error assume genesis
	if err != nil {
		log.Printf("failed to get latest height from disk, assuming genesis: %v\n", err)
	}

	// prepare correct version
	if wrapper.lastHeight == 0 {
		currentVersion, err = versions.GenesisVersion()
	} else {
		currentVersion, err = versions.GetForHeight(wrapper.lastHeight)
	}
	if err != nil && errors.Is(err, ErrNoVersionFound) {
		return wrapper, nil // no version found, assume latest
	} else if err != nil {
		return nil, fmt.Errorf("failed to get app for height %d: %w", wrapper.lastHeight, err)
	}

	// prepare remote app client
	grpcAddress := v.GetString(flagGRPCAddress)
	if grpcAddress == "" {
		grpcAddress = "localhost:9090"
	}

	conn, err := grpc.NewClient(grpcAddress, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("failed to prepare app connection: %w", err)
	}
	wrapper.conn = conn

	// start the correct version
	if currentVersion.Appd.Pid() == appd.AppdStopped {
		programArgs := os.Args
		if len(os.Args) > 2 {
			programArgs = os.Args[2:] // remove 'appd start' args
		}

		if err := currentVersion.Appd.Run(append(programArgs, currentVersion.StartArgs...)...); err != nil {
			return nil, fmt.Errorf("failed to start app: %w", err)
		}

		if currentVersion.Appd.Pid() == appd.AppdStopped { // should never happen
			return nil, fmt.Errorf("app failed to start")
		}

		wrapper.started = true
		wrapper.activeVersion = currentVersion
	}

	return wrapper, nil
}

func (m *multiplexer) getLatestHeight(rootDir string, v *viper.Viper) (int64, error) {
	dataDir := filepath.Join(rootDir, "data")
	db, err := dbm.NewDB("application", server.GetAppDBBackend(v), dataDir)
	if err != nil {
		return 0, err
	}

	height := rootmulti.GetLatestVersion(db)

	return height, db.Close()
}

// getAppForHeight gets the appropriate app based on height
func (m *multiplexer) getAppForHeight(height int64) (servertypes.ABCI, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.currentHeight = height

	// use the latest app if the height is beyond all defined versions
	if m.versions.ShouldLatestApp(height) {
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
			log.Printf("Stopping app for height %d", m.activeVersion.UntilHeight)
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
					log.Printf("Warning: PreHandler failed: %v", err)
					// Continue anyway as the pre-handler might be optional
				}
			}

			// start the new app
			programArgs := os.Args
			if len(os.Args) > 2 {
				programArgs = os.Args[2:] // Remove 'appd start' args
			}

			if err := currentVersion.Appd.Run(append(programArgs, currentVersion.StartArgs...)...); err != nil {
				return nil, fmt.Errorf("failed to start app for height %d: %w", height, err)
			}

			if currentVersion.Appd.Pid() == appd.AppdStopped {
				return nil, fmt.Errorf("app for height %d failed to start", height)
			}

			m.activeVersion = currentVersion
			m.started = true
		}
	}

	return NewRemoteABCIClient(m.conn), nil
}

func (m *multiplexer) ApplySnapshotChunk(_ context.Context, req *abci.RequestApplySnapshotChunk) (*abci.ResponseApplySnapshotChunk, error) {
	app, err := m.getAppForHeight(m.lastHeight)
	if err != nil {
		return nil, fmt.Errorf("failed to get app for height %d: %w", m.lastHeight, err)
	}
	return app.ApplySnapshotChunk(req)
}

func (m *multiplexer) CheckTx(_ context.Context, req *abci.RequestCheckTx) (*abci.ResponseCheckTx, error) {
	app, err := m.getAppForHeight(m.lastHeight)
	if err != nil {
		return nil, fmt.Errorf("failed to get app for height %d: %w", m.lastHeight, err)
	}
	return app.CheckTx(req)
}

func (m *multiplexer) Commit(context.Context, *abci.RequestCommit) (*abci.ResponseCommit, error) {
	app, err := m.getAppForHeight(m.lastHeight)
	if err != nil {
		return nil, fmt.Errorf("failed to get app for height %d: %w", m.lastHeight, err)
	}
	return app.Commit()
}

func (m *multiplexer) ExtendVote(ctx context.Context, req *abci.RequestExtendVote) (*abci.ResponseExtendVote, error) {
	m.lastHeight = req.Height
	app, err := m.getAppForHeight(req.Height)
	if err != nil {
		return nil, fmt.Errorf("failed to get app for height %d: %w", req.Height, err)
	}
	return app.ExtendVote(ctx, req)
}

func (m *multiplexer) FinalizeBlock(_ context.Context, req *abci.RequestFinalizeBlock) (*abci.ResponseFinalizeBlock, error) {
	m.lastHeight = req.Height
	app, err := m.getAppForHeight(req.Height)
	if err != nil {
		return nil, fmt.Errorf("failed to get app for height %d: %w", req.Height, err)
	}
	return app.FinalizeBlock(req)
}

func (m *multiplexer) Info(_ context.Context, req *abci.RequestInfo) (*abci.ResponseInfo, error) {
	return m.latestApp.Info(req) // Always use latest app for Info
}

func (m *multiplexer) InitChain(_ context.Context, req *abci.RequestInitChain) (*abci.ResponseInitChain, error) {
	app, err := m.getAppForHeight(0)
	if err != nil {
		return nil, fmt.Errorf("failed to get app for genesis: %w", err)
	}
	return app.InitChain(req)
}

func (m *multiplexer) ListSnapshots(_ context.Context, req *abci.RequestListSnapshots) (*abci.ResponseListSnapshots, error) {
	app, err := m.getAppForHeight(m.lastHeight)
	if err != nil {
		return nil, fmt.Errorf("failed to get app for height %d: %w", m.lastHeight, err)
	}
	return app.ListSnapshots(req)
}

func (m *multiplexer) LoadSnapshotChunk(_ context.Context, req *abci.RequestLoadSnapshotChunk) (*abci.ResponseLoadSnapshotChunk, error) {
	app, err := m.getAppForHeight(int64(req.Height))
	if err != nil {
		return nil, fmt.Errorf("failed to get app for height %d: %w", req.Height, err)
	}
	return app.LoadSnapshotChunk(req)
}

func (m *multiplexer) OfferSnapshot(_ context.Context, req *abci.RequestOfferSnapshot) (*abci.ResponseOfferSnapshot, error) {
	app, err := m.getAppForHeight(m.lastHeight)
	if err != nil {
		return nil, fmt.Errorf("failed to get app for height %d: %w", m.lastHeight, err)
	}
	return app.OfferSnapshot(req)
}

func (m *multiplexer) PrepareProposal(_ context.Context, req *abci.RequestPrepareProposal) (*abci.ResponsePrepareProposal, error) {
	m.lastHeight = req.Height
	app, err := m.getAppForHeight(req.Height)
	if err != nil {
		return nil, fmt.Errorf("failed to get app for height %d: %w", req.Height, err)
	}
	return app.PrepareProposal(req)
}

func (m *multiplexer) ProcessProposal(_ context.Context, req *abci.RequestProcessProposal) (*abci.ResponseProcessProposal, error) {
	m.lastHeight = req.Height
	app, err := m.getAppForHeight(req.Height)
	if err != nil {
		return nil, fmt.Errorf("failed to get app for height %d: %w", req.Height, err)
	}
	return app.ProcessProposal(req)
}

func (m *multiplexer) Query(ctx context.Context, req *abci.RequestQuery) (*abci.ResponseQuery, error) {
	app, err := m.getAppForHeight(req.Height)
	if err != nil {
		return nil, fmt.Errorf("failed to get app for height %d: %w", req.Height, err)
	}
	return app.Query(ctx, req)
}

func (m *multiplexer) VerifyVoteExtension(_ context.Context, req *abci.RequestVerifyVoteExtension) (*abci.ResponseVerifyVoteExtension, error) {
	m.lastHeight = req.Height
	app, err := m.getAppForHeight(req.Height)
	if err != nil {
		return nil, fmt.Errorf("failed to get app for height %d: %w", req.Height, err)
	}
	return app.VerifyVoteExtension(req)
}
