//go:build !unix

package update

import "errors"

func execSelf(string) error {
	return errors.New("update: restarting in place is not supported on this platform; start the server again")
}
