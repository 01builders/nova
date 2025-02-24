package abci

import (
	"context"

	"google.golang.org/grpc"

	"github.com/cometbft/cometbft/abci/types"
)

type RemoteABCIClient struct {
	conn *grpc.ClientConn
}

// NewRemoteABCIClient returns a new ABCI Client.
// This gRPC client connects to cometbft via grpc.
func NewRemoteABCIClient(conn *grpc.ClientConn) *RemoteABCIClient {
	return &RemoteABCIClient{conn: conn}
}

// ApplySnapshotChunk implements types.ABCI.
func (a *RemoteABCIClient) ApplySnapshotChunk(req *types.RequestApplySnapshotChunk) (*types.ResponseApplySnapshotChunk, error) {
	panic("unimplemented")
}

// CheckTx implements types.ABCI.
func (a *RemoteABCIClient) CheckTx(req *types.RequestCheckTx) (*types.ResponseCheckTx, error) {
	panic("unimplemented")
}

// Commit implements types.ABCI.
func (a *RemoteABCIClient) Commit() (*types.ResponseCommit, error) {
	panic("unimplemented")
}

// ExtendVote implements types.ABCI.
func (a *RemoteABCIClient) ExtendVote(ctx context.Context, req *types.RequestExtendVote) (*types.ResponseExtendVote, error) {
	panic("unimplemented")
}

// FinalizeBlock implements types.ABCI.
func (a *RemoteABCIClient) FinalizeBlock(req *types.RequestFinalizeBlock) (*types.ResponseFinalizeBlock, error) {
	panic("unimplemented")
}

// Info implements types.ABCI.
func (a *RemoteABCIClient) Info(req *types.RequestInfo) (*types.ResponseInfo, error) {
	panic("unimplemented")
}

// InitChain implements types.ABCI.
func (a *RemoteABCIClient) InitChain(req *types.RequestInitChain) (*types.ResponseInitChain, error) {
	panic("unimplemented")
}

// ListSnapshots implements types.ABCI.
func (a *RemoteABCIClient) ListSnapshots(req *types.RequestListSnapshots) (*types.ResponseListSnapshots, error) {
	panic("unimplemented")
}

// LoadSnapshotChunk implements types.ABCI.
func (a *RemoteABCIClient) LoadSnapshotChunk(req *types.RequestLoadSnapshotChunk) (*types.ResponseLoadSnapshotChunk, error) {
	panic("unimplemented")
}

// OfferSnapshot implements types.ABCI.
func (a *RemoteABCIClient) OfferSnapshot(req *types.RequestOfferSnapshot) (*types.ResponseOfferSnapshot, error) {
	panic("unimplemented")
}

// PrepareProposal implements types.ABCI.
func (a *RemoteABCIClient) PrepareProposal(req *types.RequestPrepareProposal) (*types.ResponsePrepareProposal, error) {
	panic("unimplemented")
}

// ProcessProposal implements types.ABCI.
func (a *RemoteABCIClient) ProcessProposal(req *types.RequestProcessProposal) (*types.ResponseProcessProposal, error) {
	panic("unimplemented")
}

// Query implements types.ABCI.
func (a *RemoteABCIClient) Query(ctx context.Context, req *types.RequestQuery) (*types.ResponseQuery, error) {
	panic("unimplemented")
}

// VerifyVoteExtension implements types.ABCI.
func (a *RemoteABCIClient) VerifyVoteExtension(req *types.RequestVerifyVoteExtension) (*types.ResponseVerifyVoteExtension, error) {
	panic("unimplemented")
}
