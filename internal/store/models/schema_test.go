package models_test

import (
	"slices"
	"sync"
	"testing"

	"gorm.io/gorm/schema"

	"github.com/wokacz/go-example/internal/store/models"
)

// GORM builds a composite index only when several fields carry the same index
// name. A lone `index:name,priority:1` tag silently degrades to a single-column
// index, which is easy to write and impossible to notice — hence these tests.
func indexOf(t *testing.T, model any, name string) *schema.Index {
	t.Helper()

	s, err := schema.Parse(model, &sync.Map{}, schema.NamingStrategy{})
	if err != nil {
		t.Fatalf("schema.Parse(%T) = %v, want nil", model, err)
	}

	for _, idx := range s.ParseIndexes() {
		if idx.Name == name {
			return idx
		}
	}

	t.Fatalf("index %q not found on %T", name, model)

	return nil
}

func indexColumns(idx *schema.Index) []string {
	cols := make([]string, 0, len(idx.Fields))
	for _, f := range idx.Fields {
		cols = append(cols, f.DBName)
	}

	return cols
}

func TestDeviceFingerprintIsUniquePerUser(t *testing.T) {
	idx := indexOf(t, &models.Device{}, "idx_device_user_fp")

	want := []string{"user_id", "fingerprint"}
	if got := indexColumns(idx); !slices.Equal(got, want) {
		t.Errorf("idx_device_user_fp columns = %v, want %v "+
			"(a fingerprint-only unique index would bar two users from sharing a device)", got, want)
	}
	if idx.Class != "UNIQUE" {
		t.Errorf("idx_device_user_fp class = %q, want %q", idx.Class, "UNIQUE")
	}
}

func TestLoginEventTimeIndexes(t *testing.T) {
	tests := map[string][]string{
		"idx_login_user_time":   {"user_id", "created_at"},
		"idx_login_device_time": {"device_id", "created_at"},
	}

	for name, want := range tests {
		t.Run(name, func(t *testing.T) {
			if got := indexColumns(indexOf(t, &models.LoginEvent{}, name)); !slices.Equal(got, want) {
				t.Errorf("%s columns = %v, want %v", name, got, want)
			}
		})
	}
}
