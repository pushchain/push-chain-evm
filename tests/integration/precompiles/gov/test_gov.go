package gov

import (
	"math/big"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	ethtypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/holiman/uint256"

	"github.com/cosmos/evm/precompiles/gov"
	"github.com/cosmos/evm/testutil"
	"github.com/cosmos/evm/x/vm/statedb"
	evmtypes "github.com/cosmos/evm/x/vm/types"

	"cosmossdk.io/collections"

	sdk "github.com/cosmos/cosmos-sdk/types"
	govv1 "github.com/cosmos/cosmos-sdk/x/gov/types/v1"
)

func (s *PrecompileTestSuite) TestIsTransaction() {
	testCases := []struct {
		name   string
		method abi.Method
		isTx   bool
	}{
		{
			gov.VoteMethod,
			s.precompile.Methods[gov.VoteMethod],
			true,
		},
		{
			"invalid",
			abi.Method{},
			false,
		},
	}

	for _, tc := range testCases {
		s.Run(tc.name, func() {
			s.Require().Equal(s.precompile.IsTransaction(&tc.method), tc.isTx)
		})
	}
}

// TestRun tests the precompile's Run method.
func (s *PrecompileTestSuite) TestRun() {
	testcases := []struct {
		name        string
		malleate    func() (common.Address, []byte)
		readOnly    bool
		expPass     bool
		errContains string
	}{
		{
			name: "pass - vote transaction",
			malleate: func() (common.Address, []byte) {
				const proposalID uint64 = 1
				const option uint8 = 1
				const metadata = "metadata"

				input, err := s.precompile.Pack(
					gov.VoteMethod,
					s.keyring.GetAddr(0),
					proposalID,
					option,
					metadata,
				)
				s.Require().NoError(err, "failed to pack input")
				return s.keyring.GetAddr(0), input
			},
			readOnly: false,
			expPass:  true,
		},
	}

	for _, tc := range testcases {
		s.Run(tc.name, func() {
			// setup basic test suite
			s.SetupTest()
			ctx := s.network.GetContext()

			baseFee := s.network.App.GetEVMKeeper().GetBaseFee(ctx)

			// malleate testcase
			caller, input := tc.malleate()

			contract := vm.NewPrecompile(caller, s.precompile.Address(), uint256.NewInt(0), uint64(1e6))
			contract.Input = input

			contractAddr := contract.Address()
			// Build and sign Ethereum transaction
			txArgs := evmtypes.EvmTxArgs{
				ChainID:   evmtypes.GetEthChainConfig().ChainID,
				Nonce:     0,
				To:        &contractAddr,
				Amount:    nil,
				GasLimit:  100000,
				GasPrice:  testutil.ExampleMinGasPrices,
				GasFeeCap: baseFee,
				GasTipCap: big.NewInt(1),
				Accesses:  &ethtypes.AccessList{},
			}
			msg, err := s.factory.GenerateGethCoreMsg(s.keyring.GetPrivKey(0), txArgs)
			s.Require().NoError(err)

			// Instantiate config
			proposerAddress := ctx.BlockHeader().ProposerAddress
			cfg, err := s.network.App.GetEVMKeeper().EVMConfig(ctx, proposerAddress)
			s.Require().NoError(err, "failed to instantiate EVM config")

			// Instantiate EVM
			stDB := statedb.New(
				ctx,
				s.network.App.GetEVMKeeper(),
				statedb.NewEmptyTxConfig(),
			)
			evm := s.network.App.GetEVMKeeper().NewEVM(
				ctx, *msg, cfg, nil, stDB,
			)

			precompiles, found, err := s.network.App.GetEVMKeeper().GetPrecompileInstance(ctx, contractAddr)
			s.Require().NoError(err, "failed to instantiate precompile")
			s.Require().True(found, "not found precompile")
			evm.WithPrecompiles(precompiles.Map)

			// Run precompiled contract
			bz, err := s.precompile.Run(evm, contract, tc.readOnly)

			// Check results
			if tc.expPass {
				s.Require().NoError(err, "expected no error when running the precompile")
				s.Require().NotNil(bz, "expected returned bytes not to be nil")
			} else {
				s.Require().Error(err, "expected error to be returned when running the precompile")
				s.Require().Nil(bz, "expected returned bytes to be nil")
				s.Require().ErrorContains(err, tc.errContains)
			}
		})
	}
}

// runPrecompile executes the gov precompile with the given calldata, mirroring
// what the EVM does for a delivered transaction: the state DB is committed after
// the call so that every store write performed by the precompile is flushed to
// the underlying context.
func (s *PrecompileTestSuite) runPrecompile(ctx sdk.Context, input []byte, readOnly bool) []byte {
	baseFee := s.network.App.GetEVMKeeper().GetBaseFee(ctx)

	contract := vm.NewPrecompile(s.keyring.GetAddr(0), s.precompile.Address(), uint256.NewInt(0), uint64(1e6))
	contract.Input = input

	contractAddr := contract.Address()
	// Build and sign the Ethereum transaction backing the call.
	txArgs := evmtypes.EvmTxArgs{
		ChainID:   evmtypes.GetEthChainConfig().ChainID,
		Nonce:     0,
		To:        &contractAddr,
		Amount:    nil,
		GasLimit:  100000,
		GasPrice:  testutil.ExampleMinGasPrices,
		GasFeeCap: baseFee,
		GasTipCap: big.NewInt(1),
		Accesses:  &ethtypes.AccessList{},
	}
	msg, err := s.factory.GenerateGethCoreMsg(s.keyring.GetPrivKey(0), txArgs)
	s.Require().NoError(err)

	cfg, err := s.network.App.GetEVMKeeper().EVMConfig(ctx, ctx.BlockHeader().ProposerAddress)
	s.Require().NoError(err, "failed to instantiate EVM config")

	stDB := statedb.New(ctx, s.network.App.GetEVMKeeper(), statedb.NewEmptyTxConfig())
	evm := s.network.App.GetEVMKeeper().NewEVM(ctx, *msg, cfg, nil, stDB)

	precompiles, found, err := s.network.App.GetEVMKeeper().GetPrecompileInstance(ctx, contractAddr)
	s.Require().NoError(err, "failed to instantiate precompile")
	s.Require().True(found, "not found precompile")
	evm.WithPrecompiles(precompiles.Map)

	bz, err := s.precompile.Run(evm, contract, readOnly)
	s.Require().NoError(err, "expected no error when running the precompile")
	s.Require().NotNil(bz, "expected returned bytes not to be nil")

	// A delivered tx always commits the state DB, which flushes the precompile's
	// cache context onto the underlying multi store.
	s.Require().NoError(stDB.Commit(), "failed to commit the state DB")

	return bz
}

// proposalVoters returns the voters that currently have a vote stored for the
// given proposal.
func (s *PrecompileTestSuite) proposalVoters(ctx sdk.Context, proposalID uint64) []string {
	govKeeper := s.network.App.GetGovKeeper()

	voters := []string{}
	err := govKeeper.Votes.Walk(
		ctx,
		collections.NewPrefixedPairRange[uint64, sdk.AccAddress](proposalID),
		func(_ collections.Pair[uint64, sdk.AccAddress], vote govv1.Vote) (bool, error) {
			voters = append(voters, vote.Voter)
			return false, nil
		},
	)
	s.Require().NoError(err, "failed to walk the proposal votes")

	return voters
}

// TestGetTallyResultDoesNotDeleteVotes is the regression test for F-2026-18187.
//
// The SDK's gov TallyResult query delegates to Keeper.Tally, which deletes every
// vote it counts. That is safe on the SDK's own query paths because a gRPC query
// runs against a context whose writes are never committed. Precompiles break that
// invariant: cmn.Precompile.runNativeAction commits the state DB cache context
// unconditionally, so before the fix a plain committing EVM call to
// getTallyResult wiped the votes of any proposal in the voting period.
//
// Query classification does not help here: getTallyResult is absent from
// IsTransaction, but that only feeds the staticcall guard and the gas price, so
// both a CALL and a STATICCALL reach the mutating SDK code. Both are covered.
func (s *PrecompileTestSuite) TestGetTallyResultDoesNotDeleteVotes() {
	const proposalID uint64 = 1

	testcases := []struct {
		name     string
		readOnly bool
	}{
		{
			name:     "call",
			readOnly: false,
		},
		{
			name:     "staticcall",
			readOnly: true,
		},
	}

	for _, tc := range testcases {
		s.Run(tc.name, func() {
			s.SetupTest()

			ctx := s.network.GetContext()
			govKeeper := s.network.App.GetGovKeeper()

			// Account 0 is the only delegator of the test network, so it holds all of
			// the voting power (3e18). Split its vote across every option so that the
			// positive control below exercises the whole tally result.
			s.Require().NoError(govKeeper.AddVote(ctx, proposalID, s.keyring.GetAccAddr(0), govv1.WeightedVoteOptions{
				{Option: govv1.OptionYes, Weight: "0.4"},
				{Option: govv1.OptionAbstain, Weight: "0.3"},
				{Option: govv1.OptionNo, Weight: "0.2"},
				{Option: govv1.OptionNoWithVeto, Weight: "0.1"},
			}, ""))
			// Accounts 1 and 2 hold no delegations: they add nothing to the tally but
			// are stored votes all the same, and Keeper.Tally removes them too.
			s.Require().NoError(govKeeper.AddVote(ctx, proposalID, s.keyring.GetAccAddr(1), govv1.NewNonSplitVoteOption(govv1.OptionNo), ""))
			s.Require().NoError(govKeeper.AddVote(ctx, proposalID, s.keyring.GetAccAddr(2), govv1.NewNonSplitVoteOption(govv1.OptionYes), ""))

			expVoters := []string{
				s.keyring.GetAccAddr(0).String(),
				s.keyring.GetAccAddr(1).String(),
				s.keyring.GetAccAddr(2).String(),
			}
			s.Require().ElementsMatch(expVoters, s.proposalVoters(ctx, proposalID), "expected the votes to be stored before the query")

			proposal, err := govKeeper.Proposals.Get(ctx, proposalID)
			s.Require().NoError(err)
			s.Require().Equal(govv1.StatusVotingPeriod, proposal.Status, "the proposal must be in the voting period for the tally to mutate state")

			input, err := s.precompile.Pack(gov.GetTallyResultMethod, proposalID)
			s.Require().NoError(err, "failed to pack input")

			bz := s.runPrecompile(ctx, input, tc.readOnly)

			// Regression assertion: the query must leave every vote in place.
			s.Require().ElementsMatch(expVoters, s.proposalVoters(ctx, proposalID), "getTallyResult must not delete the proposal's votes")

			// Positive control: the tally must still be the one the SDK computes, so
			// we do not trade a mutating query for a wrong one.
			expTally := gov.TallyResultData{
				Yes:        "1200000000000000000", // 40% of 3e18
				Abstain:    "900000000000000000",  // 30% of 3e18
				No:         "600000000000000000",  // 20% of 3e18
				NoWithVeto: "300000000000000000",  // 10% of 3e18
			}

			var out gov.TallyResultOutput
			s.Require().NoError(s.precompile.UnpackIntoInterface(&out, gov.GetTallyResultMethod, bz))
			s.Require().Equal(expTally, out.TallyResult, "unexpected tally result")

			// And the query must be repeatable: a second call sees the same votes and
			// therefore returns the same numbers.
			var secondOut gov.TallyResultOutput
			s.Require().NoError(s.precompile.UnpackIntoInterface(&secondOut, gov.GetTallyResultMethod, s.runPrecompile(ctx, input, tc.readOnly)))
			s.Require().Equal(expTally, secondOut.TallyResult, "the tally result must be stable across calls")
			s.Require().ElementsMatch(expVoters, s.proposalVoters(ctx, proposalID), "the votes must survive repeated queries")
		})
	}
}
