package feemarket

import (
	"math"

	"github.com/cosmos/evm/testutil/integration/evm/network"

	storetypes "cosmossdk.io/store/types"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

func (s *KeeperTestSuite) TestEndBlock() {
	var (
		nw  *network.UnitTestNetwork
		ctx sdk.Context
	)

	testCases := []struct {
		name         string
		NoBaseFee    bool
		malleate     func()
		expGasWanted uint64
	}{
		{
			"baseFee nil",
			true,
			func() {},
			uint64(0),
		},
		{
			"pass",
			false,
			func() {
				meter := storetypes.NewGasMeter(uint64(1000000000))
				ctx = ctx.WithBlockGasMeter(meter)
				nw.App.GetFeeMarketKeeper().SetTransientBlockGasWanted(ctx, 5000000)
			},
			uint64(2500000),
		},
	}
	for _, tc := range testCases {
		s.Run(tc.name, func() {
			// reset network and context
			nw = network.NewUnitTestNetwork(s.create, s.options...)
			ctx = nw.GetContext()

			params := nw.App.GetFeeMarketKeeper().GetParams(ctx)
			params.NoBaseFee = tc.NoBaseFee

			err := nw.App.GetFeeMarketKeeper().SetParams(ctx, params)
			s.NoError(err)

			tc.malleate()

			err = nw.App.GetFeeMarketKeeper().EndBlock(ctx)
			s.NoError(err)

			gasWanted := nw.App.GetFeeMarketKeeper().GetBlockGasWanted(ctx)
			s.Equal(tc.expGasWanted, gasWanted, tc.name)
		})
	}
}

// TestEndBlockGasWantedOverflowIsClamped covers F-2026-18144.
//
// The cumulative gas wanted is a uint64 sum of per-transaction declarations.
// Under an unbounded block gas limit (max_gas: -1) nothing caps a single
// declaration, so two transactions declaring MaxInt64 sum into
// (MaxInt64, MaxUint64] - a perfectly valid uint64 that cannot be converted
// back to int64. EndBlock used to return an error for that, and an error out
// of EndBlock surfaces through FinalizeBlock after the block has been decided,
// stopping the chain. It must clamp instead.
func (s *KeeperTestSuite) TestEndBlockGasWantedOverflowIsClamped() {
	testCases := []struct {
		name         string
		gasWanted    uint64
		expGasWanted uint64
	}{
		{
			// 2 * MaxInt64, the two-transaction shape from the finding.
			"two MaxInt64 declarations",
			uint64(math.MaxInt64)*2 - 1,
			// clamped to MaxInt64, then * MinGasMultiplier (0.5)
			uint64(math.MaxInt64) / 2,
		},
		{
			"MaxUint64",
			uint64(math.MaxUint64),
			uint64(math.MaxInt64) / 2,
		},
		{
			// Exactly MaxInt64 is representable and must be left alone.
			"MaxInt64 is not clamped",
			uint64(math.MaxInt64),
			uint64(math.MaxInt64) / 2,
		},
	}

	for _, tc := range testCases {
		s.Run(tc.name, func() {
			nw := network.NewUnitTestNetwork(s.create, s.options...)
			ctx := nw.GetContext().WithBlockGasMeter(storetypes.NewInfiniteGasMeter())

			nw.App.GetFeeMarketKeeper().SetTransientBlockGasWanted(ctx, tc.gasWanted)

			err := nw.App.GetFeeMarketKeeper().EndBlock(ctx)

			gasWanted := nw.App.GetFeeMarketKeeper().GetBlockGasWanted(ctx)
			s.Equal(tc.expGasWanted, gasWanted, tc.name)
			s.NoError(err, "EndBlock must not fail the block on an oversized cumulative gas wanted")
		})
	}
}

// TestAddTransientGasWantedSaturates: the accumulation itself must not wrap.
// A wrapped total would silently under-report the block's gas wanted to the
// base fee calculation.
func (s *KeeperTestSuite) TestAddTransientGasWantedSaturates() {
	nw := network.NewUnitTestNetwork(s.create, s.options...)
	ctx := nw.GetContext()
	k := nw.App.GetFeeMarketKeeper()

	total, err := k.AddTransientGasWanted(ctx, math.MaxUint64)
	s.Require().NoError(err)
	s.Require().Equal(uint64(math.MaxUint64), total)

	total, err = k.AddTransientGasWanted(ctx, 1)
	s.Require().NoError(err)
	s.Equal(uint64(math.MaxUint64), total, "the cumulative gas wanted must saturate, not wrap to 0")
	s.Equal(uint64(math.MaxUint64), k.GetTransientGasWanted(ctx))
}
