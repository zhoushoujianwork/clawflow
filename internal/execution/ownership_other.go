//go:build !darwin && !linux

package execution

import "os"

func acquireOwnershipFile(path string) (*os.File, error) {
	return nil, &Error{Kind: "ownership_unsupported"}
}
