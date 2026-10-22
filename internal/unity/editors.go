package unity

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/weslo/upool/internal/output"
)

// EditorsAt runs `unity status --format json` against the slot at slot, with UNITY_PROJECT_PATH
// set to it, which filters the listing to that project (docs/contracts.md (c)), and returns how
// many Editor instances it lists. A slot with none is STATUS_NO_INSTANCES, exit 6
// (testdata/captures/unity/status-no-instances.json), which EditorsAt reports as 0 and no error;
// any other failure is Reply.Err's.
func (c Client) EditorsAt(ctx context.Context, slot string) (int, error) {
	reply, err := c.Call(ctx, VerbStatus, slot)
	if err != nil {
		return 0, err
	}
	if err := reply.Err(); err != nil {
		var f *output.Failure
		if errors.As(err, &f) && string(f.Code()) == codeStatusNoInstances {
			return 0, nil
		}
		return 0, err
	}
	var data struct {
		Instances []json.RawMessage `json:"instances"`
	}
	if err := json.Unmarshal(reply.Response.Data, &data); err != nil {
		return 0, output.Fail(output.CodeCommandFailed, "unity status %s listed no instances: %v",
			slot, err)
	}
	return len(data.Instances), nil
}

// License is the machine's Unity licence, as `unity license status` reports it
// (testdata/captures/unity/license-status.json).
type License struct {
	Active   bool   `json:"active"`
	SignedIn bool   `json:"signedIn"`
	AuthMode string `json:"authMode"`
	Licenses []struct {
		Product string `json:"product"`
	} `json:"licenses"`
}

// LicenseStatus runs `unity license status --format json` in dir, the licence being the
// machine's and no project's, and returns the licence it reports, or Reply.Err's error.
func (c Client) LicenseStatus(ctx context.Context, dir string) (License, error) {
	reply, err := c.Call(ctx, VerbLicenseStatus, dir)
	if err == nil {
		err = reply.Err()
	}
	if err != nil {
		return License{}, err
	}
	var l License
	if err := json.Unmarshal(reply.Response.Data, &l); err != nil {
		return License{}, output.Fail(output.CodeCommandFailed,
			"unity license status reported no licence: %v", err)
	}
	return l, nil
}
