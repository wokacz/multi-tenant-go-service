# Atlas reads the schema from the GORM models rather than from a hand-written
# SQL file, so the models stay the single source of truth. `task migrate:diff`
# turns any drift between them and migrations/ into a new versioned migration.

data "external_schema" "gorm" {
  # loader is its own module; see the comment in loader/go.mod for why.
  program = ["go", "run", "-C", "loader", "."]
}

env "local" {
  src = data.external_schema.gorm.url

  # A throwaway database Atlas uses to compute the diff. It is created and
  # dropped per run, so it never touches the development data. The version has
  # to match production, or the diff is computed against different defaults.
  dev = "docker://postgres/18/dev?search_path=public"

  migration {
    dir = "file://migrations"
  }

  format {
    migrate {
      diff = "{{ sql . \"  \" }}"
    }
  }
}
