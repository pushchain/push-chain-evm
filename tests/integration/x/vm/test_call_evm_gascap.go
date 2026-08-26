package vm

import (
	"math/big"

	"github.com/ethereum/go-ethereum/common"

	"github.com/cosmos/evm/contracts"
	"github.com/cosmos/evm/server/config"
	testconstants "github.com/cosmos/evm/testutil/constants"
	utiltx "github.com/cosmos/evm/testutil/tx"
	"github.com/cosmos/evm/x/erc20/types"
)

// TestCallEVMWithDataHonoursGasCap is the guard for F-2026-18818.
//
// CallEVMWithData accepted a gasCap and then hardcoded GasLimit to
// config.DefaultGasCap, so the parameter did nothing. Callers passing one - the
// IBC callback keeper passes its remaining Cosmos gas, intending to sandbox the
// EVM work - got an EVM message that could burn up to 25M gas regardless.
//
// The assertion that catches this is the middle case: a cap below what the call
// costs has to actually stop the call. Asserting only that a large cap succeeds
// would pass just as happily against the ignored parameter.
func (s *KeeperTestSuite) TestCallEVMWithDataHonoursGasCap() {
	erc20 := contracts.ERC20MinterBurnerDecimalsContract.ABI
	contractAddr := common.HexToAddress(testconstants.WEVMOSContractMainnet)

	data, err := erc20.Pack("balanceOf", utiltx.GenerateAddress())
	s.Require().NoError(err)

	callWithCap := func(gasCap *big.Int) (uint64, error) {
		s.SetupTest()
		res, err := s.Network.App.GetEVMKeeper().CallEVMWithData(
			s.Network.GetContext(), types.ModuleAddress, &contractAddr, data, false, gasCap,
		)
		if res == nil {
			return 0, err
		}
		return res.GasUsed, err
	}

	// Baseline: no cap. Establishes what the call actually costs, so the
	// too-small case below is derived rather than guessed.
	baseline, err := callWithCap(nil)
	s.Require().NoError(err, "an uncapped call must still work")
	s.Require().Positive(baseline)

	// A cap below the call's own cost must fail it. This is the case that fails
	// when gasCap is ignored.
	_, err = callWithCap(new(big.Int).SetUint64(baseline - 1))
	s.Require().Error(err, "a gasCap below the call's cost must stop the call")

	// A modest cap that comfortably covers the call succeeds, while staying two
	// orders of magnitude below DefaultGasCap - so success here is the supplied
	// cap being honoured, not the default ceiling doing the work.
	//
	// Note GasUsed is not itself a viable limit. It is the net, post-refund
	// figure (~24k for this call), whereas the execution needs materially more
	// than that to be *given* up front - a cap of 2x GasUsed still reverts. Do
	// not "tighten" this to baseline or a small multiple of it.
	room := new(big.Int).SetUint64(baseline * 10)
	used, err := callWithCap(room)
	s.Require().NoError(err, "a gasCap with headroom over the call's cost must succeed")
	s.Require().Less(room.Uint64(), config.DefaultGasCap,
		"the headroom cap must stay below DefaultGasCap or this proves nothing")
	s.Require().Positive(used)

	// DefaultGasCap remains the ceiling: a caller may narrow the limit, never
	// widen it.
	huge := new(big.Int).Mul(new(big.Int).SetUint64(config.DefaultGasCap), big.NewInt(10))
	used, err = callWithCap(huge)
	s.Require().NoError(err)
	s.Require().LessOrEqual(used, config.DefaultGasCap,
		"a gasCap above DefaultGasCap must not raise the EVM gas limit")

	// A non-positive or absurdly wide cap falls back to the default ceiling
	// rather than truncating to something meaningless.
	_, err = callWithCap(big.NewInt(0))
	s.Require().NoError(err, "a zero gasCap must fall back to DefaultGasCap, not to a zero limit")

	overUint64 := new(big.Int).Lsh(big.NewInt(1), 70)
	_, err = callWithCap(overUint64)
	s.Require().NoError(err, "a gasCap wider than uint64 must fall back to DefaultGasCap")
}
