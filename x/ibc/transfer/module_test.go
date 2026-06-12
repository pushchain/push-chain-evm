package transfer

import (
	"testing"

	ibctransfer "github.com/cosmos/ibc-go/v10/modules/apps/transfer"
	"github.com/stretchr/testify/require"
)

// TestConsensusVersionMatchesHighestMigration guards the IBC transfer override's
// effective consensus version (regression guard for F-2026-17753).
//
// RegisterServices registers migrations up to 5->6 (MigrateDenomTraceToDenom), so the
// consensus version the SDK module manager reads must be 6.
//
// Subtlety this test pins down: the local `const consensusVersion = 5` is declared on
// AppModuleBasic, which the manager does NOT read for migrations — RunMigrations and
// GetVersionMap call ConsensusVersion() on the registered AppModule (cosmos-sdk
// types/module/module.go). The override's AppModule defines no ConsensusVersion() of its
// own; it inherits 6 from the embedded upstream ibc-go AppModule. So the effective version
// is 6 and the 5->6 migration runs as intended. If anyone pins the AppModule to 5 (or the
// embedded upstream version changes), this test fails.
func TestConsensusVersionMatchesHighestMigration(t *testing.T) {
	const highestRegisteredMigrationTarget uint64 = 6 // RegisterMigration(..., 5, MigrateDenomTraceToDenom)

	am := AppModule{AppModule: &ibctransfer.AppModule{}}
	t.Logf("effective AppModule.ConsensusVersion() = %d   (AppModuleBasic = %d, dead/unused)",
		am.ConsensusVersion(), AppModuleBasic{}.ConsensusVersion())

	require.Equal(t, highestRegisteredMigrationTarget, am.ConsensusVersion(),
		"effective ConsensusVersion (read off AppModule) must equal the highest registered migration target (5->6)")

	// The override must not diverge from upstream ibc-go's transfer consensus version.
	require.Equal(t, (ibctransfer.AppModule{}).ConsensusVersion(), am.ConsensusVersion(),
		"override AppModule.ConsensusVersion must match upstream ibc-go")
}
