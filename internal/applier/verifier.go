package applier

import (
	"errors"
	"fmt"
	"strings"

	"github.com/skeema/skeema/internal/fs"
	"github.com/skeema/skeema/internal/tengo"
	"github.com/skeema/skeema/internal/workspace"
)

// VerifierOptions specifies configuration for the diff verification operation.
// All fields are mandatory, even though some may be redundant with
// WorkspaceOptions in some situations.
type VerifierOptions struct {
	AllAlters           bool // if false, only verify unsupported alter diffs; if true, verify all alter diffs
	Flavor              tengo.Flavor
	DefaultCharacterSet string
	DefaultCollation    string
	WorkspaceOptions    workspace.Options
}

// VerifierOptionsForTarget returns VerifierOptions based on the target's
// configuration.
func VerifierOptionsForTarget(t *Target) (opts VerifierOptions, err error) {
	opts = VerifierOptions{
		AllAlters:           t.Dir.Config.GetBool("verify"),
		Flavor:              t.Instance.Flavor(),
		DefaultCharacterSet: t.Dir.Config.Get("default-character-set"),
		DefaultCollation:    t.Dir.Config.Get("default-collation"),
	}
	opts.WorkspaceOptions, err = workspace.OptionsForDir(t.Dir, t.Instance)
	return
}

// VerifyDiff verifies the result of AlterTable values found in diff.TableDiffs,
// confirming that applying the corresponding ALTER would bring a table from the
// version currently in the instance to the version specified in the filesystem.
func VerifyDiff(diff *tengo.SchemaDiff, vopts VerifierOptions) error {
	// If diff contains no ALTER TABLEs, nothing to verify
	altersInDiff := diff.FilteredTableDiffs(tengo.DiffTypeAlter)
	if len(altersInDiff) == 0 {
		return nil
	}

	// The goal of VerifyDiff is to confirm that the diff contains the correct and
	// complete set of differences between all modified tables. We use a strict set
	// of statement modifiers that will transform the initial state into an exact
	// match of the desired state. This is all run in a workspace, so we can be
	// more aggressive about which statement modifiers are used in generating the
	// ALTER here. When the diff is actually used against the real live table
	// later, a different looser set of modifiers is used which filters out some
	// of the undesired cosmetic clauses by default.
	mods := tengo.StatementModifiers{
		NextAutoInc:            tengo.NextAutoIncAlways,      // use whichever auto_increment is in the fs
		Partitioning:           tengo.PartitioningPermissive, // ditto with partitioning status
		AllowUnsafe:            true,                         // needed since we're just running against the temp schema
		AlgorithmClause:        "copy",                       // needed so the DB doesn't ignore attempts to re-order indexes
		StrictIndexOrder:       true,                         // needed since we want the SHOW CREATE TABLEs to match
		StrictCheckOrder:       true,                         // ditto (only affects MariaDB)
		StrictForeignKeyNaming: true,                         // ditto
		StrictColumnDefinition: true,                         // ditto (only affects MySQL 8 edge cases)
		SkipPreDropAlters:      true,                         // ignore DROP PARTITIONs that were only generated to speed up a DROP TABLE
		Flavor:                 vopts.Flavor,
	}
	if mods.Flavor.Matches(tengo.FlavorMySQL55) {
		mods.AlgorithmClause = "" // MySQL 5.5 doesn't support ALGORITHM clause
	}

	// Gather CREATE and ALTER for modified tables, and put into a LogicalSchema,
	// which we then materialize into a real schema using a workspace.
	// Even if verify is disabled, we still must look for unsupported diffs, to
	// potentially mark some as supported (if they generate non-blank SQL which
	// properly verifies due to not actually touching unsupported features)
	logicalSchema := fs.NewLogicalSchema()
	logicalSchema.CharSet = vopts.DefaultCharacterSet
	logicalSchema.Collation = vopts.DefaultCollation
	desiredTables := make(map[string]*tengo.Table)
	unsupportedTables := make(map[string]*tengo.TableDiff)
	for _, td := range altersInDiff {
		stmt, err := td.Statement(mods)
		if stmt == "" {
			continue
		} else if err != nil && tengo.IsUnsupportedDiff(err) {
			unsupportedTables[td.From.Name] = td
		} else if !vopts.AllAlters {
			continue
		}

		// Note: sometimes a table's diff gets split into multiple ALTERs, but this
		// logic can ignore that fact. If there are redundant AddStatement calls for
		// one CREATE, the first AddStatement for that CREATE succeeds and the
		// subsequent duplicate CREATEs error, but that is harmless in this code path!
		logicalSchema.AddStatement(&tengo.Statement{
			Type:       tengo.StatementTypeCreate,
			Text:       td.From.CreateStatement,
			ObjectType: tengo.ObjectTypeTable,
			ObjectName: td.From.Name,
		})
		logicalSchema.AddStatement(&tengo.Statement{
			Type:       tengo.StatementTypeAlter,
			Text:       stmt,
			ObjectType: tengo.ObjectTypeTable,
			ObjectName: td.From.Name,
		})
		desiredTables[td.From.Name] = td.To
	}

	// Return early if --verify was disabled and there were no verifiable
	// unsupported tables
	if len(desiredTables) == 0 {
		return nil
	}

	wsSchema, err := workspace.ExecLogicalSchema(logicalSchema, vopts.WorkspaceOptions)
	if err == nil && len(wsSchema.Failures) > 0 {
		err = wsSchema.Failures[0]
	}
	if err != nil {
		return fmt.Errorf("Diff verification failure: %s", err.Error())
	}

	// Compare the "expected" version of each table ("to" side of original diff,
	// from the filesystem) with the "actual" version (from the workspace after the
	// generated ALTERs were run there) by running a second diff. Verification
	// is successful if this second diff has no clauses (tables completely and
	// exactly match) or only a blank statement (suppressed by StatementModifiers).
	// We use very strict StatementModifiers here, except StrictColumnDefinition
	// must be omitted because MySQL 8 behaves inconsistently with respect to
	// superfluous column-level charset/collation clauses in some specific edge-
	// cases. (These MySQL 8 discrepancies are purely cosmetic, safe to ignore.)
	mods.StrictColumnDefinition = false
	mods.AlgorithmClause = ""
	actualTables := wsSchema.TablesByName()
	for name, desiredTable := range desiredTables {
		// If an unsupported diff passes verification, mark it as supported, but
		// otherwise we can just ignore any error from an unsupported diff.
		td, wasUnsupported := unsupportedTables[name]
		if err := verifyTable(actualTables[name], desiredTable, mods); err == nil && wasUnsupported {
			td.MarkSupported()
		} else if err != nil && !wasUnsupported {
			return err
		}
	}
	return nil
}

// verifyTable confirms that a table has the expected structure by doing an
// additional diff. Typically this diff will return quickly based on SHOW CREATE
// TABLE matching, but if they don't match (as happens with some MySQL 8 edge-
// cases) it will do a full structural comparison of the tables' fields. If this
// second diff returns a non-empty ALTER, an error, or an unsupported diff, it
// means the first diff did not properly do its job, so verification fails.
func verifyTable(actual, desired *tengo.Table, mods tengo.StatementModifiers) error {
	var unsupportedErr *tengo.UnsupportedDiffError
	td := tengo.NewAlterTable(actual, desired)
	stmt, err := td.Statement(mods)
	header := "Diff verification failure on table " + desired.Name
	if errors.As(err, &unsupportedErr) {
		unsupportedErr.Reason = strings.Replace(unsupportedErr.Reason, "original state", "post-verification state", 1)
		unsupportedErr.ExpectedDesc = strings.Replace(unsupportedErr.ExpectedDesc, "original state", "post-verification state", 1)
		unsupportedErr.ActualDesc = strings.Replace(unsupportedErr.ActualDesc, "original state", "post-verification state", 1)
		return fmt.Errorf(header+". This may indicate a Skeema bug.\nRun command again with --skip-verify if this discrepancy is safe to ignore.\nDebug details: %s", unsupportedErr.ExtendedError())
	} else if err != nil {
		return fmt.Errorf(header+" due to unexpected error: %w.\nRun command again with --skip-verify if this is safe to ignore.", err)
	} else if stmt != "" {
		return fmt.Errorf(header+": the generated ALTER TABLE does not fully bring the table to the desired state.\nRun command again with --skip-verify if this discrepancy is safe to ignore.\nDebug details: secondary verification diff is non-empty, yielding this DDL: %s", stmt)
	}
	return nil
}
