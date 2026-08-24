package gov

import (
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/core/vm"

	vmstoretypes "github.com/cosmos/evm/x/vm/store/types"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

const (
	// GetVotesMethod defines the method name for the votes precompile request.
	GetVotesMethod = "getVotes"
	// GetVoteMethod defines the method name for the vote precompile request.
	GetVoteMethod = "getVote"
	// GetDepositMethod defines the method name for the deposit precompile request.
	GetDepositMethod = "getDeposit"
	// GetDepositsMethod defines the method name for the deposits precompile request.
	GetDepositsMethod = "getDeposits"
	// GetTallyResultMethod defines the method name for the tally result precompile request.
	GetTallyResultMethod = "getTallyResult"
	// GetProposalMethod defines the method name for the proposal precompile request.
	GetProposalMethod = "getProposal"
	// GetProposalsMethod defines the method name for the proposals precompile request.
	GetProposalsMethod = "getProposals"
	// GetParamsMethod defines the method name for the get params precompile request.
	GetParamsMethod = "getParams"
	// GetConstitutionMethod defines the method name for the get constitution precompile request.
	GetConstitutionMethod = "getConstitution"
)

// GetVotes implements the query logic for getting votes for a proposal.
func (p *Precompile) GetVotes(
	ctx sdk.Context,
	method *abi.Method,
	_ *vm.Contract,
	args []interface{},
) ([]byte, error) {
	queryVotesReq, err := ParseVotesArgs(method, args)
	if err != nil {
		return nil, err
	}

	res, err := p.govQuerier.Votes(ctx, queryVotesReq)
	if err != nil {
		return nil, err
	}

	output, err := new(VotesOutput).FromResponse(res)
	if err != nil {
		return nil, err
	}
	return method.Outputs.Pack(output.Votes, output.PageResponse)
}

// GetVote implements the query logic for getting votes for a proposal.
func (p *Precompile) GetVote(
	ctx sdk.Context,
	method *abi.Method,
	_ *vm.Contract,
	args []interface{},
) ([]byte, error) {
	queryVotesReq, err := ParseVoteArgs(args, p.addrCdc)
	if err != nil {
		return nil, err
	}

	res, err := p.govQuerier.Vote(ctx, queryVotesReq)
	if err != nil {
		return nil, err
	}

	output, err := new(VoteOutput).FromResponse(res)
	if err != nil {
		return nil, err
	}
	return method.Outputs.Pack(output.Vote)
}

// GetDeposit implements the query logic for getting a deposit for a proposal.
func (p *Precompile) GetDeposit(
	ctx sdk.Context,
	method *abi.Method,
	_ *vm.Contract,
	args []interface{},
) ([]byte, error) {
	queryDepositReq, err := ParseDepositArgs(args, p.addrCdc)
	if err != nil {
		return nil, err
	}

	res, err := p.govQuerier.Deposit(ctx, queryDepositReq)
	if err != nil {
		return nil, err
	}

	output, err := new(DepositOutput).FromResponse(res)
	if err != nil {
		return nil, err
	}
	return method.Outputs.Pack(output.Deposit)
}

// GetDeposits implements the query logic for getting all deposits for a proposal.
func (p *Precompile) GetDeposits(
	ctx sdk.Context,
	method *abi.Method,
	_ *vm.Contract,
	args []interface{},
) ([]byte, error) {
	queryDepositsReq, err := ParseDepositsArgs(method, args)
	if err != nil {
		return nil, err
	}

	res, err := p.govQuerier.Deposits(ctx, queryDepositsReq)
	if err != nil {
		return nil, err
	}

	output, err := new(DepositsOutput).FromResponse(res)
	if err != nil {
		return nil, err
	}
	return method.Outputs.Pack(output.Deposits, output.PageResponse)
}

// GetTallyResult implements the query logic for getting the tally result of a proposal.
func (p *Precompile) GetTallyResult(
	ctx sdk.Context,
	method *abi.Method,
	_ *vm.Contract,
	args []interface{},
) ([]byte, error) {
	queryTallyResultReq, err := ParseTallyResultArgs(args)
	if err != nil {
		return nil, err
	}

	// The SDK's TallyResult query is *not* read-only for a proposal in the voting
	// period: it delegates to the gov Keeper.Tally, which deletes every vote it
	// counts (x/gov/keeper/tally.go). That is safe on the SDK's own query paths
	// because a gRPC query runs against a context whose writes are never committed.
	//
	// Precompiles break that invariant: RunNativeAction commits the state DB cache
	// context unconditionally, so a store write performed here would survive the
	// enclosing EVM transaction and a plain CALL to getTallyResult would wipe the
	// proposal's votes. Run the query against a throwaway cache instead, so the
	// deletions are discarded while the tally itself -- delegations, weighted votes,
	// validator defaults -- is still the one the SDK computes.
	cacheCtx, discardWrites := newDiscardedCacheContext(ctx)
	defer discardWrites()

	res, err := p.govQuerier.TallyResult(cacheCtx, queryTallyResultReq)
	if err != nil {
		return nil, err
	}

	output := new(TallyResultOutput).FromResponse(res)
	return method.Outputs.Pack(output.TallyResult)
}

// newDiscardedCacheContext returns a context to run a query on, together with the
// function that throws away every store write the query performed. The caller is
// expected to always call it -- the writes are never meant to reach the parent
// store.
//
// sdk.Context.CacheContext alone is not enough inside a precompile. It defers to
// the underlying multi store's CacheMultiStore, and while the SDK's stores return
// a detached cache whose writes only reach the parent through the (here dropped)
// write function, the store the EVM runs precompiles on does not:
// x/vm/store/snapshotmulti pushes a snapshot layer and returns the very same
// store, so the layer stays live and is flushed along with everything else when
// the state DB commits. On that store the layer has to be popped explicitly,
// which is what the snapshot/revert pair below does.
func newDiscardedCacheContext(ctx sdk.Context) (sdk.Context, func()) {
	if snapshotter, ok := ctx.MultiStore().(vmstoretypes.Snapshotter); ok {
		snapshot := snapshotter.Snapshot()
		return ctx, func() { snapshotter.RevertToSnapshot(snapshot) }
	}

	cacheCtx, _ := ctx.CacheContext() // write function intentionally discarded
	return cacheCtx, func() {}
}

// GetProposal implements the query logic for getting a proposal
func (p *Precompile) GetProposal(
	ctx sdk.Context,
	method *abi.Method,
	_ *vm.Contract,
	args []interface{},
) ([]byte, error) {
	queryProposalReq, err := ParseProposalArgs(args)
	if err != nil {
		return nil, err
	}

	res, err := p.govQuerier.Proposal(ctx, queryProposalReq)
	if err != nil {
		return nil, err
	}

	output, err := new(ProposalOutput).FromResponse(res)
	if err != nil {
		return nil, err
	}
	return method.Outputs.Pack(output.Proposal)
}

// GetProposals implements the query logic for getting proposals
func (p *Precompile) GetProposals(
	ctx sdk.Context,
	method *abi.Method,
	_ *vm.Contract,
	args []interface{},
) ([]byte, error) {
	queryProposalsReq, err := ParseProposalsArgs(method, args, p.addrCdc)
	if err != nil {
		return nil, err
	}

	res, err := p.govQuerier.Proposals(ctx, queryProposalsReq)
	if err != nil {
		return nil, err
	}

	output, err := new(ProposalsOutput).FromResponse(res)
	if err != nil {
		return nil, err
	}
	return method.Outputs.Pack(output.Proposals, output.PageResponse)
}

// GetParams implements the query logic for getting governance parameters
func (p *Precompile) GetParams(
	ctx sdk.Context,
	method *abi.Method,
	_ *vm.Contract,
	args []interface{},
) ([]byte, error) {
	queryParamsReq, err := BuildQueryParamsRequest(args)
	if err != nil {
		return nil, err
	}

	res, err := p.govQuerier.Params(ctx, queryParamsReq)
	if err != nil {
		return nil, err
	}

	output := new(ParamsOutput).FromResponse(res)
	return method.Outputs.Pack(output)
}

// GetConstitution implements the query logic for getting the constitution
func (p *Precompile) GetConstitution(
	ctx sdk.Context,
	method *abi.Method,
	_ *vm.Contract,
	args []interface{},
) ([]byte, error) {
	req, err := BuildQueryConstitutionRequest(args)
	if err != nil {
		return nil, err
	}

	res, err := p.govQuerier.Constitution(ctx, req)
	if err != nil {
		return nil, err
	}

	return method.Outputs.Pack(res.Constitution)
}
