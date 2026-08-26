package werc20_test

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/params"
	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	cmn "github.com/cosmos/evm/precompiles/common"
	"github.com/cosmos/evm/precompiles/werc20"
	erc20types "github.com/cosmos/evm/x/erc20/types"
)

// runPrecompile drives werc20.Precompile.Run with a nil StateDB and returns the
// ABI-encoded revert reason.
//
// The early selector guard never looks at evm.StateDB, whereas the first thing
// RunNativeAction does is type-assert on it. A revert reason carrying
// cmn.ErrNotRunInEvm therefore proves the call reached the native-action
// preamble, and any other reason proves it was rejected before it - which is
// what lets these tests tell the two apart without standing up a full app.
func runPrecompile(t *testing.T, input []byte, value *uint256.Int) string {
	t.Helper()

	evm := vm.NewEVM(vm.BlockContext{BlockNumber: big.NewInt(1)}, nil, params.TestChainConfig, vm.Config{})
	contract := vm.NewContract(common.Address{}, common.Address{}, value, 100_000, nil)
	contract.Input = input

	bz, err := werc20.NewPrecompile(erc20types.TokenPair{}, nil, nil, nil).Run(evm, contract, false)
	require.ErrorIs(t, err, vm.ErrExecutionReverted, "precompile errors are always surfaced as a revert")

	return string(bz)
}

// TestRunPreservesFallbackAndReceive is the regression guard for F-2026-18786.
//
// WERC20 is the one affected precompile whose ABI declares a `fallback` and a
// `receive`, and it leans on all three of SetupABI's special cases: empty
// calldata, calldata shorter than a selector, and an unknown selector all
// resolve to fallback/receive and behave like `deposit`. A naive
// "len(input) < 4 || MethodById fails -> revert" guard at the top of Run would
// have broken every one of them, so the guard resolves the method through
// cmn.ResolveMethod - the same helper SetupABI itself uses - instead.
func TestRunPreservesFallbackAndReceive(t *testing.T) {
	testCases := []struct {
		name  string
		input []byte
		value *uint256.Int
	}{
		{
			name:  "empty calldata with value resolves to receive",
			input: nil,
			value: uint256.NewInt(1),
		},
		{
			name:  "empty calldata without value resolves to fallback",
			input: nil,
			value: uint256.NewInt(0),
		},
		{
			name:  "calldata shorter than a selector resolves to fallback",
			input: []byte{0x01, 0x02, 0x03},
			value: uint256.NewInt(1),
		},
		{
			name:  "unknown selector resolves to fallback",
			input: []byte("nonExistingMethod"),
			value: uint256.NewInt(1),
		},
		{
			name:  "a valid selector is still dispatched",
			input: werc20.ABI.Methods[werc20.DepositMethod].ID,
			value: uint256.NewInt(1),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			reason := runPrecompile(t, tc.input, tc.value)

			require.Contains(t, reason, cmn.ErrNotRunInEvm,
				"WERC20 dispatches this calldata via fallback/receive, so it must still reach RunNativeAction")
			require.NotContains(t, reason, "no method with id",
				"the early selector guard must not reject calldata that fallback/receive would have handled")
		})
	}
}
