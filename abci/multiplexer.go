package abci

import (
	"context"
	"fmt"
	"sync"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	abci "github.com/cometbft/cometbft/abci/types"
	servertypes "github.com/cosmos/cosmos-sdk/server/types"
)

const LastestVersion = "latest"

// Version defines the configuration for remote apps
type Version struct {
	Height      int64
	Version     string
	BinaryPath  string
	GRPCAddress string
}

type multiplexer struct {
	currentHeight, lastHeight int64

	latestApp servertypes.ABCI
	versions  map[string]Version
	conns     map[string]*grpc.ClientConn
	mu        sync.Mutex
}

// NewMultiplexer creates a new ABCI wrapper for multiplexing
func NewMultiplexer(latestApp servertypes.ABCI, versions map[string]Version) (abci.Application, error) {
	wrapper := &multiplexer{
		latestApp: latestApp,
		versions:  versions,
	}

	// connect to each app
	for name, v := range versions {
		conn, err := grpc.Dial(v.GRPCAddress,
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		)
		if err != nil {
			return nil, fmt.Errorf("failed to connect to version %s at %s: %w", v.Version, v.GRPCAddress, err)
		}

		wrapper.conns[name] = conn
	}

	return wrapper, nil
}

// Helper to get the appropriate app based on height
func (m *multiplexer) getAppForHeight(height int64) servertypes.ABCI {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.currentHeight = height

	latestVersion := ""
	for name, v := range m.versions {
		if height <= v.Height {
			latestVersion = name
			break
		}
	}

	if latestVersion == "" || latestVersion == LastestVersion {
		return m.latestApp
	}

	if conn, ok := m.conns[latestVersion]; ok {
		return NewRemoteABCIClient(conn)
	}

	return m.latestApp // Fallback to latest app if version not found
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
