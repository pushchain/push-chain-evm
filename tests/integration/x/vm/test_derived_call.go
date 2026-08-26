package vm

import (
	"math"
	"math/big"
	"strconv"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/holiman/uint256"

	abcitypes "github.com/cometbft/cometbft/abci/types"

	"github.com/cosmos/evm/contracts"
	"github.com/cosmos/evm/server/config"
	testconstants "github.com/cosmos/evm/testutil/constants"
	"github.com/cosmos/evm/testutil/integration/evm/utils"
	utiltx "github.com/cosmos/evm/testutil/tx"
	"github.com/cosmos/evm/x/vm/statedb"
	evmtypes "github.com/cosmos/evm/x/vm/types"

	storetypes "cosmossdk.io/store/types"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

// findEvent returns the first ABCI event of the given type, or false if none.
func findEvent(events []abcitypes.Event, eventType string) (abcitypes.Event, bool) {
	for _, e := range events {
		if e.Type == eventType {
			return e, true
		}
	}
	return abcitypes.Event{}, false
}

// TestDerivedEVMCallModuleSenderRequiresNonce checks the module-sender guard: a
// derived call from a module account must carry a manual nonce (module accounts
// have no auth sequence to draw from), and otherwise errors out before the EVM.
func (s *KeeperTestSuite) TestDerivedEVMCallModuleSenderRequiresNonce() {
	s.SetupTest()

	erc20ABI := contracts.ERC20MinterBurnerDecimalsContract.ABI
	wevmos := common.HexToAddress(testconstants.WEVMOSContractMainnet)
	from := s.Keyring.GetAddr(0)

	_, err := s.Network.App.GetEVMKeeper().DerivedEVMCall(
		s.Network.GetContext(), erc20ABI, from, wevmos,
		big.NewInt(0), big.NewInt(100_000),
		true,  // commit
		false, // gasless
		true,  // isModuleSender
		nil,   // manualNonce — missing on purpose
		"balanceOf", from,
	)
	s.Require().Error(err)
	s.Require().ErrorContains(err, "manual nonce required for module sender")
}

// TestDerivedEVMCallWithDataEmitsDerivedTxEvents verifies that a committed
// derived call emits the events the JSON-RPC layer relies on to reconstruct a
// derived EVM tx: an ethereum_tx event carrying hash/index/data/recipient, and a
// message event tagged with the derived tx type (99) so the indexer can tell it
// apart from a standard MsgEthereumTx.
func (s *KeeperTestSuite) TestDerivedEVMCallWithDataEmitsDerivedTxEvents() {
	s.SetupTest()

	erc20ABI := contracts.ERC20MinterBurnerDecimalsContract.ABI
	wevmos := common.HexToAddress(testconstants.WEVMOSContractMainnet)
	from := s.Keyring.GetAddr(0)
	ctx := s.Network.GetContext()
	keeper := s.Network.App.GetEVMKeeper()

	idxBefore := keeper.GetTxIndexTransient(ctx)
	data, err := erc20ABI.Pack("balanceOf", from)
	s.Require().NoError(err)

	res, err := keeper.DerivedEVMCallWithData(
		ctx, from, &wevmos, data,
		true,  // commit
		false, // gasless
		false, // isModuleSender — draw the nonce from the account sequence
		big.NewInt(0), big.NewInt(100_000), nil,
	)
	s.Require().NoError(err)
	s.Require().False(res.Failed())

	abciEvents := ctx.EventManager().Events().ToABCIEvents()

	ethEvent, ok := findEvent(abciEvents, evmtypes.EventTypeEthereumTx)
	s.Require().True(ok, "derived call must emit an ethereum_tx event")
	s.Require().NotEmpty(utils.GetEventAttributeValue(ethEvent, evmtypes.AttributeKeyEthereumTxHash))
	s.Require().Equal(strconv.FormatUint(idxBefore, 10),
		utils.GetEventAttributeValue(ethEvent, evmtypes.AttributeKeyTxIndex))
	s.Require().NotEmpty(utils.GetEventAttributeValue(ethEvent, evmtypes.AttributeKeyTxData))
	s.Require().Equal(wevmos.Hex(),
		utils.GetEventAttributeValue(ethEvent, evmtypes.AttributeKeyRecipient))

	var txType string
	for _, e := range abciEvents {
		if e.Type != sdk.EventTypeMessage {
			continue
		}
		if v := utils.GetEventAttributeValue(e, evmtypes.AttributeKeyTxType); v != "" {
			txType = v
			break
		}
	}
	s.Require().Equal(strconv.FormatUint(evmtypes.DerivedTxType, 10), txType,
		"message event must tag the execution as a derived tx (type 99)")
}

// TestDerivedEVMCallWithDataAdvancesTxIndex verifies a committed derived call
// advances the block-level eth-tx index — the shared counter that also drives
// standard MsgEthereumTx ordering, so the next eth tx gets a unique index.
func (s *KeeperTestSuite) TestDerivedEVMCallWithDataAdvancesTxIndex() {
	s.SetupTest()

	erc20ABI := contracts.ERC20MinterBurnerDecimalsContract.ABI
	wevmos := common.HexToAddress(testconstants.WEVMOSContractMainnet)
	from := s.Keyring.GetAddr(0)
	ctx := s.Network.GetContext()
	keeper := s.Network.App.GetEVMKeeper()

	idxBefore := keeper.GetTxIndexTransient(ctx)
	data, err := erc20ABI.Pack("balanceOf", from)
	s.Require().NoError(err)

	_, err = keeper.DerivedEVMCallWithData(
		ctx, from, &wevmos, data,
		true, false, false, big.NewInt(0), big.NewInt(100_000), nil,
	)
	s.Require().NoError(err)
	s.Require().Equal(idxBefore+1, keeper.GetTxIndexTransient(ctx),
		"a committed derived call must advance the shared eth-tx index by one")
}

// TestDerivedEVMCallWithDataGasless verifies the gasless flag zeroes the gas
// reported in the emitted event, even though the EVM still meters gas internally
// (used by sponsored / module-initiated derived calls).
func (s *KeeperTestSuite) TestDerivedEVMCallWithDataGasless() {
	s.SetupTest()

	erc20ABI := contracts.ERC20MinterBurnerDecimalsContract.ABI
	wevmos := common.HexToAddress(testconstants.WEVMOSContractMainnet)
	from := s.Keyring.GetAddr(0)
	ctx := s.Network.GetContext()
	keeper := s.Network.App.GetEVMKeeper()

	data, err := erc20ABI.Pack("balanceOf", from)
	s.Require().NoError(err)

	res, err := keeper.DerivedEVMCallWithData(
		ctx, from, &wevmos, data,
		true, // commit
		true, // gasless
		false,
		big.NewInt(0), big.NewInt(100_000), nil,
	)
	s.Require().NoError(err)
	s.Require().False(res.Failed())

	ethEvent, ok := findEvent(ctx.EventManager().Events().ToABCIEvents(), evmtypes.EventTypeEthereumTx)
	s.Require().True(ok)
	s.Require().Equal("0", utils.GetEventAttributeValue(ethEvent, evmtypes.AttributeKeyTxGasUsed),
		"gasless derived call must report zero gas in its event")
	s.Require().Greater(res.GasUsed, uint64(0), "the EVM still meters gas internally")
}

// TestDerivedEVMCallWithDataRevertDoesNotMutateBloom is a regression test for F-2026-17738
// (a reverted derived execution must not mutate the block bloom) and the failure half of
// F-2026-17736 (failed execution must not commit). An ERC20 transfer from a zero-balance
// account reverts; the call must surface the error and leave the block bloom untouched.
func (s *KeeperTestSuite) TestDerivedEVMCallWithDataRevertDoesNotMutateBloom() {
	s.SetupTest()

	erc20ABI := contracts.ERC20MinterBurnerDecimalsContract.ABI
	wevmos := common.HexToAddress(testconstants.WEVMOSContractMainnet)
	from := s.Keyring.GetAddr(0)
	ctx := s.Network.GetContext()
	keeper := s.Network.App.GetEVMKeeper()

	bloomBefore := keeper.GetBlockBloomTransient(ctx).Bytes()

	// transfer 1 token from a zero-balance account → ERC20 reverts.
	data, err := erc20ABI.Pack("transfer", common.HexToAddress("0x000000000000000000000000000000000000dEaD"), big.NewInt(1))
	s.Require().NoError(err)

	res, err := keeper.DerivedEVMCallWithData(
		ctx, from, &wevmos, data,
		true, false, false, big.NewInt(0), big.NewInt(100_000), nil,
	)
	s.Require().Error(err, "a reverting derived call must surface an error")
	s.Require().NotNil(res)
	s.Require().True(res.Failed(), "the EVM execution reverted")

	s.Require().Equal(bloomBefore, keeper.GetBlockBloomTransient(ctx).Bytes(),
		"a reverted derived execution must not mutate the block bloom (F-2026-17738)")
}

// ---------------------------------------------------------------------------
// F-2026-18824: failed EVM calls must charge the parent Cosmos gas meter
// ---------------------------------------------------------------------------

// gasBurnerRevertCode expands memory to 1 MiB and then REVERTs:
//
//	PUSH1 0x00        // MSTORE value
//	PUSH3 0x100000    // MSTORE offset -> forces a 1 MiB memory expansion
//	MSTORE
//	PUSH1 0x00
//	PUSH1 0x00
//	REVERT
//
// The memory expansion costs ~2.2M gas, deterministically, and REVERT hands the
// rest back — so res.GasUsed lands far above the incidental KV-store gas the
// call also draws, which is what makes it a usable signal in these tests.
var gasBurnerRevertCode = []byte{0x60, 0x00, 0x62, 0x10, 0x00, 0x00, 0x52, 0x60, 0x00, 0x60, 0x00, 0xfd}

// gasBurnerSuccessCode is gasBurnerRevertCode with the trailing REVERT swapped
// for STOP: the same ~2.2M of memory expansion, but a successful execution.
var gasBurnerSuccessCode = []byte{0x60, 0x00, 0x62, 0x10, 0x00, 0x00, 0x52, 0x00}

// stopCode is a single STOP opcode: the cheapest possible successful execution,
// costing exactly the 21,000 intrinsic gas of the call itself. It gives the gas
// estimator something it can predict exactly.
var stopCode = []byte{0x00}

// invalidOpcodeCode is a single INVALID opcode. An exceptional halt burns every
// unit of gas handed to the frame, so res.GasUsed comes back equal to the gas
// cap — the same shape as running out of gas, without waiting for a loop to
// actually consume tens of millions of gas.
var invalidOpcodeCode = []byte{0xfe}

// parentGasMeterLimit bounds the enclosing "Cosmos tx" in these tests. It sits
// above config.DefaultGasCap (25M) so a correctly clamped failure fits, and far
// below an unclamped caller-supplied limit so an unclamped charge panics.
const parentGasMeterLimit = 60_000_000

// deployRawCode installs raw runtime bytecode at a fresh address.
func (s *KeeperTestSuite) deployRawCode(ctx sdk.Context, code []byte) common.Address {
	addr := utiltx.GenerateAddress()
	codeHash := crypto.Keccak256Hash(code)

	k := s.Network.App.GetEVMKeeper()
	k.SetCode(ctx, codeHash.Bytes(), code)
	s.Require().NoError(k.SetAccount(ctx, addr, statedb.Account{
		Nonce:    1,
		Balance:  new(uint256.Int),
		CodeHash: codeHash.Bytes(),
	}))
	return addr
}

// TestDerivedEVMCallWithDataFailedCallChargesParentGas is the regression test for
// F-2026-18824. A reverting derived call used to return before the parent
// GasMeter().ConsumeGas, so the EVM work it had just performed was free at the
// Cosmos meter — only the incidental KV-store gas leaked through. The call now
// charges res.GasUsed on the failure path exactly as it does on success.
func (s *KeeperTestSuite) TestDerivedEVMCallWithDataFailedCallChargesParentGas() {
	s.SetupTest()

	from := s.Keyring.GetAddr(0)
	ctx := s.Network.GetContext().WithGasMeter(storetypes.NewGasMeter(parentGasMeterLimit))
	keeper := s.Network.App.GetEVMKeeper()

	burner := s.deployRawCode(ctx, gasBurnerRevertCode)
	gasBefore := ctx.GasMeter().GasConsumed()

	res, err := keeper.DerivedEVMCallWithData(
		ctx, from, &burner, nil,
		true,  // commit
		false, // gasless
		false, // isModuleSender
		big.NewInt(0), big.NewInt(5_000_000), nil,
	)
	s.Require().Error(err, "the burner reverts, so the call must surface an error")
	s.Require().NotNil(res)
	s.Require().True(res.Failed())
	s.Require().Greater(res.GasUsed, uint64(2_000_000),
		"the burner must actually burn multi-million gas for this test to mean anything")

	charged := ctx.GasMeter().GasConsumed() - gasBefore
	s.Require().GreaterOrEqual(charged, res.GasUsed,
		"a failed derived call must charge its EVM gas to the parent Cosmos meter (F-2026-18824)")
	s.Require().LessOrEqual(charged, res.GasUsed+1_000_000,
		"res.GasUsed must be charged once, not doubled")
}

// TestDerivedEVMCallWithDataGaslessFailureChargesParentGas covers the same
// failure path with gasless=true. The flag only zeroes the TxGasUsed event
// attribute; it never exempted the parent meter, and must not become an exemption
// now that failures are charged.
func (s *KeeperTestSuite) TestDerivedEVMCallWithDataGaslessFailureChargesParentGas() {
	s.SetupTest()

	from := s.Keyring.GetAddr(0)
	ctx := s.Network.GetContext().WithGasMeter(storetypes.NewGasMeter(parentGasMeterLimit))
	keeper := s.Network.App.GetEVMKeeper()

	burner := s.deployRawCode(ctx, gasBurnerRevertCode)
	gasBefore := ctx.GasMeter().GasConsumed()

	res, err := keeper.DerivedEVMCallWithData(
		ctx, from, &burner, nil,
		true, // commit
		true, // gasless
		false,
		big.NewInt(0), big.NewInt(5_000_000), nil,
	)
	s.Require().Error(err)
	s.Require().True(res.Failed())
	s.Require().GreaterOrEqual(ctx.GasMeter().GasConsumed()-gasBefore, res.GasUsed,
		"gasless only zeroes the reported event attribute, not the parent meter")
}

// TestDerivedEVMCallWithDataClampsCallerGasLimit is the safety half of
// F-2026-18824. Charging res.GasUsed on failure is only sound because the
// effective gas cap is clamped to config.DefaultGasCap: on an exceptional halt
// res.GasUsed equals the cap, so an unclamped caller-supplied gasLimit would let
// the caller choose how much gas the enclosing Cosmos tx is forced to consume and
// panic it with OutOfGas — which, on the MsgVoteInbound path, means a lost
// validator vote. Here the caller asks for 10x DefaultGasCap and the call halts
// on an INVALID opcode; the parent meter must absorb at most the clamped cap.
func (s *KeeperTestSuite) TestDerivedEVMCallWithDataClampsCallerGasLimit() {
	s.SetupTest()

	from := s.Keyring.GetAddr(0)
	ctx := s.Network.GetContext().WithGasMeter(storetypes.NewGasMeter(parentGasMeterLimit))
	keeper := s.Network.App.GetEVMKeeper()

	halter := s.deployRawCode(ctx, invalidOpcodeCode)
	gasBefore := ctx.GasMeter().GasConsumed()

	//nolint:gosec // test-only constant, no overflow
	callerGasLimit := big.NewInt(int64(config.DefaultGasCap) * 10)

	var res *evmtypes.MsgEthereumTxResponse
	var err error
	s.Require().NotPanics(func() {
		res, err = keeper.DerivedEVMCallWithData(
			ctx, from, &halter, nil,
			true, false, false,
			big.NewInt(0), callerGasLimit, nil,
		)
	}, "a caller-supplied gasLimit above DefaultGasCap must not be able to panic the parent gas meter")

	s.Require().Error(err)
	s.Require().True(res.Failed())
	s.Require().Equal(config.DefaultGasCap, res.GasUsed,
		"an exceptional halt burns the whole cap, and the cap must be the clamped DefaultGasCap")

	charged := ctx.GasMeter().GasConsumed() - gasBefore
	s.Require().GreaterOrEqual(charged, config.DefaultGasCap,
		"the clamped cap is still charged to the parent meter")
	s.Require().LessOrEqual(charged, config.DefaultGasCap+1_000_000,
		"the parent meter must never be charged beyond the clamped cap")
}

// TestDerivedEVMCallWithDataSuccessChargesGasUsed pins the happy path: a
// successful derived call charges res.GasUsed once, unchanged by the failure-path
// fix.
func (s *KeeperTestSuite) TestDerivedEVMCallWithDataSuccessChargesGasUsed() {
	s.SetupTest()

	from := s.Keyring.GetAddr(0)
	ctx := s.Network.GetContext().WithGasMeter(storetypes.NewGasMeter(parentGasMeterLimit))
	keeper := s.Network.App.GetEVMKeeper()

	burner := s.deployRawCode(ctx, gasBurnerSuccessCode)
	gasBefore := ctx.GasMeter().GasConsumed()

	res, err := keeper.DerivedEVMCallWithData(
		ctx, from, &burner, nil,
		true, false, false,
		big.NewInt(0), big.NewInt(5_000_000), nil,
	)
	s.Require().NoError(err)
	s.Require().False(res.Failed())
	s.Require().Greater(res.GasUsed, uint64(2_000_000))

	charged := ctx.GasMeter().GasConsumed() - gasBefore
	s.Require().GreaterOrEqual(charged, res.GasUsed)
	s.Require().LessOrEqual(charged, res.GasUsed+1_000_000,
		"res.GasUsed must be charged exactly once on the success path")
}

// ---------------------------------------------------------------------------
// F-2026-18182: a gas limit that does not fit in uint64 must be rejected, not
// silently truncated to its low 64 bits.
// ---------------------------------------------------------------------------

// TestDerivedEVMCallWithDataRejectsNonUint64GasLimit is the regression test for
// F-2026-18182. big.Int.Uint64 is undefined for values that do not fit and in
// practice returns the low 64 bits, so before the IsUint64 guard a caller-supplied
// gasLimit at or above 2^64 was silently reinterpreted as a much smaller number.
// Node-side validation bounds UniversalPayload.GasLimit to uint256, so every value
// exercised here genuinely reaches this call.
//
// The third case is the one that makes the bug visible rather than merely wrong:
// 2^64 + 5_000_000 truncates to exactly 5_000_000 gas, which is enough for the
// burner to run to completion — so without the guard the call would report a clean
// success against a budget the caller never asked for. The first two truncate to 0
// and 5, which fail intrinsic-gas checks and so surface as a confusing out-of-gas
// error instead of a clear rejection. Both shapes are wrong; asserting on the error
// text plus the untouched parent meter distinguishes the fix from either of them.
func (s *KeeperTestSuite) TestDerivedEVMCallWithDataRejectsNonUint64GasLimit() {
	twoPow64 := new(big.Int).Lsh(big.NewInt(1), 64)

	testCases := []struct {
		name        string
		gasLimit    *big.Int
		truncatesTo string
	}{
		{
			name:        "2^64",
			gasLimit:    new(big.Int).Set(twoPow64),
			truncatesTo: "0",
		},
		{
			name:        "2^64 + 5",
			gasLimit:    new(big.Int).Add(twoPow64, big.NewInt(5)),
			truncatesTo: "5",
		},
		{
			name:        "2^64 + 5_000_000 (truncation would execute successfully)",
			gasLimit:    new(big.Int).Add(twoPow64, big.NewInt(5_000_000)),
			truncatesTo: "5000000",
		},
		{
			name:        "2^256 - 1 (the largest value node-side validation admits)",
			gasLimit:    new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1)),
			truncatesTo: "18446744073709551615",
		},
	}

	for _, tc := range testCases {
		s.Run(tc.name, func() {
			s.SetupTest()

			from := s.Keyring.GetAddr(0)
			ctx := s.Network.GetContext().WithGasMeter(storetypes.NewGasMeter(parentGasMeterLimit))
			keeper := s.Network.App.GetEVMKeeper()

			// The burner needs ~2.2M gas: reachable under the truncated budget of
			// the 5_000_000 case, unreachable under a correct rejection.
			burner := s.deployRawCode(ctx, gasBurnerSuccessCode)
			gasBefore := ctx.GasMeter().GasConsumed()

			res, err := keeper.DerivedEVMCallWithData(
				ctx, from, &burner, nil,
				true,  // commit
				false, // gasless
				false, // isModuleSender
				big.NewInt(0), tc.gasLimit, nil,
			)

			// Assert the *shape* of the outcome before the error text: under a
			// truncation regression the 5_000_000 case returns (res, nil) and every
			// error-flavoured assertion would be vacuous, so the no-execution checks
			// have to come first to be the ones that actually fail.
			charged := ctx.GasMeter().GasConsumed() - gasBefore
			s.Require().Less(charged, uint64(100_000),
				"a rejected gas limit must not run the EVM; truncating to %s would have burned ~2.2M",
				tc.truncatesTo)
			s.Require().Nil(res,
				"a rejected gas limit must not produce an execution result")

			s.Require().Error(err, "a gas limit above uint64 must be rejected outright")
			s.Require().ErrorContains(err, "does not fit in uint64",
				"the rejection must name the problem rather than surface as out-of-gas")
			s.Require().ErrorIs(err, evmtypes.ErrInvalidGasLimit)
		})
	}
}

// TestDerivedEVMCallWithDataAcceptsMaxUint64GasLimit pins the boundary: MaxUint64
// exactly does fit in a uint64, so it must be accepted by the guard and then
// clamped to config.DefaultGasCap by the F-2026-18824 clamp — not rejected. The
// INVALID opcode burns the whole frame, so res.GasUsed reports the effective cap
// and shows the clamp took hold.
func (s *KeeperTestSuite) TestDerivedEVMCallWithDataAcceptsMaxUint64GasLimit() {
	s.SetupTest()

	from := s.Keyring.GetAddr(0)
	ctx := s.Network.GetContext().WithGasMeter(storetypes.NewGasMeter(parentGasMeterLimit))
	keeper := s.Network.App.GetEVMKeeper()

	halter := s.deployRawCode(ctx, invalidOpcodeCode)
	gasBefore := ctx.GasMeter().GasConsumed()

	maxUint64 := new(big.Int).SetUint64(math.MaxUint64)

	var res *evmtypes.MsgEthereumTxResponse
	var err error
	s.Require().NotPanics(func() {
		res, err = keeper.DerivedEVMCallWithData(
			ctx, from, &halter, nil,
			true, false, false,
			big.NewInt(0), maxUint64, nil,
		)
	}, "MaxUint64 fits in a uint64 and must not panic the parent gas meter once clamped")

	s.Require().Error(err, "the halter halts exceptionally, so the call still surfaces an error")
	s.Require().NotErrorIs(err, evmtypes.ErrInvalidGasLimit,
		"MaxUint64 fits in uint64 and must not be rejected by the IsUint64 guard")
	s.Require().NotNil(res)
	s.Require().True(res.Failed())
	s.Require().Equal(config.DefaultGasCap, res.GasUsed,
		"MaxUint64 must be clamped to DefaultGasCap, not accepted as-is")

	s.Require().LessOrEqual(ctx.GasMeter().GasConsumed()-gasBefore, config.DefaultGasCap+1_000_000,
		"the parent meter must never be charged beyond the clamped cap")
}

// TestDerivedEVMCallWithDataNormalGasLimitUnaffected pins ordinary traffic against
// the new guard. 610,477 is the gasUsed observed on donut for a real UEA payload
// execution, ~40x below the 25M DefaultGasCap; it fits in a uint64, so the guard
// must be invisible to it.
func (s *KeeperTestSuite) TestDerivedEVMCallWithDataNormalGasLimitUnaffected() {
	s.SetupTest()

	erc20ABI := contracts.ERC20MinterBurnerDecimalsContract.ABI
	wevmos := common.HexToAddress(testconstants.WEVMOSContractMainnet)
	from := s.Keyring.GetAddr(0)
	ctx := s.Network.GetContext().WithGasMeter(storetypes.NewGasMeter(parentGasMeterLimit))
	keeper := s.Network.App.GetEVMKeeper()

	data, err := erc20ABI.Pack("balanceOf", from)
	s.Require().NoError(err)

	res, err := keeper.DerivedEVMCallWithData(
		ctx, from, &wevmos, data,
		true, false, false,
		big.NewInt(0), big.NewInt(610_477), nil,
	)
	s.Require().NoError(err, "a real-world gas limit must be unaffected by the uint64 guard")
	s.Require().False(res.Failed())
	s.Require().Greater(res.GasUsed, uint64(0))
	s.Require().Less(res.GasUsed, uint64(610_477),
		"the call must run inside the declared budget, not against a truncated one")
}

// TestDerivedEVMCallWithDataNilGasLimitUsesEstimation pins the nil path: the guard
// lives inside the `gasLimit != nil` branch, so a nil limit must still fall through
// to EstimateGasInternal untouched — and must certainly not be dereferenced.
func (s *KeeperTestSuite) TestDerivedEVMCallWithDataNilGasLimitUsesEstimation() {
	s.SetupTest()

	from := s.Keyring.GetAddr(0)
	// Deliberately the plain context: gas estimation binary-searches the EVM and
	// this test is about the nil branch, not about metering.
	ctx := s.Network.GetContext()
	keeper := s.Network.App.GetEVMKeeper()

	// A STOP-only contract is the one target whose cost the estimator can predict
	// exactly. The estimator is documented to underpredict on richer targets — a
	// pre-existing property of this path that the guard neither causes nor fixes.
	target := s.deployRawCode(ctx, stopCode)

	var res *evmtypes.MsgEthereumTxResponse
	var err error
	s.Require().NotPanics(func() {
		res, err = keeper.DerivedEVMCallWithData(
			ctx, from, &target, nil,
			true,  // commit — the estimation branch only runs when committing
			false, // gasless
			false, // isModuleSender
			big.NewInt(0), nil, nil,
		)
	}, "a nil gasLimit must never reach the IsUint64 guard")

	s.Require().NoError(err)
	s.Require().NotErrorIs(err, evmtypes.ErrInvalidGasLimit,
		"the guard lives inside the gasLimit != nil branch and must not fire on nil")
	s.Require().NotNil(res)
	s.Require().False(res.Failed())
	s.Require().Greater(res.GasUsed, uint64(0),
		"the estimation path must still produce a workable budget")
}
