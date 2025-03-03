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

	latestApp servertypes.ABCI
	versions  Versions
	conn      *grpc.ClientConn
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

	// prepare remote app
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
			programArgs = os.Args[2:] // 'remove appd start' args
		}

		if err := currentVersion.Appd.Run(append(programArgs, currentVersion.StartArgs...)...); err != nil {
			return nil, fmt.Errorf("failed to start app: %w", err)
		}

		if currentVersion.Appd.Pid() == appd.AppdStopped { // should never happen
			panic("app has not started")
		}
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
func (m *multiplexer) getAppForHeight(height int64) servertypes.ABCI {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.currentHeight = height

	if m.versions.ShouldLatestApp(height) {
		return m.latestApp
	}

	return NewRemoteABCIClient(m.conn)
}

func (m *multiplexer) ApplySnapshotChunk(_ context.Context, req *abci.RequestApplySnapshotChunk) (*abci.ResponseApplySnapshotChunk, error) {
	return m.getAppForHeight(m.lastHeight).ApplySnapshotChunk(req)
}

func (m *multiplexer) CheckTx(_ context.Context, req *abci.RequestCheckTx) (*abci.ResponseCheckTx, error) {
	return m.getAppForHeight(m.lastHeight).CheckTx(req)
}

func (m *multiplexer) Commit(context.Context, *abci.RequestCommit) (*abci.ResponseCommit, error) {
	return m.getAppForHeight(m.lastHeight).Commit()
}

func (m *multiplexer) ExtendVote(ctx context.Context, req *abci.RequestExtendVote) (*abci.ResponseExtendVote, error) {
	m.lastHeight = req.Height
	return m.getAppForHeight(req.Height).ExtendVote(ctx, req)
}

func (m *multiplexer) FinalizeBlock(_ context.Context, req *abci.RequestFinalizeBlock) (*abci.ResponseFinalizeBlock, error) {
	m.lastHeight = req.Height
	return m.getAppForHeight(req.Height).FinalizeBlock(req)
}

func (m *multiplexer) Info(_ context.Context, req *abci.RequestInfo) (*abci.ResponseInfo, error) {
	return m.latestApp.Info(req) // Always use latest app for Info
}

func (m *multiplexer) InitChain(_ context.Context, req *abci.RequestInitChain) (*abci.ResponseInitChain, error) {
	return m.getAppForHeight(0).InitChain(req)
}

func (m *multiplexer) ListSnapshots(_ context.Context, req *abci.RequestListSnapshots) (*abci.ResponseListSnapshots, error) {
	return m.getAppForHeight(m.lastHeight).ListSnapshots(req)
}

func (m *multiplexer) LoadSnapshotChunk(_ context.Context, req *abci.RequestLoadSnapshotChunk) (*abci.ResponseLoadSnapshotChunk, error) {
	return m.getAppForHeight(int64(req.Height)).LoadSnapshotChunk(req)
}

func (m *multiplexer) OfferSnapshot(_ context.Context, req *abci.RequestOfferSnapshot) (*abci.ResponseOfferSnapshot, error) {
	return m.getAppForHeight(m.lastHeight).OfferSnapshot(req)
}

func (m *multiplexer) PrepareProposal(_ context.Context, req *abci.RequestPrepareProposal) (*abci.ResponsePrepareProposal, error) {
	m.lastHeight = req.Height
	return m.getAppForHeight(req.Height).PrepareProposal(req)
}

func (m *multiplexer) ProcessProposal(_ context.Context, req *abci.RequestProcessProposal) (*abci.ResponseProcessProposal, error) {
	m.lastHeight = req.Height
	return m.getAppForHeight(req.Height).ProcessProposal(req)
}

func (m *multiplexer) Query(ctx context.Context, req *abci.RequestQuery) (*abci.ResponseQuery, error) {
	// queries shouldn't store the queried height into the multiplexer
	return m.getAppForHeight(req.Height).Query(ctx, req)
}

func (m *multiplexer) VerifyVoteExtension(_ context.Context, req *abci.RequestVerifyVoteExtension) (*abci.ResponseVerifyVoteExtension, error) {
	m.lastHeight = req.Height
	return m.getAppForHeight(req.Height).VerifyVoteExtension(req)
}
