package skills

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/cdmx-in/kera/internal/access"
	"github.com/cdmx-in/kera/internal/config"
	"github.com/cdmx-in/kera/internal/db"
)

// CLIUsage is the help text of `kera skills`.
const CLIUsage = `  kera skills import <zip-or-dir> [--grant PRINCIPAL]...
                          add or update skills by name (PRINCIPAL: user:<id>, group:<name|id>, *; default admins only)
  kera skills list        list skills
`

type multi []string

func (m *multi) String() string     { return fmt.Sprint(*m) }
func (m *multi) Set(v string) error { *m = append(*m, v); return nil }

// Command runs `kera skills ...`.
func Command(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 || args[0] != "import" && args[0] != "list" {
		return errors.New("usage:\n" + CLIUsage)
	}
	fs := flag.NewFlagSet("skills "+args[0], flag.ContinueOnError)
	var grants multi
	fs.Var(&grants, "grant", "grant read to a principal (repeatable)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	var src string
	if fs.NArg() > 0 { // flags may follow the path
		src = fs.Arg(0)
		if err := fs.Parse(fs.Args()[1:]); err != nil {
			return err
		}
	}
	if args[0] == "import" && (src == "" || fs.NArg() > 0) {
		return errors.New("usage:\n" + CLIUsage)
	}
	env, err := config.Load(false)
	if err != nil {
		return err
	}
	pool, err := db.Open(ctx, env.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if _, err := db.Migrate(ctx, pool); err != nil {
		return err
	}
	if args[0] == "list" {
		rows, err := All(ctx, pool)
		if err != nil {
			return err
		}
		for _, r := range rows {
			state := "enabled"
			if !r.Enabled {
				state = "disabled"
			}
			fmt.Fprintf(out, "%-36s %-8s %3d files  %s\n", r.Name, state, r.Files, clip(r.Description, 80))
		}
		return nil
	}
	var gs []access.Grant
	for _, p := range grants {
		p, err := resolveGroup(ctx, pool, p)
		if err != nil {
			return err
		}
		gs = append(gs, access.Grant{Principal: p})
	}
	list, warns, err := Load(src)
	if err != nil {
		return err
	}
	for _, w := range warns {
		fmt.Fprintln(out, "warning:", w)
	}
	if len(list) == 0 {
		return fmt.Errorf("%s holds no SKILL.md", src)
	}
	res, err := Import(ctx, pool, list, gs)
	if err != nil {
		return err
	}
	n := map[string]int{}
	for _, r := range res {
		n[r.Outcome]++
		fmt.Fprintf(out, "%-9s  %s\n", r.Outcome, r.Name)
	}
	fmt.Fprintf(out, "%d created, %d updated, %d unchanged\n", n[Created], n[Updated], n[Unchanged])
	return nil
}
