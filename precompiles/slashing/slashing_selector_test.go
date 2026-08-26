package slashing_test

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/params"
	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	cmn "github.com/cosmos/evm/precompiles/common"
	"github.com/cosmos/evm/precompiles/slashing"
)

// runPrecompile drives slashing.Precompile.Run with a nil StateDB and returns the
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

	bz, err := slashing.NewPrecompile(nil, nil, nil, nil, nil).Run(evm, contract, false)
	require.ErrorIs(t, err, vm.ErrExecutionReverted, "precompile errors are always surfaced as a revert")

	return string(bz)
}

// TestRunResolvesSelectorBeforeNativeAction covers F-2026-18786: calldata that
// cannot be dispatched used to run the whole native-action preamble - cache
// context, multi-store snapshot and a state DB commit that replays the caller's
// entire dirty set - before SetupABI got around to failing on the selector, and
// RequiredGas returns 0 for an unresolvable selector so none of it was charged.
func TestRunResolvesSelectorBeforeNativeAction(t *testing.T) {
	testCases := []struct {
		name string
		// input is the calldata handed to the precompile.
		input []byte
		// reachesNativeAction is true when the calldata is dispatchable and so
		// must still be handed to RunNativeAction.
		reachesNativeAction bool
		// expReason, when set, must appear in the revert reason.
		expReason string
	}{
		{
			name:      "unknown selector is rejected before the native action",
			input:     []byte{0xde, 0xad, 0xbe, 0xef},
			expReason: "no method with id",
		},
		{
			name:  "calldata shorter than a selector is rejected before the native action",
			input: []byte{0x01, 0x02, 0x03},
		},
		{
			name:  "empty calldata is rejected before the native action",
			input: nil,
		},
		{
			name:                "a valid selector is still dispatched",
			input:               slashing.ABI.Methods[slashing.GetParamsMethod].ID,
			reachesNativeAction: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			reason := runPrecompile(t, tc.input, uint256.NewInt(0))

			if tc.reachesNativeAction {
				require.Contains(t, reason, cmn.ErrNotRunInEvm,
					"a valid selector must still reach RunNativeAction")
				return
			}

			require.NotContains(t, reason, cmn.ErrNotRunInEvm,
				"undispatchable calldata must not reach the native-action preamble (F-2026-18786)")

			if tc.expReason != "" {
				require.Contains(t, reason, tc.expReason)
			}
		})
	}
}
