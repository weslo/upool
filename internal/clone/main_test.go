package clone

import (
	"testing"

	"github.com/weslo/upool/internal/unity/fake"
)

// TestMain lets this package's test binary stand in for `unity`, for the cache key tests
// (internal/unity/fake; CONTRIBUTING.md, "Testing").
func TestMain(m *testing.M) { fake.Main(m) }
