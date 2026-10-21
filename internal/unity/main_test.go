package unity

import (
	"testing"

	"github.com/weslo/upool/internal/unity/fake"
)

// TestMain lets the test binary act as the fake unity (CONTRIBUTING.md, "Testing").
func TestMain(m *testing.M) { fake.Main(m) }
