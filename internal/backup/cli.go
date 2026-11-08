package backup

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
)

// CLIUsage documents `mangaveyra backup`.
const CLIUsage = `usage:
  mangaveyra backup create [--kind manual|export|pre_update]
  mangaveyra backup list [--kind <kind>]
  mangaveyra backup restore <id|path> --yes
  mangaveyra backup delete <id> --yes

restore replaces every account, project and setting with the archive's. It
refuses while a server is running against the same database (stop it first, or
restore from Admin → Backup & import), turns maintenance mode on for its
duration and runs migrations when the archive is from an older version.`

// CLIOptions are the flags of `mangaveyra backup`.
type CLIOptions struct {
	Kind *string
	Yes  *bool
}

// CLIFlags registers the backup flags on fs.
func CLIFlags(fs *flag.FlagSet) CLIOptions {
	return CLIOptions{
		Kind: fs.String("kind", "", "backup kind: manual (default for create), export or pre_update; filters list"),
		Yes:  fs.Bool("yes", false, "confirm restore or delete"),
	}
}

// ParseInterspersed parses flags that may follow positional arguments
// ("restore 01J… --yes") and returns the positional arguments.
func ParseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return pos, nil
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}

// RunCLI implements `mangaveyra backup <create|list|restore|delete>`.
func RunCLI(ctx context.Context, d *core.Deps, sub string, args []string, o CLIOptions, out io.Writer) error {
	s := newService(d)
	kind, yes := "", false
	if o.Kind != nil {
		kind = *o.Kind
	}
	if o.Yes != nil {
		yes = *o.Yes
	}
	switch sub {
	case "create":
		if kind == "" {
			kind = KindManual
		}
		if kind != KindManual && kind != KindExport && kind != KindPreUpdate {
			return fmt.Errorf("backup: --kind must be manual, export or pre_update")
		}
		start := time.Now()
		b, err := s.Create(ctx, Request{Kind: kind})
		if err != nil {
			return err
		}
		path, _, _ := s.Path(ctx, b.ID)
		fmt.Fprintf(out, "created %s (%s, %s, %s)\n%s\n", b.ID, b.Kind, formatSize(b.Bytes), time.Since(start).Round(time.Millisecond), path)
		return nil
	case "list":
		list, err := s.List(ctx, kind, 200)
		if err != nil {
			return err
		}
		tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tKIND\tSTATUS\tCREATED\tSIZE\tSCHEMA")
		for _, b := range list {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", b.ID, b.Kind, b.Status, b.CreatedAt.Format("2006-01-02 15:04:05"), formatSize(b.Bytes), b.Schema)
		}
		return tw.Flush()
	case "restore":
		if len(args) != 1 {
			return errors.New(CLIUsage)
		}
		if !yes {
			return errors.New("backup: restore replaces all data on this server; run it again with --yes to confirm")
		}
		running, err := s.ServerRunning(ctx)
		if err != nil {
			return err
		}
		if running {
			return errors.New("backup: a MangaVeyra server is running against this database; stop it first, or restore from Admin → Backup & import")
		}
		id := args[0]
		if !db.ValidID(id) {
			b, err := s.Register(ctx, id, "", false)
			if err != nil {
				return err
			}
			id = b.ID
		}
		turnedOn, err := setMaintenance(ctx, d, true, "")
		if err != nil {
			return err
		}
		rep, rerr := s.restoreArchive(ctx, id, "")
		if turnedOn {
			if _, err := setMaintenance(ctx, d, false, ""); err != nil && rerr == nil {
				rerr = err
			}
		}
		if rerr != nil {
			return rerr
		}
		fmt.Fprintf(out, "restored %s: %d tables, %d rows, %d media objects (schema %s)\n", id, rep.Tables, rep.Rows, rep.Media, rep.FromSchema)
		if len(rep.Migrated) > 0 {
			fmt.Fprintf(out, "migrated: %s\n", strings.Join(rep.Migrated, ", "))
		}
		if rep.MasterKeyDiffers {
			fmt.Fprintln(out, "warning: the archive was sealed with a different master key. Provider keys and World tokens must be entered again, and members with two-factor sign-in must set it up again.")
		}
		return nil
	case "delete":
		if len(args) != 1 {
			return errors.New(CLIUsage)
		}
		if !yes {
			return errors.New("backup: run delete again with --yes to remove the archive")
		}
		if err := s.Delete(ctx, args[0]); err != nil {
			return err
		}
		fmt.Fprintf(out, "deleted %s\n", args[0])
		return nil
	}
	return errors.New(CLIUsage)
}

// setMaintenance switches maintenance mode and reports whether it changed it.
func setMaintenance(ctx context.Context, d *core.Deps, on bool, message string) (bool, error) {
	d.Settings.Invalidate()
	inst, err := d.Settings.Instance(ctx)
	if err != nil {
		return false, err
	}
	if inst.Maintenance.Enabled == on {
		return false, nil
	}
	_, err = d.Settings.UpdateInstance(ctx, "", func(i *settings.Instance) error {
		i.Maintenance.Enabled = on
		if on {
			i.Maintenance.Message = message
		}
		return nil
	})
	return err == nil, err
}
