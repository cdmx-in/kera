package apply

import (
	"fmt"

	"github.com/cdmx-in/kera/internal/access"
	"github.com/cdmx-in/kera/internal/skills"
)

// skillState is what apply compares for one skill: its text (as a digest)
// and its managed grants. enabled and lazy belong to the admin console.
type skillState struct {
	Description string         `yaml:"description"`
	Digest      string         `yaml:"text"`
	Grants      []access.Grant `yaml:"grants,omitempty"`
}

// skills imports the skills of every skills block (dir and import_zip) by
// name; each skill gets the grants of the block it came from.
func (r *run) skills() error {
	if len(r.f.Skills) == 0 {
		return nil
	}
	byName := map[string]skills.Skill{}
	var items []item
	for _, sk := range r.f.Skills {
		grants := r.normGrants(0, "skills", sk.Grants)
		for _, p := range []string{sk.Dir, sk.ImportZip} {
			if p == "" {
				continue
			}
			got, warns, err := skills.Load(p)
			if err != nil {
				r.errs.add(0, "skills: %v", err)
				return nil
			}
			for _, w := range warns {
				r.res.Warnings = append(r.res.Warnings, "skills: "+w)
			}
			for _, s := range got {
				if _, dup := byName[s.Name]; dup {
					r.res.Warnings = append(r.res.Warnings, fmt.Sprintf("skills: %q is declared twice; the first is used", s.Name))
					continue
				}
				byName[s.Name] = s
				items = append(items, item{id: s.Name, v: skillState{Description: s.Description, Digest: s.Digest(), Grants: grants}})
			}
		}
	}
	return r.reconcile("skill", true, items, r.readSkills,
		func(it item) error {
			id, _, err := skills.Upsert(r.ctx, r.tx, byName[it.id], true)
			if err != nil {
				return err
			}
			return r.setGrants(access.Skill, id, it.v.(skillState).Grants)
		},
		func(name string) error {
			return r.exec(`WITH d AS (DELETE FROM skill WHERE name = $1 AND managed RETURNING id)
				DELETE FROM access_grant WHERE resource_type = 'skill' AND resource_id IN (SELECT id FROM d)`, name)
		})
}

func (r *run) readSkills() ([]row, error) {
	grants, err := r.readGrants(access.Skill)
	if err != nil {
		return nil, err
	}
	rows, err := r.tx.Query(r.ctx, `SELECT id, name, description, content, resources, managed FROM skill ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("read skills: %w", err)
	}
	defer rows.Close()
	var out []row
	for rows.Next() {
		var id string
		var s skills.Skill
		var managed bool
		if err := rows.Scan(&id, &s.Name, &s.Description, &s.Content, &s.Resources, &managed); err != nil {
			return nil, fmt.Errorf("read skill: %w", err)
		}
		out = append(out, row{id: s.Name, v: skillState{Description: s.Description, Digest: s.Digest(), Grants: grants[id]}, managed: managed})
	}
	return out, rows.Err()
}
